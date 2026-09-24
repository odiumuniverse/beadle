package engine

import (
	"fmt"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// DSHIssues reports the DeepSeek Harness adapter state. Rules and skills are
// write surfaces since A-38/A-39; the doctor reports what it found: a missing
// harness is an info line (not an error), an empty DSH_HOME is a warning, and
// a present harness lists its paths, the read-only shared skills root and the
// profile count.
func (e *Engine) DSHIssues() []Issue {
	if e.home == "" {
		return nil
	}

	var issues []Issue

	if _, emptyEnv := agent.DSHHome(e.home); emptyEnv {
		issues = append(issues, Issue{Severity: SeverityWarn, Agent: agent.DSHID, Message: agent.DSHHomeNote})
	}

	detected, err := agent.DSHDetected(e.home)
	if err != nil {
		return append(issues, Issue{Severity: SeverityWarn, Agent: agent.DSHID, Message: "DSH: " + err.Error()})
	}

	if !detected {
		return append(issues, Issue{Severity: SeverityInfo, Agent: agent.DSHID, Message: "DSH: not found"})
	}

	home, _ := agent.DSHHome(e.home)
	rules, skills := agent.DSHSurfacePaths(e.home)

	return append(issues,
		Issue{
			Severity: SeverityInfo, Kind: kind.Rules, Agent: agent.DSHID,
			Message: fmt.Sprintf("DSH: home %s; rules %s (write)",
				displayHomePath(home, e.home), displayHomePath(rules, e.home)),
		},
		Issue{
			Severity: SeverityInfo, Kind: kind.Skills, Agent: agent.DSHID,
			Message: fmt.Sprintf("DSH: skills %s (write); shared %s (read-only); profiles %d",
				displayHomePath(skills, e.home), displayHomePath(agent.DSHSharedSkillsDir(e.home), e.home), len(agent.DSHProfiles(e.home))),
		},
	)
}
