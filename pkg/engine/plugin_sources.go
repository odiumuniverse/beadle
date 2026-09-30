package engine

import (
	"cmp"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/plugin"
)

// recSource returns the plugin host of a ledger record; records written
// before plugin sources existed belong to Claude Code.
func recSource(rec pluginLedgerRec) string {
	if rec.Source == "" {
		return plugin.SourceClaudeCode
	}

	return rec.Source
}

// pluginSourceRoots lists the install roots the pivots of one source may
// point into. Every root is resolved before the containment check.
func (e *Engine) pluginSourceRoots(source string) []string {
	home := e.home

	switch source {
	case plugin.SourceCodex:
		return []string{
			filepath.Join(home, ".codex", "plugins"),
			filepath.Join(home, ".agents", "plugins"),
		}
	case plugin.SourceGeminiCLI:
		return []string{filepath.Join(home, ".gemini", "extensions")}
	case plugin.SourceAntigravityCLI:
		return []string{
			filepath.Join(home, ".gemini", "antigravity-cli", "plugins"),
			filepath.Join(home, ".gemini", "config", "plugins"),
			filepath.Join(home, ".agents", "plugins"),
		}
	case plugin.SourceCursor:
		return []string{filepath.Join(home, ".cursor", "plugins")}
	case plugin.SourceOMP:
		// omp keeps every install (marketplace cache and node_modules
		// symlinks) under <ompRoot>/plugins; <ompRoot> is ~/.omp unless
		// PI_CONFIG_DIR relocates it. plugin.OmpRoot is the reader's own
		// resolution, so containment and discovery cannot drift.
		return []string{filepath.Join(plugin.OmpRoot(home), "plugins")}
	default:
		return []string{filepath.Join(home, ".claude", "plugins")}
	}
}

// pluginCacheReachable reports whether any install root of the source exists.
func (e *Engine) pluginCacheReachable(source string) bool {
	for _, root := range e.pluginSourceRoots(source) {
		if _, err := filepath.EvalSymlinks(root); err == nil {
			return true
		}
	}

	return false
}

// pluginPivotDir returns the vault pivot directory of one plugin key.
func (e *Engine) pluginPivotDir(key string) string {
	origin, name, _ := strings.Cut(key, "/")

	return filepath.Join(e.vault.PluginsDir(), origin, name, farmPivotName)
}

// pluginDedup reports the plugin keys whose presentation an equivalent plugin
// already covers: the same plugin name in several hosts with a byte-identical
// artifact digest is presented once, first source wins. Divergent copies are
// not suppressed — they are reported instead.
type pluginDedup struct {
	// Suppressed maps every losing key to the winning key.
	Suppressed map[string]string
	// LoserHosts maps a presented key to the source hosts whose own copy lost
	// the dedup or the same-key source conflict: those hosts read their
	// native copy and must not receive the winner's presentation too.
	LoserHosts map[string]map[string]bool
	// Notes describe the suppressed duplicates.
	Notes []string
	// Warns describe divergent copies; nothing is suppressed for them.
	Warns []string
}

// pluginDedup computes the duplicate-plugin presentation from the host
// registries. The farm's ledger used to answer "which host installed this
// plugin, and which copy wins"; the registries answer both directly, and the
// registry is the truth the plugin manager itself reads.
func (e *Engine) pluginDedup() pluginDedup {
	dedup := pluginDedup{Suppressed: map[string]string{}, LoserHosts: map[string]map[string]bool{}}

	if e.home == "" {
		return dedup
	}

	manifest, err := plugin.ReadAll(e.home)
	if err != nil {
		return dedup
	}

	records := map[string]plugin.Plugin{}

	groups := map[string][]string{}

	resolved, notes, warns := groupPlugins(manifest.Plugins)

	dedup.Notes = notes
	dedup.Warns = warns

	for _, group := range resolved {
		key := pluginKey(group.Origin, group.Name)

		chosen := chooseRecord(group.Plugins)
		records[key] = chosen

		// The host that lists the plugin reads it natively - it loads the
		// plugin's own skills and hooks - so it must not also receive a canon
		// copy of the same items. A host whose copy lost the source conflict
		// reads its native copy for the same reason.
		dedup.markLosers(key, []string{chosen.Source})
		dedup.markLosers(key, group.Overridden)

		groups[group.Name] = append(groups[group.Name], key)
	}

	for _, name := range slices.Sorted(maps.Keys(groups)) {
		keys := groups[name]
		if len(keys) < 2 {
			continue
		}

		e.dedupDuplicateGroup(&dedup, keys, records)
	}

	return dedup
}

func (e *Engine) dedupDuplicateGroup(dedup *pluginDedup, keys []string, records map[string]plugin.Plugin) {
	winner := e.duplicateWinner(keys, records)
	if winner == "" {
		return
	}

	winnerRec := records[winner]

	winnerDigest, err := plugin.ArtifactDigest(winnerRec.Source, winnerRec.InstallPath)
	if err != nil {
		return
	}

	for _, key := range keys {
		if key == winner {
			continue
		}

		rec := records[key]

		if rec.Source == winnerRec.Source {
			// Same-host namespace collisions are two keys with one name, and
			// both stay: they are different plugins to their host.
			continue
		}

		digest, err := plugin.ArtifactDigest(rec.Source, rec.InstallPath)
		if err != nil {
			continue
		}

		if digest != winnerDigest {
			dedup.Warns = append(dedup.Warns, duplicateKeepBothWarn(nameOfKey(key), key, winner, slices.Min(keys)))

			continue
		}

		dedup.Suppressed[key] = winner

		dedup.markLosers(winner, []string{rec.Source})

		dedup.Notes = append(dedup.Notes, duplicateNote(nameOfKey(key), key, winner))
	}
}

// markLosers records the hosts whose own copy of a key lost the presentation.
func (dedup *pluginDedup) markLosers(key string, sources []string) {
	for _, source := range sources {
		if dedup.LoserHosts[key] == nil {
			dedup.LoserHosts[key] = map[string]bool{}
		}

		dedup.LoserHosts[key][source] = true
	}
}

// duplicateWinner picks the key that presents the group: the first source in
// host order whose install path is on disk. A key whose install is missing
// does not win - presenting an unreadable copy would hide the readable ones.
func (e *Engine) duplicateWinner(keys []string, records map[string]plugin.Plugin) string {
	ordered := slices.Clone(keys)

	slices.SortFunc(ordered, func(a, b string) int {
		return cmp.Compare(pluginSourceIndex(records[a].Source), pluginSourceIndex(records[b].Source))
	})

	for _, key := range ordered {
		if isDir(records[key].InstallPath) {
			return key
		}
	}

	return ""
}

func pluginSourceIndex(source string) int {
	for i, id := range plugin.SourceHosts() {
		if id == source {
			return i
		}
	}

	return len(plugin.SourceHosts())
}

func nameOfKey(key string) string {
	_, name, _ := strings.Cut(key, "/")

	return name
}

// duplicateNote renders the informational note of two identical plugin
// copies; name is the plugin name, other and winner describe the copies (a
// plugin key or a host ID).
func duplicateNote(name, other, winner string) string {
	return fmt.Sprintf("plugin %s is installed by %s and %s; presenting the %s copy", name, other, winner, winner)
}

// duplicateWarn renders the warning of two divergent copies of one plugin
// name when the losing copy is dropped (one key, several hosts): the winner's
// records are the ones the farm presents.
func duplicateWarn(name, other, winner string) string {
	return fmt.Sprintf("plugin %s is installed by %s and %s with different content; presenting the %s copy", name, other, winner, winner)
}

// duplicateKeepBothWarn renders the warning of two divergent copies of one
// plugin name that are not suppressed: both keys stay parked, and the farm's
// owner map — first key in plugin order — presents their shared items.
func duplicateKeepBothWarn(name, other, winner, presented string) string {
	return fmt.Sprintf("plugin %s is installed in %s and %s with different content; both copies stay parked and the shared items come from %s",
		name, other, winner, presented)
}
