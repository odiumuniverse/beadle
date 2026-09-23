package engine

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/skill"
)

// FlatSkillIssues reports the flat `<name>.md` copies the active hosts read
// from their own skills directories: an Info line per host with the names,
// and a Warn for every flat file a same-name skill directory shadows (the
// host keeps the directory copy, the file stays untouched). It runs as part
// of `Doctor()`.
func (e *Engine) FlatSkillIssues(active []*agent.Agent) []Issue {
	var issues []Issue

	for _, a := range active {
		for _, surface := range a.SurfacesOf(kind.Skills) {
			reader, ok := surface.(agent.SkillFlatReader)
			if !ok {
				continue
			}

			readable, shadowed, err := reader.FlatSkillRefs()
			if err != nil {
				issues = append(issues, Issue{Severity: SeverityWarn, Kind: kind.Skills, Agent: a.ID, Message: err.Error()})

				continue
			}

			if len(readable) > 0 {
				names := slices.Sorted(maps.Keys(readable))

				issues = append(issues, Issue{
					Severity: SeverityInfo, Kind: kind.Skills, Agent: a.ID,
					Message: fmt.Sprintf("%d flat skill(s) in %s are read by the host: %s",
						len(names), displayHomePath(surface.Path(), e.home), strings.Join(names, ", ")),
				})
			}

			for _, name := range slices.Sorted(maps.Keys(shadowed)) {
				issues = append(issues, Issue{
					Severity: SeverityWarn, Kind: kind.Skills, Agent: a.ID,
					Message: fmt.Sprintf("flat skill %s is shadowed by %s/%s; the file is left untouched",
						displayHomePath(shadowed[name], e.home), name, skill.FileName),
				})
			}
		}
	}

	return issues
}
