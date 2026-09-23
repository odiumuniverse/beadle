package engine

import (
	"fmt"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// DSHIssues reports the DeepSeek Harness adapter state. The surfaces are
// read-only by default in this phase, so the doctor only reports what it
// found: a missing harness is an info line (not an error), an empty DSH_HOME
// is a warning, and a present harness lists its read paths and profile count.
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
			Message: fmt.Sprintf("DSH: read-only by default (A-37); home %s; rules %s",
				displayHomePath(home, e.home), displayHomePath(rules, e.home)),
		},
		Issue{
			Severity: SeverityInfo, Kind: kind.Skills, Agent: agent.DSHID,
			Message: fmt.Sprintf("DSH: read-only by default (A-37); skills %s; profiles %d",
				displayHomePath(skills, e.home), len(agent.DSHProfiles(e.home))),
		},
	)
}
