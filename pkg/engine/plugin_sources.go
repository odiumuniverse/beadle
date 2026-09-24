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
	// Notes describe the suppressed duplicates.
	Notes []string
	// Warns describe divergent copies; nothing is suppressed for them.
	Warns []string
}

// pluginDedup computes the duplicate-plugin presentation from the ledger.
func (e *Engine) pluginDedup(ledger pluginLedger) pluginDedup {
	dedup := pluginDedup{Suppressed: map[string]string{}}

	groups := map[string][]string{}

	for _, key := range slices.Sorted(maps.Keys(ledger.Plugins)) {
		rec := ledger.Plugins[key]

		if !rec.QuarantinedAt.IsZero() || !rec.RetiredAt.IsZero() {
			continue
		}

		origin, name, ok := strings.Cut(key, "/")
		if !ok || !validPluginKey(origin, name) {
			continue
		}

		groups[name] = append(groups[name], key)
	}

	for _, name := range slices.Sorted(maps.Keys(groups)) {
		keys := groups[name]
		if len(keys) < 2 {
			continue
		}

		e.dedupDuplicateGroup(&dedup, keys, ledger)
	}

	return dedup
}

func (e *Engine) dedupDuplicateGroup(dedup *pluginDedup, keys []string, ledger pluginLedger) {
	winner := e.duplicateWinner(keys, ledger)
	if winner == "" {
		return
	}

	winnerRec := ledger.Plugins[winner]

	winnerDigest, err := plugin.ArtifactDigest(recSource(winnerRec), winnerRec.Target)
	if err != nil {
		return
	}

	for _, key := range keys {
		if key == winner {
			continue
		}

		rec := ledger.Plugins[key]

		if recSource(rec) == recSource(winnerRec) {
			// Same-host namespace collisions are reported by the farm owner
			// map, not deduplicated.
			continue
		}

		digest, err := plugin.ArtifactDigest(recSource(rec), rec.Target)
		if err != nil {
			continue
		}

		if digest != winnerDigest {
			dedup.Warns = append(dedup.Warns, duplicateKeepBothWarn(nameOfKey(key), key, winner, slices.Min(keys)))

			continue
		}

		dedup.Suppressed[key] = winner

		dedup.Notes = append(dedup.Notes, duplicateNote(nameOfKey(key), key, winner))
	}
}

// duplicateWinner picks the key that presents the group: the first source in
// host order whose install path resolves inside its own roots. A key whose
// install is unresolvable does not win — presenting an unreadable copy would
// hide the readable ones.
func (e *Engine) duplicateWinner(keys []string, ledger pluginLedger) string {
	ordered := slices.Clone(keys)

	slices.SortFunc(ordered, func(a, b string) int {
		return cmp.Compare(pluginSourceIndex(recSource(ledger.Plugins[a])), pluginSourceIndex(recSource(ledger.Plugins[b])))
	})

	for _, key := range ordered {
		rec := ledger.Plugins[key]

		if _, _, ok := e.pluginTargetRoot(key, rec); ok {
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
