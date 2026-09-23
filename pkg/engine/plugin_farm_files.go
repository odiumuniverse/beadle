package engine

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

const (
	farmAgentsDirName   = "agents"
	farmCommandsDirName = "commands"
	// farmRenderDirName holds the rendered host-format copies of plugin
	// definitions: <vault>/plugins/farm/<marketplace>/<plugin>/<kind>/<ns>.<ext>.
	farmRenderDirName = "farm"
)

// farmFileSpec describes one plugin-sourced file kind the farm presents:
// agents or commands.
type farmFileSpec struct {
	Kind kind.ID
	// DirName is the plugin subdirectory that holds the definitions.
	DirName string
	// Label names one definition in warnings ("agent", "command").
	Label string
	// Artifacts is the artifact subdirectory under plugins/farm/...; it keeps
	// the rendered copies of different kinds apart.
	Artifacts string
	// Skip lists the hosts that read plugin definitions natively or take no
	// writes for this kind.
	Skip map[string]bool
	// ValidName reports whether a definition name is a canonical slug for the
	// kind: hosts that cannot express a name must not fail one by one.
	ValidName func(string) bool
	// Rename rewrites the definition identity to the namespaced name before
	// rendering; nil when the host format carries no name field.
	Rename func(ns string, markdown []byte) ([]byte, error)
}

// farmFileName namespaces a plugin-sourced definition: flat host directories
// keep one file per name, and the prefix keeps plugin content out of the canon
// names' way.
func farmFileName(plugin, name string) string { return plugin + "--" + name }

type farmFileLot struct {
	Key    string
	Plugin string
	Name   string
}

// farmFilePlan is what every host with a file surface of the kind may present
// from the parked plugins.
type farmFilePlan struct {
	Desired     map[string]farmFileLot
	Owner       map[string]string
	Parked      map[string]struct{}
	Quarantined map[string]pluginLedgerRec
	Canon       map[string]struct{}
}

func (e *Engine) buildFarmFilePlan(ledger pluginLedger, spec farmFileSpec) (farmFilePlan, []string) {
	plan := farmFilePlan{
		Desired:     map[string]farmFileLot{},
		Owner:       map[string]string{},
		Parked:      map[string]struct{}{},
		Quarantined: map[string]pluginLedgerRec{},
		Canon:       map[string]struct{}{},
	}

	var warns []string

	for _, key := range slices.Sorted(maps.Keys(ledger.Plugins)) {
		rec := ledger.Plugins[key]

		switch {
		case !rec.QuarantinedAt.IsZero():
			plan.Quarantined[key] = rec

			continue
		case !rec.RetiredAt.IsZero():
			continue
		}

		marketplace, plugin, ok := strings.Cut(key, "/")
		if !ok || !validPluginKey(marketplace, plugin) {
			continue
		}

		root, rootWarns, ok := e.pluginTargetRoot(key, rec)
		warns = append(warns, rootWarns...)

		if !ok {
			continue
		}

		names, scanWarns := e.scanPluginFiles(key, rec.Target, root, spec)
		warns = append(warns, scanWarns...)

		plan.Parked[key] = struct{}{}

		for _, name := range names {
			ns := farmFileName(plugin, name)

			if taken, ok := plan.Owner[ns]; ok {
				warns = append(warns, fmt.Sprintf("plugin farm: %s %s of %s is already provided by %s", spec.Label, ns, key, taken))

				continue
			}

			plan.Owner[ns] = key
			plan.Desired[ns] = farmFileLot{Key: key, Plugin: plugin, Name: name}
		}
	}

	canon, canonWarns := e.canonFileNames(spec)

	plan.Canon = canon

	warns = append(warns, canonWarns...)

	for _, ns := range slices.Sorted(maps.Keys(plan.Desired)) {
		if _, shadowed := canon[ns]; !shadowed {
			continue
		}

		warns = append(warns, fmt.Sprintf("plugin farm: %s %s is shadowed by the vault canon", spec.Label, ns))

		delete(plan.Desired, ns)
	}

	return plan, warns
}

// scanPluginFiles lists the top-level markdown definitions of one parked
// plugin. Nested definitions carry ids this flat presentation cannot express,
// so they are skipped with one warning.
func (e *Engine) scanPluginFiles(key, target, root string, spec farmFileSpec) ([]string, []string) {
	dir := filepath.Join(target, spec.DirName)

	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, []string{fmt.Sprintf("plugin %s %s cannot be read: %v", key, spec.DirName, err)}
	}

	var (
		names []string
		warns []string
	)

	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())

		if entry.IsDir() {
			warns = append(warns, fmt.Sprintf("plugin %s %s directory %s is not presented (nested ids are skipped)", key, spec.Label, entry.Name()))

			continue
		}

		base, ok := strings.CutSuffix(entry.Name(), ".md")
		if !ok {
			continue
		}

		if !spec.ValidName(base) {
			warns = append(warns, fmt.Sprintf("plugin %s %s %s is not a valid name (skipped)", key, spec.Label, entry.Name()))

			continue
		}

		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || !underDir(resolved, root) {
			warns = append(warns, fmt.Sprintf("plugin %s %s %s resolves outside the plugin cache", key, spec.Label, entry.Name()))

			continue
		}

		names = append(names, base)
	}

	return names, warns
}

// canonFileNames lists the names the vault canon owns for the kind: a plugin
// entry with the same final name never shadows the canon silently.
func (e *Engine) canonFileNames(spec farmFileSpec) (map[string]struct{}, []string) {
	names := map[string]struct{}{}

	items, _, err := e.loadVault(spec.Kind)
	if err != nil {
		return names, []string{"plugin farm: " + err.Error()}
	}

	for key := range items {
		names[strings.TrimSuffix(key, ".md")] = struct{}{}
	}

	return names, nil
}

// farmPluginFiles presents plugin-sourced definitions of one kind to every
// host whose surface takes files; the hosts in spec.Skip are left alone.
func (e *Engine) farmPluginFiles(active []*agent.Agent, spec farmFileSpec) ([]FarmResult, []string) {
	if e.home == "" || !e.config.KindEnabled(spec.Kind) {
		return nil, nil
	}

	ledger, warns, err := loadPluginLedger(e.vault.PluginsLedgerPath())
	if err != nil {
		return nil, append(warns, "plugin farm: "+err.Error())
	}

	plan, planWarns := e.buildFarmFilePlan(ledger, spec)
	warns = append(warns, planWarns...)

	var results []FarmResult

	for _, a := range active {
		if spec.Skip[a.ID] {
			continue
		}

		surface := a.Surface(spec.Kind)
		if surface == nil {
			continue
		}

		if e.config.ModeFor(a.ID, spec.Kind, surface.Traits().DefaultMode) == config.ModeOff {
			continue
		}

		target, ok := surface.(agent.FarmTarget)
		if !ok {
			continue
		}

		agentResults, agentWarns := e.farmFileSurface(a.ID, target, plan, spec)
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

	warns = append(warns, e.pruneFarmArtifacts(plan, spec)...)

	return results, warns
}

func (e *Engine) farmFileSurface(agentID string, target agent.FarmTarget, plan farmFilePlan, spec farmFileSpec) ([]FarmResult, []string) {
	dir := target.FarmDir()

	if len(plan.Desired) > 0 && !isDir(dir) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, []string{"plugin farm: " + err.Error()}
		}
	}

	renderer, renders := target.(agent.FarmRenderer)
	if renders && strings.HasSuffix(target.FarmName("probe"), ".md") {
		// Markdown hosts read the plugin file as-is: the namespaced symlink
		// carries the identity in its name, and the bytes stay untouched.
		renderer, renders = nil, false
	}

	var warns []string

	linked := map[string]int{}
	skipped := map[string][]string{}
	warned := map[string]bool{}
	files := map[string]string{}

	for _, ns := range slices.Sorted(maps.Keys(plan.Desired)) {
		lot := plan.Desired[ns]
		file := target.FarmName(ns)

		files[file] = ns

		source, sourceWarns := e.farmFileSourceOrNote(agentID, lot, warned, spec)

		warns = append(warns, sourceWarns...)

		if source == "" {
			continue
		}

		action, err := e.presentFarmFile(agentID, target, renderer, renders, dir, ns, lot, source, spec)

		switch {
		case errors.Is(err, fsutil.ErrSymlinksUnsupported):
			return []FarmResult{{Agent: agentID, Kind: spec.Kind, Action: FarmSkipped, Note: symlinkUnsupportedNote(dir)}}, warns
		case err != nil:
			warns = append(warns, "plugin farm: "+err.Error())
		case action == FarmLinked:
			linked[lot.Key]++
		case action == FarmSkipped:
			skipped[lot.Key] = append(skipped[lot.Key], ns)
		}
	}

	pruned, pruneWarns := e.pruneFarmFiles(agentID, dir, target, plan, files, spec)

	warns = append(warns, pruneWarns...)

	var results []FarmResult

	results = append(results, farmResults(agentID, spec.Kind, FarmLinked, linked)...)
	results = append(results, farmResults(agentID, spec.Kind, FarmPruned, pruned)...)

	for _, plugin := range slices.Sorted(maps.Keys(skipped)) {
		results = append(results, FarmResult{Agent: agentID, Kind: spec.Kind, Plugin: plugin, Action: FarmSkipped, Note: summarizeFarmNames(skipped[plugin])})
	}

	return results, warns
}

// presentFarmFile links or renders one plugin definition into the host surface.
func (e *Engine) presentFarmFile(
	agentID string, target agent.FarmTarget, renderer agent.FarmRenderer, renders bool,
	dir, ns string, lot farmFileLot, source string, spec farmFileSpec,
) (FarmAction, error) {
	if renders {
		return e.renderFarmFile(agentID, target, renderer, dir, ns, lot, source, spec)
	}

	return e.farmLink(filepath.Join(dir, target.FarmName(ns)), source)
}

// farmFileSourceOrNote resolves the plugin file for one lot; a missing file
// yields one pinned-pivot note per plugin.
func (e *Engine) farmFileSourceOrNote(agentID string, lot farmFileLot, warned map[string]bool, spec farmFileSpec) (string, []string) {
	source, ok := e.farmFileSource(agentID, lot, spec)
	if ok {
		return source, nil
	}

	if warned[lot.Key] {
		return "", nil
	}

	warned[lot.Key] = true

	return "", []string{"plugin farm: " + e.pinnedPivotNote(agentID, lot.Key)}
}

// farmFileSource resolves the plugin file a host presents: the host's own
// pivot (pin-aware), like the skills farm.
func (e *Engine) farmFileSource(agentID string, lot farmFileLot, spec farmFileSpec) (string, bool) {
	pivot, ok := e.pluginPivotFor(agentID, lot.Key)
	if !ok {
		return "", false
	}

	source := filepath.Join(pivot, spec.DirName, lot.Name+".md")
	if !isRegularFile(source) {
		return "", false
	}

	return source, true
}

// renderFarmFile renders the host-format copy of a plugin definition (Gemini
// commands TOML) into the vault and links it into the host directory: the file
// surface reads symlinks as read-only, so the kind sync never adopts or prunes
// a plugin presentation.
func (e *Engine) renderFarmFile(
	agentID string, target agent.FarmTarget, renderer agent.FarmRenderer,
	dir, ns string, lot farmFileLot, source string, spec farmFileSpec,
) (FarmAction, error) {
	data, err := os.ReadFile(source) //nolint:gosec // the source is a vault plugin pivot
	if err != nil {
		return FarmNoop, fmt.Errorf("read %s: %w", source, err)
	}

	if spec.Rename != nil {
		data, err = spec.Rename(ns, data)
		if err != nil {
			return FarmSkipped, err
		}
	}

	rendered, ok, err := renderer.FarmRender(ns, data)
	if err != nil {
		return FarmNoop, fmt.Errorf("render %s: %w", ns, err)
	}

	if !ok {
		return FarmSkipped, nil
	}

	marketplace, plugin, _ := strings.Cut(lot.Key, "/")

	// The rendered artifact lives outside the plugin pivot: the orphan-pivot
	// cleanup only retires a pivot that holds nothing but the symlink.
	lotDir := filepath.Join(e.vault.PluginsDir(), farmRenderDirName, marketplace, plugin, spec.Artifacts)
	if err := os.MkdirAll(lotDir, 0o700); err != nil {
		return FarmNoop, fmt.Errorf("create %s: %w", lotDir, err)
	}

	artifact := filepath.Join(lotDir, target.FarmName(ns))

	if existing, err := os.ReadFile(artifact); err != nil || string(existing) != string(rendered) { //nolint:gosec // the artifact lives in the vault
		if err := fsutil.WriteFileAtomic(artifact, rendered, 0o600); err != nil {
			return FarmNoop, fmt.Errorf("write %s: %w", artifact, err)
		}
	}

	return e.farmLink(filepath.Join(dir, target.FarmName(ns)), artifact)
}

// pruneFarmArtifacts drops the rendered host-format copies the current plan
// does not need: a gone plugin leaves nothing behind, and a renamed or
// removed definition does not accumulate stale files in the vault.
func (e *Engine) pruneFarmArtifacts(plan farmFilePlan, spec farmFileSpec) []string {
	root := filepath.Join(e.vault.PluginsDir(), farmRenderDirName)

	markets, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	var warns []string

	for _, market := range markets {
		if !market.IsDir() {
			continue
		}

		marketDir := filepath.Join(root, market.Name())

		warns = append(warns, e.pruneFarmMarket(marketDir, market.Name(), plan, spec)...)

		_ = os.Remove(marketDir)
	}

	return warns
}

// pruneFarmMarket cleans one marketplace's artifact directory.
func (e *Engine) pruneFarmMarket(dir, marketplace string, plan farmFilePlan, spec farmFileSpec) []string {
	plugins, err := os.ReadDir(dir)
	if err != nil {
		return []string{"plugin farm: " + err.Error()}
	}

	var warns []string

	for _, plugin := range plugins {
		if !plugin.IsDir() {
			continue
		}

		key := marketplace + "/" + plugin.Name()
		pluginDir := filepath.Join(dir, plugin.Name())
		kindDir := filepath.Join(pluginDir, spec.Artifacts)

		if _, parked := plan.Parked[key]; !parked {
			warns = append(warns, removeFarmDir(kindDir)...)
			_ = os.Remove(pluginDir)

			continue
		}

		warns = append(warns, e.pruneFarmKindArtifacts(kindDir, key, plan)...)

		_ = os.Remove(kindDir)
		_ = os.Remove(pluginDir)
	}

	return warns
}

// pruneFarmKindArtifacts drops the files of one plugin and kind that no longer
// name a presented definition.
func (e *Engine) pruneFarmKindArtifacts(dir, key string, plan farmFilePlan) []string {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var warns []string

	for _, file := range files {
		ns, _, ok := strings.Cut(file.Name(), ".")
		if !ok {
			continue
		}

		if lot, desired := plan.Desired[ns]; desired && lot.Key == key {
			continue
		}

		if err := os.Remove(filepath.Join(dir, file.Name())); err != nil {
			warns = append(warns, fmt.Sprintf("plugin farm: remove %s: %v", file.Name(), err))
		}
	}

	return warns
}

// removeFarmDir deletes a beadle-owned artifact directory file by file; the
// directory goes only once it is empty.
func removeFarmDir(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var warns []string

	for _, entry := range entries {
		if entry.IsDir() {
			warns = append(warns, removeFarmDir(filepath.Join(dir, entry.Name()))...)

			continue
		}

		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			warns = append(warns, fmt.Sprintf("plugin farm: remove %s: %v", entry.Name(), err))
		}
	}

	_ = os.Remove(dir)

	return warns
}

// pruneFarmFiles removes the presented copies that are no longer desired: the
// plugin is gone, quarantined, retired, or another owner took the name. Every
// presentation is a symlink, identified by its raw target.
func (e *Engine) pruneFarmFiles(
	agentID, dir string, target agent.FarmTarget, plan farmFilePlan, files map[string]string, spec farmFileSpec,
) (map[string]int, []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}

		return nil, []string{"plugin farm: " + err.Error()}
	}

	var warns []string

	pruned := map[string]int{}
	ext := filepath.Ext(target.FarmName("probe"))

	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(dir, name)

		plugin, ok := e.farmFileOwner(path, spec)
		if !ok {
			continue
		}

		ns, ours := files[name]
		if !ours {
			ns = strings.TrimSuffix(name, ext)
		}

		// The canon always wins, and a presentation that cannot resolve (a
		// pinned pivot that is gone) goes: the surface would report a broken
		// symlink on every sync. Everything else that is no longer desired
		// goes too.
		switch {
		case canonShadows(plan.Canon, ns):
		case ours && plan.Desired[ns].Key == plugin && e.presentationResolvable(agentID, plugin, path):
			continue
		}

		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			warns = append(warns, fmt.Sprintf("plugin farm: remove %s: %v", path, err))

			continue
		}

		if _, quarantined := plan.Quarantined[plugin]; quarantined {
			warns = append(warns, fmt.Sprintf("plugin farm: plugin %s is quarantined; its %s %s is no longer presented", plugin, spec.Label, name))
		}

		pruned[plugin]++
	}

	return pruned, warns
}

// presentationResolvable reports whether a presented copy still points at a
// live file: an unresolvable pin or a dead raw target leaves a dangling link.
func (e *Engine) presentationResolvable(agentID, plugin, path string) bool {
	if _, pinned := e.config.PluginPin(agentID, plugin); pinned && !e.pivotReady(agentID, plugin) {
		return false
	}

	return fsutil.Exists(path)
}

func (e *Engine) pivotReady(agentID, key string) bool {
	_, ok := e.pluginPivotFor(agentID, key)

	return ok
}

// farmFileOwner names the plugin a presented copy belongs to, or ok=false
// when the entry is not beadle's. Every presentation is a symlink into the
// vault plugin area, so the raw target identifies the owner.
func (e *Engine) farmFileOwner(path string, spec farmFileSpec) (string, bool) {
	link, err := os.Readlink(path)
	if err != nil || !e.farmOwned(link) {
		return "", false
	}

	key, _, ok := e.farmFileLinkOwner(link, spec)

	return key, ok
}

// farmFileLinkOwner parses a farmed link target; two shapes exist:
// <vault>/plugins/<marketplace>/<plugin>/<pivot>/<kind>/<name>.md (markdown
// hosts) and <vault>/plugins/farm/<marketplace>/<plugin>/<kind>/<name>.<ext>
// (rendered hosts).
func (e *Engine) farmFileLinkOwner(link string, spec farmFileSpec) (string, string, bool) {
	root := filepath.Clean(e.vault.PluginsDir())

	rel, ok := strings.CutPrefix(filepath.Clean(link), root+string(filepath.Separator))
	if !ok {
		return "", "", false
	}

	parts := strings.Split(rel, string(filepath.Separator))

	if len(parts) == 5 && parts[3] == spec.DirName && pivotEntryName(parts[2]) {
		if name, ok := strings.CutSuffix(parts[4], ".md"); ok {
			return parts[0] + "/" + parts[1], name, true
		}
	}

	// The rendered shape is tried when the markdown suffix does not match: a
	// marketplace named like the artifact root can make a rendered link look
	// like the markdown shape for the first five path parts.
	if len(parts) == 5 && parts[0] == farmRenderDirName && parts[3] == spec.Artifacts {
		name, _, ok := strings.Cut(parts[4], ".")
		if !ok {
			return "", "", false
		}

		return parts[1] + "/" + parts[2], name, true
	}

	return "", "", false
}

func canonShadows(canon map[string]struct{}, name string) bool {
	_, shadowed := canon[name]

	return shadowed
}

func isRegularFile(path string) bool {
	info, err := os.Lstat(path)

	return err == nil && info.Mode().IsRegular()
}
