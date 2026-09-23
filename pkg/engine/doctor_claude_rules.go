package engine

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// claudeUserRulesIssues reports a non-empty user-level ~/.claude/rules
// directory as a documented non-goal: beadle syncs project .claude/rules only,
// so these rules stay local and invisible to other hosts. The check is gated
// on the active Claude Code agent: a disabled agent's user-level rules are out
// of scope, and the gap would only add noise.
func (e *Engine) claudeUserRulesIssues(active []*agent.Agent) []Issue {
	if e.home == "" || agent.ByID(active, agent.ClaudeCodeID) == nil {
		return nil
	}

	dir := filepath.Join(e.home, ".claude", "rules")

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		return []Issue{{
			Severity: SeverityInfo, Kind: kind.Rules, Agent: agent.ClaudeCodeID,
			Message: "user-level ~/.claude/rules are not synced by beadle (documented non-goal); keep them local or move them into a project or the vault rules document",
		}}
	}

	return nil
}
