package engine_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/agent"
	"github.com/odiumuniverse/agents-sync/pkg/engine"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
)

func syncWithOpenCodeMCPServer(t *testing.T) (*fixture, *engine.Report) {
	t.Helper()

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	write(t, f.openCodeConfig(), `{"mcp": {"alpha": {"type": "local", "command": ["a"]}}}`)

	return f, f.sync(t)
}

func TestReloadHintOnlyOnWrite(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f, report := syncWithOpenCodeMCPServer(t)

	claude, ok := report.Kind(kind.MCP).Agent(agent.ClaudeCodeID)
	require.True(t, ok)
	require.Equal(t, engine.ActionPushed, claude.Action)
	require.Equal(t, "new Claude Code sessions load MCP changes", claude.ReloadHint)

	report = f.sync(t)

	claude, ok = report.Kind(kind.MCP).Agent(agent.ClaudeCodeID)
	require.True(t, ok)
	require.Equal(t, engine.ActionNoop, claude.Action)
	require.Empty(t, claude.ReloadHint, "a noop carries no reload hint")
}

func TestReloadHintAbsentOnDryRun(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	write(t, f.openCodeConfig(), `{"mcp": {"alpha": {"type": "local", "command": ["a"]}}}`)

	report := f.run(t, engine.SyncOptions{DryRun: true})

	claude, ok := report.Kind(kind.MCP).Agent(agent.ClaudeCodeID)
	require.True(t, ok)
	require.Equal(t, engine.ActionWouldPush, claude.Action)
	require.Empty(t, claude.ReloadHint, "a dry run writes nothing and carries no hint")
}

func TestReloadHintAbsentOnSkipped(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, f.vault.ServersPath(), `{"alpha": {"command": ["a"], "env": {"TOKEN": "{secret:GONE_MISSING}"}}}`)

	report := f.sync(t)

	claude, ok := report.Kind(kind.MCP).Agent(agent.ClaudeCodeID)
	require.True(t, ok)
	require.Equal(t, engine.ActionSkipped, claude.Action)
	require.Empty(t, claude.ReloadHint, "a skipped note is not a completed write")
}

func TestReloadHintAbsentForRules(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	f.sync(t)

	write(t, f.vault.RulesPath(), "# canon rules v2\n")

	report := f.sync(t)

	claude, ok := report.Kind(kind.Rules).Agent(agent.ClaudeCodeID)
	require.True(t, ok)
	require.Equal(t, engine.ActionPushed, claude.Action)
	require.Empty(t, claude.ReloadHint, "rules have no hot-reload hint by design")
}
