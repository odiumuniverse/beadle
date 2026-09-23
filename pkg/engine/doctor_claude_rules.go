package engine

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// Claude project instruction files: beadle manages the native AGENTS.md and
// reports the CLAUDE.md files it deliberately leaves alone.
const (
	claudeAgentsFile       = "AGENTS.md"
	claudeProjectFile      = "CLAUDE.md"
	claudeProjectLocalFile = "CLAUDE.local.md"
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

// claudeProjectRulesIssues reports the project CLAUDE.md files beadle does not
// manage: Claude Code reads the native project AGENTS.md, so a second
// instruction file either duplicates it or loads alongside it. Identical
// contents stay silent — that is one source in two names — but an unmanaged
// CLAUDE.local.md is still reported. The check is gated on the active Claude
// Code agent, like the other Claude notices.
func (e *Engine) claudeProjectRulesIssues(active []*agent.Agent) []Issue {
	if e.cwd == "" || agent.ByID(active, agent.ClaudeCodeID) == nil {
		return nil
	}

	agentsPath := filepath.Join(e.cwd, claudeAgentsFile)
	claudePath := filepath.Join(e.cwd, claudeProjectFile)
	localPath := filepath.Join(e.cwd, claudeProjectLocalFile)

	claude, hasClaude, err := readOptional(claudePath)
	if err != nil {
		return nil
	}

	agents, hasAgents, err := readOptional(agentsPath)
	if err != nil {
		return nil
	}

	switch {
	case hasClaude && hasAgents && !bytes.Equal(claude, agents):
		return []Issue{{
			Severity: SeverityInfo, Kind: kind.Rules, Agent: agent.ClaudeCodeID,
			Message: fmt.Sprintf(
				"project CLAUDE.md (%s) differs from AGENTS.md; Claude Code loads both — keep one source (AGENTS.md)",
				displayHomePath(claudePath, e.home)),
		}}
	case hasClaude && !hasAgents:
		return []Issue{{
			Severity: SeverityInfo, Kind: kind.Rules, Agent: agent.ClaudeCodeID,
			Message: fmt.Sprintf(
				"project CLAUDE.md (%s) is not managed; Claude Code reads AGENTS.md natively — move the content to AGENTS.md and run `beadle project enable AGENTS.md`",
				displayHomePath(claudePath, e.home)),
		}}
	case fsutil.Exists(localPath):
		return []Issue{{
			Severity: SeverityInfo, Kind: kind.Rules, Agent: agent.ClaudeCodeID,
			Message: fmt.Sprintf(
				"project CLAUDE.local.md (%s) is not managed; Claude Code reads AGENTS.md natively — keep one source",
				displayHomePath(localPath, e.home)),
		}}
	}

	return nil
}
