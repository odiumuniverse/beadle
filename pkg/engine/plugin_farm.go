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
	farmPivotName  = "current"
	pinPivotPrefix = "at-"
	farmSkillsDir  = "skills"
	farmSkillFile  = "SKILL.md"
	farmNoteLimit  = 5
)

var replaceSymlink = fsutil.ReplaceSymlink

func pinPivotName(version string) string {
	return pinPivotPrefix + version
}

func pivotEntryName(name string) bool {
	return name == farmPivotName || strings.HasPrefix(name, pinPivotPrefix)
}

func pinnedMissingNote(key, version string) string {
	return fmt.Sprintf("pinned version %s of %s is not in the plugin cache; keeping the pin (no silent upgrade)", version, key)
}

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
	Kind   kind.ID    `json:"kind"`
	Plugin string     `json:"plugin"`
	Action FarmAction `json:"action"`
	Count  int        `json:"count,omitempty"`
	Note   string     `json:"note,omitempty"`
}

type farmPlan struct {
	Parked      map[string][]string
	Quarantined map[string]pluginLedgerRec
	Owner       map[string]string
	Desired     map[string]struct{}
	Canon       map[string]struct{}
	// LoserHosts maps a plugin key to the source hosts whose own copy lost the
	// dedup or the same-key source conflict: those hosts read their native
	// copy and must not receive the winner's presentation.
	LoserHosts map[string]map[string]bool
	// SkillDirs maps a plugin key to the plugin-relative directory its skills
	// live in (a manifest can move them).
	SkillDirs map[string]string
}

// loserHost reports whether one host's own copy of a plugin lost the dedup or
// the same-key source conflict: the host reads its native copy, so the
// winner's presentation must not reach it too.
func (p farmPlan) loserHost(agentID, key string) bool {
	return p.LoserHosts[key][agentID]
}

func (e *Engine) syncPluginSurfaces(ctx context.Context, report *Report, active []*agent.Agent, opts SyncOptions) {
	results, warnings, notes, orphansSafe, err := e.reconcilePlugins(ctx)

	report.Warnings = append(report.Warnings, warnings...)
	report.Notes = append(report.Notes, notes...)

	if err != nil {
		report.Warnings = append(report.Warnings, "plugins: "+err.Error())
	}

	report.Plugins = results

	if opts.Direction == config.ModePull {
		return
	}

	if ledger, _, err := loadPluginLedger(e.vault.PluginsLedgerPath()); err == nil {
		dedup := e.pluginDedup(ledger)

		report.Notes = append(report.Notes, dedup.Notes...)
		report.Warnings = append(report.Warnings, dedup.Warns...)
	}

	var (
		farm         []FarmResult
		farmWarnings []string
	)

	if selected(opts.Kinds, kind.Skills) {
		skills, skillsWarnings := e.farmPluginSkills(active)

		farm = append(farm, skills...)
		farmWarnings = append(farmWarnings, skillsWarnings...)
	}

	if selected(opts.Kinds, kind.Subagents) {
		agents, agentWarnings := e.farmPluginAgents(active)

		farm = append(farm, agents...)
		farmWarnings = append(farmWarnings, agentWarnings...)
	}

	if selected(opts.Kinds, kind.Commands) {
		commands, commandWarnings := e.farmPluginCommands(active)

		farm = append(farm, commands...)
		farmWarnings = append(farmWarnings, commandWarnings...)
	}

	pruned, pruneWarnings := e.pruneLegacyOrphanLinks(orphansSafe)

	farm = append(farm, pruned...)
	farmWarnings = append(farmWarnings, pruneWarnings...)

	slices.SortFunc(farm, func(a, b FarmResult) int {
		return cmp.Or(
			cmp.Compare(a.Agent, b.Agent),
			cmp.Compare(a.Plugin, b.Plugin),
			cmp.Compare(a.Action, b.Action),
		)
	})

	report.Farm = farm
	report.Warnings = append(report.Warnings, farmWarnings...)
}

// pruneLegacyOrphanLinks runs the orphan farm-link cleanup on every sync
// that writes agents, regardless of the skill modes.
func (e *Engine) pruneLegacyOrphanLinks(orphansSafe bool) ([]FarmResult, []string) {
	if e.home == "" || !orphansSafe {
		return nil, nil
	}

	ledger, _, err := loadPluginLedger(e.vault.PluginsLedgerPath())
	if err != nil {
		return nil, nil
	}

	return e.pruneOrphanFarmLinks(ledger, false)
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
		Desired:     map[string]struct{}{},
		Canon:       map[string]struct{}{},
		SkillDirs:   map[string]string{},
	}

	warns := e.scanLedgerPlan(&plan, ledger)

	for _, key := range slices.Sorted(maps.Keys(plan.Parked)) {
		for _, skillName := range plan.Parked[key] {
			if owner, taken := plan.Owner[skillName]; taken {
				warns = append(warns, fmt.Sprintf("plugin farm: skill %s of %s is already provided by %s", skillName, key, owner))

				continue
			}

			plan.Owner[skillName] = key
			plan.Desired[skillName] = struct{}{}
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

func (e *Engine) scanLedgerPlan(plan *farmPlan, ledger pluginLedger) []string {
	var warns []string

	dedup := e.pluginDedup(ledger)

	plan.LoserHosts = dedup.LoserHosts

	for _, key := range slices.Sorted(maps.Keys(ledger.Plugins)) {
		rec := ledger.Plugins[key]

		// A retired record outranks a leftover quarantine flag: the plugin is
		// gone from the registry, so nothing is presented any more.
		if !rec.RetiredAt.IsZero() {
			continue
		}

		if !rec.QuarantinedAt.IsZero() {
			marketplace, name, ok := strings.Cut(key, "/")
			if ok && validPluginKey(marketplace, name) {
				plan.Quarantined[key] = rec
			}

			continue
		}

		if _, covered := dedup.Suppressed[key]; covered {
			// The same plugin is already presented from another host.
			continue
		}

		names, dir, ok, pluginWarns := e.parkedPluginSkills(key, rec)

		for _, warn := range pluginWarns {
			warns = append(warns, "plugin farm: "+warn)
		}

		if !ok {
			continue
		}

		plan.Parked[key] = names
		plan.SkillDirs[key] = dir
	}

	return warns
}

func (e *Engine) parkedPluginSkills(key string, rec pluginLedgerRec) ([]string, string, bool, []string) {
	marketplace, name, ok := strings.Cut(key, "/")
	if !ok || !validPluginKey(marketplace, name) {
		return nil, "", false, nil
	}

	root, warns, ok := e.pluginTargetRoot(key, rec)
	if !ok {
		return nil, "", false, warns
	}

	dir := farmKindDir(recSource(rec), rec.Target, kind.Skills)

	names, ok, scanWarns := e.scanPluginSkills(key, rec.Target, dir, root)

	return names, dir, ok, append(warns, scanWarns...)
}

func (e *Engine) pluginTargetRoot(key string, rec pluginLedgerRec) (string, []string, bool) {
	if !isDir(rec.Target) {
		return "", nil, false
	}

	resolved, err := filepath.EvalSymlinks(rec.Target)
	if err != nil {
		return "", []string{fmt.Sprintf("plugin %s install path resolves outside the plugin cache: %s", key, rec.Target)}, false
	}

	for _, candidate := range e.pluginSourceRoots(recSource(rec)) {
		root, err := filepath.EvalSymlinks(candidate)
		if err != nil || !underDir(resolved, root) {
			continue
		}

		return root, nil, true
	}

	return "", []string{fmt.Sprintf("plugin %s install path is outside the plugin cache: %s", key, rec.Target)}, false
}

func (e *Engine) scanPluginSkills(key, target, dir, root string) ([]string, bool, []string) {
	entries, err := os.ReadDir(filepath.Join(target, dir))
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
		path := filepath.Join(target, dir, entry.Name())
		if !isDir(path) || !skill.HasRoot(path) {
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
		if !entry.IsDir() {
			continue
		}

		if !skill.HasRoot(filepath.Join(e.vault.SkillsDir(), entry.Name())) {
			continue
		}

		names[normSkillName(entry.Name())] = struct{}{}
	}

	return names, nil
}

func normSkillName(name string) string {
	return norm.NFC.String(strings.ToLower(name))
}

// pluginPivotFor resolves the pivot directory an agent reads a plugin from:
// at-<version> when the plugin is pinned, current otherwise. A pinned pivot
// that is missing yields ok=false — there is no fallback to current.
func (e *Engine) pluginPivotFor(agentID, key string) (string, bool) {
	marketplace, name, ok := strings.Cut(key, "/")
	if !ok || !validPluginKey(marketplace, name) {
		return "", false
	}

	base := filepath.Join(e.vault.PluginsDir(), marketplace, name)

	version, pinned := e.config.PluginPin(agentID, key)
	if !pinned {
		return filepath.Join(base, farmPivotName), true
	}

	pivot := filepath.Join(base, pinPivotName(version))
	if !pivotValid(pivot) {
		return "", false
	}

	return pivot, true
}

func pivotValid(path string) bool {
	info, err := os.Lstat(path)

	return err == nil && info.Mode()&fs.ModeSymlink != 0 && isDir(path)
}

func (e *Engine) agentSkillTarget(agentID, key, skillName string) (string, bool) {
	return e.agentSkillTargetIn(agentID, key, farmSkillsDir, skillName)
}

// agentSkillTargetIn resolves one skill of a plugin in the payload directory
// the plugin manifest declares (defaults to skills/).
func (e *Engine) agentSkillTargetIn(agentID, key, dir, skillName string) (string, bool) {
	if dir == "" {
		dir = farmSkillsDir
	}

	pivot, ok := e.pluginPivotFor(agentID, key)
	if !ok || !filepath.IsLocal(skillName) || !filepath.IsLocal(dir) {
		return "", false
	}

	return filepath.Join(pivot, dir, skillName), true
}

func (e *Engine) notePinnedMiss(warns []string, warned map[string]bool, agentID, key string) []string {
	if warned[key] {
		return warns
	}

	warned[key] = true

	return append(warns, "plugin farm: "+e.pinnedPivotNote(agentID, key))
}

func (e *Engine) pinnedPivotNote(agentID, key string) string {
	version, _ := e.config.PluginPin(agentID, key)

	marketplace, name, ok := strings.Cut(key, "/")
	if ok {
		pivot := filepath.Join(e.vault.PluginsDir(), marketplace, name, pinPivotName(version))

		if _, err := os.Lstat(pivot); err == nil {
			return fmt.Sprintf("plugin %s pivot %s is not a usable symlink for %s; run beadle sync", key, pinPivotName(version), agentID)
		}
	}

	return fmt.Sprintf("pinned version %s of %s is not in the plugin cache for %s; keeping the pin (no silent upgrade)", version, key, agentID)
}

func (e *Engine) notePinnedSkillMiss(warns []string, warned map[string]bool, agentID, key, skillName string) []string {
	if warned[key] {
		return warns
	}

	warned[key] = true

	version, _ := e.config.PluginPin(agentID, key)

	return append(warns, fmt.Sprintf("plugin farm: skill %s is missing in %s@%s; skipped", skillName, key, version))
}

func (e *Engine) farmAgentSkills(agentID, dir string, plan farmPlan) ([]FarmResult, []string) {
	var warns []string

	if len(plan.Desired) > 0 && !isDir(dir) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, []string{"plugin farm: " + err.Error()}
		}
	}

	stubbed, stubPruned, stubWarns := e.farmStubs(agentID, dir, plan)

	warns = append(warns, stubWarns...)

	linked := map[string]int{}
	skips := map[string][]string{}
	warned := map[string]bool{}

	for _, skillName := range slices.Sorted(maps.Keys(plan.Desired)) {
		target, ok, targetWarns := e.desiredSkillTarget(agentID, plan, skillName, warned)

		warns = append(warns, targetWarns...)

		if !ok {
			continue
		}

		plugin := plan.Owner[skillName]

		action, err := e.farmLink(filepath.Join(dir, skillName), target)
		switch {
		case errors.Is(err, fsutil.ErrSymlinksUnsupported):
			return []FarmResult{{Agent: agentID, Kind: kind.Skills, Action: FarmSkipped, Note: symlinkUnsupportedNote(dir)}}, warns
		case err != nil:
			warns = append(warns, "plugin farm: "+err.Error())
		case action == FarmLinked:
			linked[plugin]++
		case action == FarmSkipped:
			skips[plugin] = append(skips[plugin], skillName)
		}
	}

	pruned, pruneWarns := e.pruneFarmLinks(agentID, dir, plan)

	warns = append(warns, pruneWarns...)

	pruned = mergeFarmPruned(pruned, stubPruned)

	var results []FarmResult

	results = append(results, farmResults(agentID, kind.Skills, FarmStubbed, stubbed)...)
	results = append(results, farmResults(agentID, kind.Skills, FarmLinked, linked)...)
	results = append(results, farmResults(agentID, kind.Skills, FarmPruned, pruned)...)

	for _, plugin := range slices.Sorted(maps.Keys(skips)) {
		results = append(results, FarmResult{Agent: agentID, Kind: kind.Skills, Plugin: plugin, Action: FarmSkipped, Note: summarizeFarmNames(skips[plugin])})
	}

	return results, warns
}

// desiredSkillTarget resolves the farm target of one desired skill for one
// host. A host whose own copy lost the dedup presents nothing; a pinned plugin
// whose pivot for this host is gone reports its own miss.
func (e *Engine) desiredSkillTarget(agentID string, plan farmPlan, skillName string, warned map[string]bool) (string, bool, []string) {
	plugin := plan.Owner[skillName]

	if plan.loserHost(agentID, plugin) {
		return "", false, nil
	}

	target, ok := e.agentSkillTargetIn(agentID, plugin, plan.SkillDirs[plugin], skillName)
	if !ok {
		return "", false, e.notePinnedMiss(nil, warned, agentID, plugin)
	}

	if _, pinned := e.config.PluginPin(agentID, plugin); pinned && !isDir(target) {
		return "", false, e.notePinnedSkillMiss(nil, warned, agentID, plugin, skillName)
	}

	return target, true, nil
}

func mergeFarmPruned(pruned, stubPruned map[string]int) map[string]int {
	if len(stubPruned) == 0 {
		return pruned
	}

	if pruned == nil {
		pruned = map[string]int{}
	}

	for key, count := range stubPruned {
		pruned[key] += count
	}

	return pruned
}

func symlinkUnsupportedNote(dir string) string {
	return fmt.Sprintf("symlinks are not supported in %s; a copy fallback is intentionally not performed", dir)
}

func farmResults(agentID string, k kind.ID, action FarmAction, counts map[string]int) []FarmResult {
	results := make([]FarmResult, 0, len(counts))

	for _, plugin := range slices.Sorted(maps.Keys(counts)) {
		results = append(results, FarmResult{Agent: agentID, Kind: k, Plugin: plugin, Action: action, Count: counts[plugin]})
	}

	return results
}

func (e *Engine) farmStubs(agentID, dir string, plan farmPlan) (map[string]int, map[string]int, []string) {
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
			key, stubWarns := e.stubQuarantinedLink(agentID, path, name, plan)

			warns = append(warns, stubWarns...)

			if key != "" {
				stubbed[key]++
			}
		case entry.IsDir():
			key, ok := skill.IsStubDir(path)
			if !ok || stubStillNeeded(plan, agentID, key, name) {
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

func (e *Engine) stubQuarantinedLink(agentID, path, name string, plan farmPlan) (string, []string) {
	link, err := os.Readlink(path)
	if err != nil || fsutil.Exists(link) {
		return "", nil
	}

	key, skillName, ok := e.farmLinkOwner(link)
	if !ok || skillName != name {
		return "", nil
	}

	// A host whose own copy lost the dedup keeps reading its native plugin:
	// the winner's quarantined link is pruned, not stubbed.
	if plan.loserHost(agentID, key) {
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

func stubStillNeeded(plan farmPlan, agentID, key, name string) bool {
	if plan.loserHost(agentID, key) {
		return false
	}

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

// farmLink presents one plugin item: it replaces beadle's own stale link,
// keeps an up-to-date one, and leaves foreign entries untouched.
func (e *Engine) farmLink(path, target string) (FarmAction, error) {
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

func (e *Engine) pruneFarmLinks(agentID, dir string, plan farmPlan) (map[string]int, []string) {
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

		if !e.farmPrunable(agentID, plan, plugin, name) && !e.pinDangling(agentID, plugin, link, name) {
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

func (e *Engine) farmPrunable(agentID string, plan farmPlan, plugin, skillName string) bool {
	if plan.loserHost(agentID, plugin) {
		return true
	}

	if _, ok := e.pluginPivotFor(agentID, plugin); !ok {
		return true
	}

	if _, canon := plan.Canon[normSkillName(skillName)]; canon {
		return true
	}

	names, parked := plan.Parked[plugin]
	if !parked {
		return false
	}

	return !slices.Contains(names, skillName)
}

func (e *Engine) pinDangling(agentID, plugin, link, skillName string) bool {
	if _, pinned := e.config.PluginPin(agentID, plugin); !pinned {
		return false
	}

	target, ok := e.agentSkillTarget(agentID, plugin, skillName)
	if !ok || !isDir(target) {
		return true
	}

	return !fsutil.Exists(link)
}

func (e *Engine) farmOwned(link string) bool {
	return strings.HasPrefix(link, filepath.Clean(e.vault.PluginsDir())+string(filepath.Separator))
}

// farmLinkOwner parses a farmed skill link target:
// <vault>/plugins/<origin>/<plugin>/<pivot>/<dir>/<name>. The payload
// directory is plugin-defined (a manifest can move it), so any directory
// under the pivot counts as beadle's own.
func (e *Engine) farmLinkOwner(link string) (string, string, bool) {
	prefix := filepath.Clean(e.vault.PluginsDir()) + string(filepath.Separator)

	rest, ok := strings.CutPrefix(link, prefix)
	if !ok {
		return "", "", false
	}

	parts := strings.Split(rest, "/")
	if len(parts) != 5 || !pivotEntryName(parts[2]) {
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
