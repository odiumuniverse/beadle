package engine

import (
	"fmt"

	"github.com/odiumuniverse/beadle/pkg/agent"
)

// OmpIssues reports the oh-my-pi adapter state: the resolved user root, the
// agent directory, the active named profile and the profile count. A missing
// host is an info line, a set-but-empty PI_CONFIG_DIR is reported as info
// (omp ignores the empty value and falls back to ~/.omp — a warning would
// fire on a healthy install where the variable is merely exported empty), and
// a present host reports where it reads from — never a warning.
func (e *Engine) OmpIssues() []Issue {
	if e.home == "" {
		return nil
	}

	var issues []Issue

	if _, emptyEnv := agent.OmpHome(e.home); emptyEnv {
		issues = append(issues, Issue{Severity: SeverityInfo, Agent: agent.OmpID, Message: agent.OmpHomeNote})
	}

	detected, err := agent.OmpDetected(e.home)
	if err != nil {
		return append(issues, Issue{Severity: SeverityWarn, Agent: agent.OmpID, Message: "oh-my-pi: " + err.Error()})
	}

	if !detected {
		return append(issues, Issue{Severity: SeverityInfo, Agent: agent.OmpID, Message: "oh-my-pi: not found"})
	}

	root, _ := agent.OmpHome(e.home)
	agentDir := agent.OmpAgentDir(e.home)

	profile := "default"
	if name, ok := agent.OmpProfile(); ok {
		profile = name
	}

	configured := "config present"
	if !agent.OmpConfigured(e.home) {
		// The binary alone marks omp present (beadle can deliver into a
		// fresh home), but nothing has set the host up yet: say so instead
		// of reporting a bare "installed".
		configured = "binary on PATH only, no config.yml yet"
	}

	// The message deliberately avoids the word "plugin": it carries no plugin
	// finding, and doctor's registry guard scans every message for it.
	return append(issues, Issue{
		Severity: SeverityInfo, Agent: agent.OmpID,
		Message: fmt.Sprintf("oh-my-pi: home %s; agent %s; profile %s; profiles %d; %s",
			displayHomePath(root, e.home), displayHomePath(agentDir, e.home),
			profile, len(agent.OmpProfiles(e.home)), configured),
	})
}
