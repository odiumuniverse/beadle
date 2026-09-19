package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/agent"
	"github.com/odiumuniverse/agents-sync/pkg/engine"
)

func shadowIssues(t *testing.T, f *fixture) []engine.Issue {
	t.Helper()

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)

	return issues
}

func findShadowIssue(t *testing.T, issues []engine.Issue, name string) engine.Issue {
	t.Helper()

	for _, issue := range issues {
		if strings.Contains(issue.Message, "skill "+name+" differs between") {
			return issue
		}
	}

	require.FailNow(t, "no shadow issue for "+name, "issues: %v", issues)

	return engine.Issue{}
}

func TestSkillShadowWarnsOnDivergentContent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, f.openCodeSkill("alpha"), "# one\n")
	write(t, f.sharedSkill("alpha"), "# two\n")

	issue := findShadowIssue(t, shadowIssues(t, f), "alpha")

	require.Equal(t, engine.SeverityWarn, issue.Severity)
	require.Equal(t, agent.OpenCodeID, issue.Agent)
	require.Contains(t, issue.Message, "~/.config/opencode/skills")
	require.Contains(t, issue.Message, "~/.agents/skills")
	require.Contains(t, issue.Message, "keep one copy or align the contents")
}

func TestSkillShadowSilentOnIdenticalContent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, f.openCodeSkill("alpha"), "# one\n")
	write(t, f.sharedSkill("alpha"), "# one\n")

	require.False(t, hasIssue(shadowIssues(t, f), engine.SeverityWarn, "differs between"))
}

func TestSkillShadowSilentOnSingleCopy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, f.openCodeSkill("alpha"), "# one\n")

	require.NoFileExists(t, filepath.Join(f.home, ".claude", "skills"))

	issues := shadowIssues(t, f)
	require.False(t, hasIssue(issues, engine.SeverityWarn, "differs between"))
	require.False(t, hasIssue(issues, engine.SeverityWarn, "read skills directory"))
}

func TestSkillShadowSkipsJunkTails(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, f.openCodeSkill("alpha"), "# one\n")
	write(t, f.sharedSkill("alpha"), "# one\n")
	write(t, filepath.Join(f.home, ".agents", "skills", "alpha", ".DS_Store"), "junk\n")

	require.False(t, hasIssue(shadowIssues(t, f), engine.SeverityWarn, "differs between"))
}

func TestSkillShadowPerAgent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	f.config.Enable(agent.CursorID)
	require.NoError(t, f.config.Save(f.vault.ConfigPath()))

	write(t, filepath.Join(f.home, ".cursor", "mcp.json"), `{"mcpServers": {}}`)

	write(t, filepath.Join(f.home, ".claude", "skills", "alpha", "SKILL.md"), "# one\n")
	write(t, f.sharedSkill("alpha"), "# two\n")

	issues := shadowIssues(t, f)

	agents := map[string]int{}

	for _, issue := range issues {
		if strings.Contains(issue.Message, "skill alpha differs between") {
			agents[issue.Agent]++
		}
	}

	require.Equal(t, map[string]int{agent.OpenCodeID: 1, agent.CursorID: 1}, agents, "each affected agent gets its own issue for the same directory pair")
}

func TestSkillShadowReadError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	dir := filepath.Join(f.home, ".config", "opencode", "skills")
	write(t, filepath.Join(dir, "alpha", "SKILL.md"), "# one\n")

	require.NoError(t, os.Chmod(dir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o750) }) //nolint:gosec // G302: restoring the fixture directory mode

	issues := shadowIssues(t, f)

	require.True(t, hasIssue(issues, engine.SeverityWarn, "read skills directory"), "issues: %v", issues)
}
