package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/skill"
)

const (
	farmPivotName = "current"
	farmSkillsDir = "skills"
	farmSkillFile = "SKILL.md"
	farmNoteLimit = 5
)

var replaceSymlink = fsutil.ReplaceSymlink

type FarmAction string

const (
	FarmLinked  FarmAction = "linked"
	FarmPruned  FarmAction = "pruned"
	FarmStubbed FarmAction = "stubbed"
	FarmNoop    FarmAction = "noop"
	FarmSkipped FarmAction = "skipped"
)

type FarmResult struct {
	Agent  string     `json:"agent"`
	Plugin string     `json:"plugin"`
	Action FarmAction `json:"action"`
	Count  int        `json:"count,omitempty"`
	Note   string     `json:"note,omitempty"`
}

type farmPlan struct {
	Parked      map[string][]string
	Quarantined map[string]pluginLedgerRec
	Owner       map[string]string
	Desired     map[string]string
	Canon       map[string]struct{}
}

func (e *Engine) syncPluginSurfaces(ctx context.Context, report *Report, active []*agent.Agent, opts SyncOptions) {
	results, warnings, err := e.reconcilePlugins(ctx)

	report.Warnings = append(report.Warnings, warnings...)

	if err != nil {
		report.Warnings = append(report.Warnings, "plugins: "+err.Error())
	}

	report.Plugins = results

	if opts.Direction == config.ModePull || !selected(opts.Kinds, kind.Skills) {
		return
	}

	farm, farmWarnings := e.farmPluginSkills(active)

	report.Farm = farm
	report.Warnings = append(report.Warnings, farmWarnings...)
}

func (e *Engine) farmPluginSkills(active []*agent.Agent) ([]FarmResult, []string) {
	ledger, warns, err := loadPluginLedger(e.vault.PluginsLedgerPath())
	if err != nil {
		return nil, append(warns, "plugin farm: "+err.Error())
	}

	if e.home == "" {
		return nil, warns
	}

	plan, planWarns := e.buildFarmPlan(ledger)

	warns = append(warns, planWarns...)

	var results []FarmResult

	for _, a := range active {
		surface := a.Surface(kind.Skills)
		if surface == nil || !e.config.KindEnabled(kind.Skills) {
			continue
		}

		if e.config.ModeFor(a.ID, kind.Skills, surface.Traits().DefaultMode) == config.ModeOff {
			continue
		}

		agentResults, agentWarns := e.farmAgentSkills(a.ID, surface.Path(), plan)

		results = append(results, agentResults...)
		warns = append(warns, agentWarns...)
	}

	slices.SortFunc(results, func(a, b FarmResult) int {
		return cmp.Or(
			cmp.Compare(a.Agent, b.Agent),
			cmp.Compare(a.Plugin, b.Plugin),
			cmp.Compare(a.Action, b.Action),
		)
	})

	return results, warns
}

func (e *Engine) buildFarmPlan(ledger pluginLedger) (farmPlan, []string) {
	plan := farmPlan{
		Parked:      map[string][]string{},
		Quarantined: map[string]pluginLedgerRec{},
		Owner:       map[string]string{},
		Desired:     map[string]string{},
		Canon:       map[string]struct{}{},
	}

	warns := e.scanLedgerPlan(plan, ledger)

	for _, key := range slices.Sorted(maps.Keys(plan.Parked)) {
		marketplace, name, ok := strings.Cut(key, "/")
		if !ok {
			continue
		}

		for _, skillName := range plan.Parked[key] {
			if owner, taken := plan.Owner[skillName]; taken {
				warns = append(warns, fmt.Sprintf("plugin farm: skill %s of %s is already provided by %s", skillName, key, owner))

				continue
			}

			plan.Owner[skillName] = key
			plan.Desired[skillName] = filepath.Join(e.vault.PluginsDir(), marketplace, name, farmPivotName, farmSkillsDir, skillName)
		}
	}

	canon, canonWarns := e.canonSkillNames()

	plan.Canon = canon

	warns = append(warns, canonWarns...)

	for _, skillName := range slices.Sorted(maps.Keys(plan.Desired)) {
		if _, shadowed := canon[normSkillName(skillName)]; !shadowed {
			continue
		}

		warns = append(warns, fmt.Sprintf("plugin farm: skill %s is shadowed by the vault canon", skillName))

		delete(plan.Desired, skillName)
	}

	return plan, warns
}

func (e *Engine) scanLedgerPlan(plan farmPlan, ledger pluginLedger) []string {
	var warns []string

	for _, key := range slices.Sorted(maps.Keys(ledger.Plugins)) {
		rec := ledger.Plugins[key]

		if !rec.QuarantinedAt.IsZero() {
			marketplace, name, ok := strings.Cut(key, "/")
			if ok && validPluginKey(marketplace, name) {
				plan.Quarantined[key] = rec
			}

			continue
		}

		if !rec.RetiredAt.IsZero() {
			continue
		}

		names, ok, pluginWarns := e.parkedPluginSkills(key, rec)

		for _, warn := range pluginWarns {
			warns = append(warns, "plugin farm: "+warn)
		}

		if !ok {
			continue
		}

		plan.Parked[key] = names
	}

	return warns
}

func (e *Engine) parkedPluginSkills(key string, rec pluginLedgerRec) ([]string, bool, []string) {
	marketplace, name, ok := strings.Cut(key, "/")
	if !ok || !validPluginKey(marketplace, name) {
		return nil, false, nil
	}

	root, warns, ok := e.pluginTargetRoot(key, rec)
	if !ok {
		return nil, false, warns
	}

	return e.scanPluginSkills(key, rec.Target, root)
}

func (e *Engine) pluginTargetRoot(key string, rec pluginLedgerRec) (string, []string, bool) {
	if !isDir(rec.Target) {
		return "", nil, false
	}

	if !e.underPluginCache(rec.Target) {
		return "", []string{fmt.Sprintf("plugin %s install path is outside the plugin cache: %s", key, rec.Target)}, false
	}

	root, err := filepath.EvalSymlinks(filepath.Join(e.home, ".claude", "plugins"))
	if err != nil {
		return "", []string{fmt.Sprintf("plugin %s cache cannot be resolved: %v", key, err)}, false
	}

	resolved, err := filepath.EvalSymlinks(rec.Target)
	if err != nil || !underDir(resolved, root) {
		return "", []string{fmt.Sprintf("plugin %s install path resolves outside the plugin cache: %s", key, rec.Target)}, false
	}

	return root, nil, true
}

func (e *Engine) scanPluginSkills(key, target, root string) ([]string, bool, []string) {
	entries, err := os.ReadDir(filepath.Join(target, farmSkillsDir))
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, true, nil
	}

	if err != nil {
		return nil, false, []string{fmt.Sprintf("plugin %s skills cannot be read: %v", key, err)}
	}

	var (
		names []string
		warns []string
	)

	for _, entry := range entries {
		path := filepath.Join(target, farmSkillsDir, entry.Name())
		if !isDir(path) || !fsutil.Exists(filepath.Join(path, farmSkillFile)) {
			continue
		}

		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || !underDir(resolved, root) {
			warns = append(warns, fmt.Sprintf("plugin %s skill %s resolves outside the plugin cache", key, entry.Name()))

			continue
		}

		names = append(names, entry.Name())
	}

	return names, true, warns
}

func (e *Engine) underPluginCache(target string) bool {
	return underDir(target, filepath.Join(e.home, ".claude", "plugins"))
}

func underDir(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

func (e *Engine) canonSkillNames() (map[string]struct{}, []string) {
	names := map[string]struct{}{}

	entries, err := os.ReadDir(e.vault.SkillsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return names, nil
	}

	if err != nil {
		return names, []string{"plugin farm: " + err.Error()}
	}

	for _, entry := range entries {
		if entry.IsDir() {
			names[normSkillName(entry.Name())] = struct{}{}
		}
	}

	return names, nil
}

func normSkillName(name string) string {
	return norm.NFC.String(strings.ToLower(name))
}

func (e *Engine) farmAgentSkills(agentID, dir string, plan farmPlan) ([]FarmResult, []string) {
	var warns []string

	if len(plan.Desired) > 0 && !isDir(dir) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, []string{"plugin farm: " + err.Error()}
		}
	}

	stubbed, stubPruned, stubWarns := e.farmStubs(dir, plan)

	warns = append(warns, stubWarns...)

	linked := map[string]int{}
	skips := map[string][]string{}

	for _, skillName := range slices.Sorted(maps.Keys(plan.Desired)) {
		plugin := plan.Owner[skillName]

		action, err := e.farmLinkSkill(dir, skillName, plan.Desired[skillName])
		switch {
		case errors.Is(err, fsutil.ErrSymlinksUnsupported):
			return []FarmResult{{Agent: agentID, Action: FarmSkipped, Note: symlinkUnsupportedNote(dir)}}, warns
		case err != nil:
			warns = append(warns, "plugin farm: "+err.Error())
		case action == FarmLinked:
			linked[plugin]++
		case action == FarmSkipped:
			skips[plugin] = append(skips[plugin], skillName)
		}
	}

	pruned, pruneWarns := e.pruneFarmLinks(dir, plan)

	warns = append(warns, pruneWarns...)

	if len(stubPruned) > 0 && pruned == nil {
		pruned = map[string]int{}
	}

	for key, count := range stubPruned {
		pruned[key] += count
	}

	var results []FarmResult

	results = append(results, farmResults(agentID, FarmStubbed, stubbed)...)
	results = append(results, farmResults(agentID, FarmLinked, linked)...)
	results = append(results, farmResults(agentID, FarmPruned, pruned)...)

	for _, plugin := range slices.Sorted(maps.Keys(skips)) {
		results = append(results, FarmResult{Agent: agentID, Plugin: plugin, Action: FarmSkipped, Note: summarizeFarmNames(skips[plugin])})
	}

	return results, warns
}

func symlinkUnsupportedNote(dir string) string {
	return fmt.Sprintf("symlinks are not supported in %s; a copy fallback is intentionally not performed", dir)
}

func farmResults(agentID string, action FarmAction, counts map[string]int) []FarmResult {
	results := make([]FarmResult, 0, len(counts))

	for _, plugin := range slices.Sorted(maps.Keys(counts)) {
		results = append(results, FarmResult{Agent: agentID, Plugin: plugin, Action: action, Count: counts[plugin]})
	}

	return results
}

func (e *Engine) farmStubs(dir string, plan farmPlan) (map[string]int, map[string]int, []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}

		return nil, nil, []string{"plugin farm: " + err.Error()}
	}

	stubbed := map[string]int{}
	pruned := map[string]int{}

	var warns []string

	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(dir, name)

		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			key, stubWarns := e.stubQuarantinedLink(path, name, plan)

			warns = append(warns, stubWarns...)

			if key != "" {
				stubbed[key]++
			}
		case entry.IsDir():
			key, ok := skill.IsStubDir(path)
			if !ok || stubStillNeeded(plan, key, name) {
				continue
			}

			if err := os.RemoveAll(path); err != nil {
				warns = append(warns, fmt.Sprintf("plugin farm: remove %s: %v", path, err))

				continue
			}

			pruned[key]++
		}
	}

	return stubbed, pruned, warns
}

func (e *Engine) stubQuarantinedLink(path, name string, plan farmPlan) (string, []string) {
	link, err := os.Readlink(path)
	if err != nil || fsutil.Exists(link) {
		return "", nil
	}

	key, skillName, ok := e.farmLinkOwner(link)
	if !ok || skillName != name {
		return "", nil
	}

	rec, ok := plan.Quarantined[key]
	if !ok {
		return "", nil
	}

	if owner, provided := plan.Owner[name]; provided && owner != key {
		return "", nil
	}

	if _, canon := plan.Canon[normSkillName(name)]; canon {
		return "", nil
	}

	if !stubInputsSafe(name, key, rec.Version) {
		return "", []string{fmt.Sprintf("plugin farm: cannot stub skill %s of %s: unsafe plugin metadata", name, key)}
	}

	if err := os.Remove(path); err != nil {
		return "", []string{fmt.Sprintf("plugin farm: remove %s: %v", path, err)}
	}

	if _, err := writePluginStub(path, name, key, rec.Version, rec.QuarantinedAt); err != nil {
		return "", []string{fmt.Sprintf("plugin farm: stub %s: %v", path, err)}
	}

	return key, nil
}

func stubStillNeeded(plan farmPlan, key, name string) bool {
	if _, parked := plan.Parked[key]; parked {
		return false
	}

	if _, canon := plan.Canon[normSkillName(name)]; canon {
		return false
	}

	if owner, provided := plan.Owner[name]; provided && owner != key {
		return false
	}

	_, quarantined := plan.Quarantined[key]

	return quarantined
}

func (e *Engine) farmLinkSkill(dir, skillName, target string) (FarmAction, error) {
	path := filepath.Join(dir, skillName)

	link, err := os.Readlink(path)
	if err == nil {
		switch {
		case !e.farmOwned(link):
			return FarmSkipped, nil
		case link == target:
			return FarmNoop, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		info, lstatErr := os.Lstat(path)
		if lstatErr == nil && info.Mode()&fs.ModeSymlink == 0 {
			return FarmSkipped, nil
		}

		return FarmNoop, fmt.Errorf("readlink %s: %w", path, err)
	}

	if err := replaceSymlink(path, target); err != nil {
		return FarmNoop, err
	}

	return FarmLinked, nil
}

func (e *Engine) pruneFarmLinks(dir string, plan farmPlan) (map[string]int, []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}

		return nil, []string{"plugin farm: " + err.Error()}
	}

	var warns []string

	pruned := map[string]int{}

	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(dir, name)

		link, err := os.Readlink(path)
		if err != nil {
			continue
		}

		plugin, skillName, ok := e.farmLinkOwner(link)
		if !ok || skillName != name {
			continue
		}

		if !e.farmPrunable(plan, plugin, name) {
			continue
		}

		info, err := os.Lstat(path)
		if err != nil || info.Mode()&fs.ModeSymlink == 0 {
			continue
		}

		if err := os.Remove(path); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				warns = append(warns, fmt.Sprintf("plugin farm: remove %s: %v", path, err))
			}

			continue
		}

		pruned[plugin]++
	}

	return pruned, warns
}

func (e *Engine) farmPrunable(plan farmPlan, plugin, skillName string) bool {
	if _, canon := plan.Canon[normSkillName(skillName)]; canon {
		return true
	}

	names, parked := plan.Parked[plugin]
	if !parked {
		return false
	}

	return !slices.Contains(names, skillName)
}

func (e *Engine) farmOwned(link string) bool {
	return strings.HasPrefix(link, filepath.Clean(e.vault.PluginsDir())+string(filepath.Separator))
}

func (e *Engine) farmLinkOwner(link string) (string, string, bool) {
	prefix := filepath.Clean(e.vault.PluginsDir()) + string(filepath.Separator)

	rest, ok := strings.CutPrefix(link, prefix)
	if !ok {
		return "", "", false
	}

	parts := strings.Split(rest, "/")
	if len(parts) != 5 || parts[2] != farmPivotName || parts[3] != farmSkillsDir {
		return "", "", false
	}

	return pluginKey(parts[0], parts[1]), parts[4], true
}

func summarizeFarmNames(names []string) string {
	sorted := slices.Compact(slices.Sorted(slices.Values(names)))

	if len(sorted) > farmNoteLimit {
		return strings.Join(sorted[:farmNoteLimit], ", ") + fmt.Sprintf(" +%d more", len(sorted)-farmNoteLimit)
	}

	return strings.Join(sorted, ", ")
}
