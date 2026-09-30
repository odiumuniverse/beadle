package engine

import (
	"cmp"
	"maps"
	"slices"

	"github.com/odiumuniverse/beadle/pkg/plugin"
)

const scopeUser = "user"

type pluginGroup struct {
	Origin  string
	Name    string
	Plugins []plugin.Plugin
	// Overridden lists the source hosts whose own same-key copy lost the
	// source conflict: those hosts read their native copy.
	Overridden []string
}

// groupPlugins groups plugin records by their vault key and resolves the
// cross-source collisions: one key installed by several hosts presents the
// copy of the first source in host order. A byte-identical duplicate is a
// note; a divergent copy is a warning. The farm used to keep the losing
// records out of the ledger; the doctor still resolves the collision, because
// a user with two hosts carrying one plugin has to be told which copy wins.
func groupPlugins(plugins []plugin.Plugin) ([]pluginGroup, []string, []string) {
	index := map[string]int{}

	var groups []pluginGroup

	for _, p := range plugins {
		key := pluginKey(p.Origin, p.Name)

		i, ok := index[key]
		if !ok {
			i = len(groups)
			index[key] = i

			groups = append(groups, pluginGroup{Origin: p.Origin, Name: p.Name})
		}

		groups[i].Plugins = append(groups[i].Plugins, p)
	}

	var notes, warns []string

	for i := range groups {
		resolved, overridden, groupNotes, groupWarns := resolveSourceConflict(groups[i])

		groups[i].Plugins = resolved
		groups[i].Overridden = overridden

		notes = append(notes, groupNotes...)
		warns = append(warns, groupWarns...)
	}

	slices.SortFunc(groups, func(a, b pluginGroup) int {
		return cmp.Compare(pluginKey(a.Origin, a.Name), pluginKey(b.Origin, b.Name))
	})

	return groups, notes, warns
}

// resolveSourceConflict picks one source for a key installed by several
// hosts. Copies with an identical artifact digest are the same plugin and
// produce a note; divergent copies produce a warning, because silently
// choosing one would hide the other. A source whose install cannot be read
// does not win while a readable source exists - presenting a broken copy
// would hide the working one; when every copy is unreadable the source order
// decides and the reader warnings stay.
func resolveSourceConflict(group pluginGroup) ([]plugin.Plugin, []string, []string, []string) {
	bySource := map[string][]plugin.Plugin{}

	for _, p := range group.Plugins {
		bySource[p.Source] = append(bySource[p.Source], p)
	}

	if len(bySource) < 2 {
		return group.Plugins, nil, nil, nil
	}

	ordered := orderedSources(bySource)

	winner, winnerDigest := "", ""

	for _, source := range ordered {
		digest, err := plugin.ArtifactDigest(source, bySource[source][0].InstallPath)
		if err != nil {
			continue
		}

		winner, winnerDigest = source, digest

		break
	}

	if winner == "" {
		winner = ordered[0]
	}

	var (
		overridden []string
		notes      []string
		warns      []string
	)

	for _, source := range ordered {
		if source == winner {
			continue
		}

		digest, err := plugin.ArtifactDigest(source, bySource[source][0].InstallPath)
		if err != nil {
			// The copy is gone or unreadable: the host has nothing native to
			// keep reading.
			continue
		}

		overridden = append(overridden, source)

		if winnerDigest == "" {
			continue
		}

		if digest == winnerDigest {
			notes = append(notes, duplicateNote(group.Name, source, winner))

			continue
		}

		warns = append(warns, duplicateWarn(group.Name, source, winner))
	}

	return bySource[winner], overridden, notes, warns
}

// orderedSources lists the sources of a key in host-registration order;
// unknown sources follow alphabetically.
func orderedSources(bySource map[string][]plugin.Plugin) []string {
	var ordered []string

	for _, source := range plugin.SourceHosts() {
		if _, ok := bySource[source]; ok {
			ordered = append(ordered, source)
		}
	}

	for _, source := range slices.Sorted(maps.Keys(bySource)) {
		if !slices.Contains(ordered, source) {
			ordered = append(ordered, source)
		}
	}

	return ordered
}

func chooseRecord(plugins []plugin.Plugin) plugin.Plugin {
	chosen := plugins[0]

	for _, p := range plugins[1:] {
		if pickRecord(p) && !pickRecord(chosen) {
			chosen = p
		}
	}

	return chosen
}

func pickRecord(p plugin.Plugin) bool {
	return p.Scope == scopeUser
}
