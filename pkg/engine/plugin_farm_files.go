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
	"github.com/odiumuniverse/beadle/pkg/plugin"
)

const (
	farmAgentsDirName   = "agents"
	farmCommandsDirName = "commands"
	// farmRenderDirName holds the rendered host-format copies of plugin
	// definitions: <vault>/plugins/farm/<marketplace>/<plugin>/<kind>/<ns>.<ext>.
	farmRenderDirName = "farm"
	// markdownExt is the default definition format; sources whose
	// definitions are not markdown declare their own extensions.
	markdownExt = ".md"
)

// farmFileSpec describes one plugin-sourced file kind the farm presents:
// agents or commands.
type farmFileSpec struct {
	Kind kind.ID
	// DirName is the well-known plugin subdirectory that holds the
	// definitions; a manifest may declare another one, resolved per plugin.
	DirName string
	// Label names one definition in warnings ("agent", "command").
	Label string
	// Artifacts is the artifact subdirectory under plugins/farm/...; it keeps
	// the rendered copies of different kinds apart.
	Artifacts string
	// Skip lists, per host, the plugin sources whose definitions must not be
	// presented there: a host reads its own plugin definitions natively, while
	// another host's plugin has no native path. An empty source list skips
	// every source for that host.
	Skip map[string][]string
	// ValidName reports whether a definition name is a canonical slug for the
	// kind: hosts that cannot express a name must not fail one by one.
	ValidName func(string) bool
	// Rename rewrites the definition identity to the namespaced name before
	// rendering; nil when the host format carries no name field.
	Rename func(ns string, markdown []byte) ([]byte, error)
	// SourceExts maps a plugin source to the file extensions its definitions
	// use, in priority order. An empty map means markdown only.
	SourceExts map[string][]string
	// Lift converts a non-markdown definition (Gemini CLI TOML commands) into
	// canonical markdown; nil means every definition is already markdown.
	Lift func(name string, data []byte) ([]byte, bool, error)
}

// skipHost reports whether one host/source pair is excluded: a host reads its
// own plugin definitions natively, and an empty source list excludes every
// source (a surface that takes no writes for this kind).
func (spec farmFileSpec) skipHost(agentID, source string) bool {
	sources, listed := spec.Skip[agentID]
	if !listed {
		return false
	}

	return len(sources) == 0 || slices.Contains(sources, source)
}

// extsFor returns the definition extensions of one plugin source.
func (spec farmFileSpec) extsFor(source string) []string {
	if exts, ok := spec.SourceExts[source]; ok && len(exts) > 0 {
		return exts
	}

	return []string{markdownExt}
}

// sourceExts returns every definition extension of the spec, longest first so
// a name never loses its full extension.
func (spec farmFileSpec) sourceExts() []string {
	exts := []string{markdownExt}

	for _, source := range plugin.SourceHosts() {
		exts = append(exts, spec.extsFor(source)...)
	}

	slices.SortFunc(exts, func(a, b string) int { return len(b) - len(a) })

	return slices.Compact(exts)
}

// definitionName strips one definition extension from a file name.
func (spec farmFileSpec) definitionName(file string) (string, bool) {
	for _, ext := range spec.sourceExts() {
		if name, ok := strings.CutSuffix(file, ext); ok && name != "" {
			return name, true
		}
	}

	return "", false
}

type farmFileLot struct {
	Key    string
	Plugin string
	Name   string
	// Source is the plugin host the definition came from; the presentation
	// skips its own host, which reads the definition natively.
	Source string
	// Dir is the plugin-relative payload directory of this definition.
	Dir string
	// File is the definition file name inside Dir.
	File string
}

// farmFileName namespaces a plugin-sourced definition: flat host directories
// keep one file per name, and the prefix keeps plugin content out of the canon
// names' way.
func farmFileName(plugin, name string) string { return plugin + "--" + name }

// farmKindDir resolves the payload directory of one file kind for a plugin:
// a manifest can move the definitions, so the well-known name is only the
// fallback.
func farmKindDir(source, installPath string, k kind.ID) string {
	skills, agents, commands := plugin.ArtifactDirs(source, installPath)

	switch k {
	case kind.Subagents:
		return agents
	case kind.Commands:
		return commands
	default:
		return skills
	}
}

// farmFilePlan is what every host with a file surface of the kind may present
// from the parked plugins.
type farmFilePlan struct {
	Desired     map[string]farmFileLot
	Owner       map[string]string
	Parked      map[string]struct{}
	Quarantined map[string]pluginLedgerRec
	Canon       map[string]struct{}
	// LoserHosts maps a plugin key to the source hosts whose own copy lost the
	// dedup or the same-key source conflict: those hosts read their native
	// copy and must not receive the winner's presentation.
	LoserHosts map[string]map[string]bool
}

// loserHost reports whether one host's own copy of a plugin lost the dedup or
// the same-key source conflict: the host reads its native copy, so the
// winner's presentation must not reach it too.
func (p farmFilePlan) loserHost(agentID, key string) bool {
	return p.LoserHosts[key][agentID]
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

	dedup := e.pluginDedup(ledger)

	plan.LoserHosts = dedup.LoserHosts

	for _, key := range slices.Sorted(maps.Keys(ledger.Plugins)) {
		rec := ledger.Plugins[key]

		switch {
		case !rec.RetiredAt.IsZero():
			// A retired record outranks a leftover quarantine flag: the
			// plugin is gone from the registry, so nothing is presented and
			// no stub is needed.
			continue
		case !rec.QuarantinedAt.IsZero():
			plan.Quarantined[key] = rec

			continue
		}

		if _, covered := dedup.Suppressed[key]; covered {
			// The same plugin is already presented from another host.
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

		source := recSource(rec)
		dirName := farmKindDir(source, rec.Target, spec.Kind)

		entries, scanWarns := e.scanPluginFiles(key, rec.Target, dirName, root, spec.extsFor(source), spec)
		warns = append(warns, scanWarns...)

		plan.Parked[key] = struct{}{}

		for _, entry := range entries {
			ns := farmFileName(plugin, entry.Name)

			if taken, ok := plan.Owner[ns]; ok {
				warns = append(warns, fmt.Sprintf("plugin farm: %s %s of %s is already provided by %s", spec.Label, ns, key, taken))

				continue
			}

			plan.Owner[ns] = key
			plan.Desired[ns] = farmFileLot{Key: key, Plugin: plugin, Name: entry.Name, Source: source, Dir: dirName, File: entry.File}
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

// farmFileEntry is one definition found in a plugin payload directory.
type farmFileEntry struct {
	Name string
	File string
}

// scanPluginFiles lists the top-level definitions of one parked plugin in the
// host's own file format. Nested definitions carry ids this flat presentation
// cannot express, so they are skipped with one warning.
func (e *Engine) scanPluginFiles(key, target, dirName, root string, exts []string, spec farmFileSpec) ([]farmFileEntry, []string) {
	dir := filepath.Join(target, dirName)

	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, []string{fmt.Sprintf("plugin %s %s cannot be read: %v", key, dirName, err)}
	}

	var (
		files []farmFileEntry
		warns []string
	)

	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())

		if entry.IsDir() {
			warns = append(warns, fmt.Sprintf("plugin %s %s directory %s is not presented (nested ids are skipped)", key, spec.Label, entry.Name()))

			continue
		}

		name, ok := definitionNameFor(entry.Name(), exts)
		if !ok {
			continue
		}

		if !spec.ValidName(name) {
			warns = append(warns, fmt.Sprintf("plugin %s %s %s is not a valid name (skipped)", key, spec.Label, entry.Name()))

			continue
		}

		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || !underDir(resolved, root) {
			warns = append(warns, fmt.Sprintf("plugin %s %s %s resolves outside the plugin cache", key, spec.Label, entry.Name()))

			continue
		}

		files = append(files, farmFileEntry{Name: name, File: entry.Name()})
	}

	return files, warns
}

// definitionNameFor strips the first matching extension of a source.
func definitionNameFor(file string, exts []string) (string, bool) {
	for _, ext := range exts {
		if name, ok := strings.CutSuffix(file, ext); ok && name != "" {
			return name, true
		}
	}

	return "", false
}

// markdownSource reports whether a payload file is already markdown, so the
// lift never touches a markdown definition.
func markdownSource(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case markdownExt, ".mdc", ".markdown", ".txt":
		return true
	default:
		return false
	}
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
		names[strings.TrimSuffix(key, markdownExt)] = struct{}{}
	}

	return names, nil
}

// farmPluginFiles presents plugin-sourced definitions of one kind to every
// host whose surface takes files; the host/source pairs in spec.Skip are left
// alone (a host reads its own plugin definitions natively).
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

// desiredFarmLots lists the definitions one host presents: the plan's lots
// minus the host/source pairs the spec skips and minus the plugins whose own
// copy of that host lost the dedup (it reads its native copy).
func desiredFarmLots(agentID string, plan farmFilePlan, spec farmFileSpec) []string {
	var desired []string

	for _, ns := range slices.Sorted(maps.Keys(plan.Desired)) {
		lot := plan.Desired[ns]
		if spec.skipHost(agentID, lot.Source) || plan.loserHost(agentID, lot.Key) {
			continue
		}

		desired = append(desired, ns)
	}

	return desired
}

func (e *Engine) farmFileSurface(agentID string, target agent.FarmTarget, plan farmFilePlan, spec farmFileSpec) ([]FarmResult, []string) {
	dir := target.FarmDir()
	desired := desiredFarmLots(agentID, plan, spec)

	if len(desired) > 0 && !isDir(dir) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, []string{"plugin farm: " + err.Error()}
		}
	}

	renderer, renders := target.(agent.FarmRenderer)
	if renders && strings.HasSuffix(target.FarmName("probe"), markdownExt) {
		// Markdown hosts read the plugin file as-is: the namespaced symlink
		// carries the identity in its name, and the bytes stay untouched.
		renderer, renders = nil, false
	}

	var warns []string

	linked := map[string]int{}
	skipped := map[string][]string{}
	warned := map[string]bool{}
	files := map[string]string{}

	for _, ns := range desired {
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

	if spec.Lift != nil && !markdownSource(source) {
		return e.liftFarmFile(target, dir, ns, lot, source, spec)
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

	source := filepath.Join(pivot, lot.Dir, lot.File)
	if !isRegularFile(source) {
		return "", false
	}

	return source, true
}

// farmFileMarkdown returns the canonical markdown of one definition: the raw
// plugin bytes for a markdown source, the lifted host format otherwise.
func (e *Engine) farmFileMarkdown(lot farmFileLot, source string, spec farmFileSpec) ([]byte, bool, error) {
	data, err := os.ReadFile(source) //nolint:gosec // the source is a vault plugin pivot
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", source, err)
	}

	if spec.Lift == nil || markdownSource(source) {
		return data, true, nil
	}

	lifted, ok, err := spec.Lift(lot.Name, data)
	if err != nil {
		return nil, false, fmt.Errorf("lift %s: %w", lot.Name, err)
	}

	return lifted, ok, nil
}

// liftFarmFile converts a foreign-format plugin definition (Gemini CLI TOML
// commands) into canonical markdown, writes it into the vault render area and
// links it into a markdown host directory.
func (e *Engine) liftFarmFile(target agent.FarmTarget, dir, ns string, lot farmFileLot, source string, spec farmFileSpec) (FarmAction, error) {
	markdown, ok, err := e.farmFileMarkdown(lot, source, spec)
	if err != nil {
		return FarmNoop, err
	}

	if !ok {
		return FarmSkipped, nil
	}

	artifact, err := e.writeFarmArtifact(lot, target.FarmName(ns), markdown, spec)
	if err != nil {
		return FarmNoop, err
	}

	return e.farmLink(filepath.Join(dir, target.FarmName(ns)), artifact)
}

// renderFarmFile renders the host-format copy of a plugin definition (Gemini
// commands TOML) into the vault and links it into the host directory: the file
// surface reads symlinks as read-only, so the kind sync never adopts or prunes
// a plugin presentation.
func (e *Engine) renderFarmFile(
	agentID string, target agent.FarmTarget, renderer agent.FarmRenderer,
	dir, ns string, lot farmFileLot, source string, spec farmFileSpec,
) (FarmAction, error) {
	markdown, ok, err := e.farmFileMarkdown(lot, source, spec)
	if err != nil {
		return FarmNoop, err
	}

	if !ok {
		return FarmSkipped, nil
	}

	if spec.Rename != nil {
		markdown, err = spec.Rename(ns, markdown)
		if err != nil {
			return FarmSkipped, err
		}
	}

	rendered, ok, err := renderer.FarmRender(ns, markdown)
	if err != nil {
		return FarmNoop, fmt.Errorf("render %s: %w", ns, err)
	}

	if !ok {
		return FarmSkipped, nil
	}

	artifact, err := e.writeFarmArtifact(lot, target.FarmName(ns), rendered, spec)
	if err != nil {
		return FarmNoop, err
	}

	return e.farmLink(filepath.Join(dir, target.FarmName(ns)), artifact)
}

// writeFarmArtifact writes one rendered or lifted definition into the vault
// render area, outside the plugin pivot: the orphan-pivot cleanup only retires
// a pivot that holds nothing but the symlink.
func (e *Engine) writeFarmArtifact(lot farmFileLot, name string, data []byte, spec farmFileSpec) (string, error) {
	marketplace, plugin, _ := strings.Cut(lot.Key, "/")

	lotDir := filepath.Join(e.vault.PluginsDir(), farmRenderDirName, marketplace, plugin, spec.Artifacts)
	if err := os.MkdirAll(lotDir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", lotDir, err)
	}

	artifact := filepath.Join(lotDir, name)

	if existing, err := os.ReadFile(artifact); err != nil || string(existing) != string(data) { //nolint:gosec // the artifact lives in the vault
		if err := fsutil.WriteFileAtomic(artifact, data, 0o600); err != nil {
			return "", fmt.Errorf("write %s: %w", artifact, err)
		}
	}

	return artifact, nil
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

	desired := map[string]bool{}

	for ns, lot := range plan.Desired {
		if lot.Key == key {
			desired[ns] = true
		}
	}

	var warns []string

	for _, file := range files {
		name, ok := trimArtifactExt(file.Name())
		if ok && desired[name] {
			continue
		}

		if err := os.Remove(filepath.Join(dir, file.Name())); err != nil {
			warns = append(warns, fmt.Sprintf("plugin farm: remove %s: %v", file.Name(), err))
		}
	}

	return warns
}

// trimArtifactExt strips the host extension of a rendered artifact. The last
// dot is the boundary, so a plugin name carrying dots survives.
func trimArtifactExt(name string) (string, bool) {
	ext := filepath.Ext(name)
	if ext == "" {
		return "", false
	}

	return strings.TrimSuffix(name, ext), true
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

// farmFileLinkOwner parses a farmed link target; three shapes exist:
// <vault>/plugins/<origin>/<plugin>/<pivot>/<dir>/<file> (markdown hosts, the
// payload dir is plugin-defined), <vault>/plugins/farm/<origin>/<plugin>/<kind>/<name>.<ext>
// (host-format renders), and the same render shape with a markdown extension
// (definitions lifted from a foreign format for markdown hosts).
func (e *Engine) farmFileLinkOwner(link string, spec farmFileSpec) (string, string, bool) {
	root := filepath.Clean(e.vault.PluginsDir())

	rel, ok := strings.CutPrefix(filepath.Clean(link), root+string(filepath.Separator))
	if !ok {
		return "", "", false
	}

	parts := strings.Split(rel, string(filepath.Separator))

	if len(parts) != 5 {
		return "", "", false
	}

	// A rendered artifact in a non-markdown host format can never be mistaken
	// for a plugin payload file: the payload lives in the source format.
	if parts[0] == farmRenderDirName && !markdownSource(parts[4]) {
		name, ok := trimArtifactExt(parts[4])
		if !ok {
			return "", "", false
		}

		return parts[1] + "/" + parts[2], name, true
	}

	// The markdown shape: the payload directory name comes from the plugin
	// manifest, so no fixed name is required for the second-to-last part.
	if pivotEntryName(parts[2]) {
		if name, ok := spec.definitionName(parts[4]); ok {
			return parts[0] + "/" + parts[1], name, true
		}
	}

	// The rendered markdown artifact of a definition lifted from a foreign
	// format (Gemini CLI TOML commands).
	if parts[0] == farmRenderDirName && parts[3] == spec.Artifacts {
		name, ok := trimArtifactExt(parts[4])
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
