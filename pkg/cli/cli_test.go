package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()

	root := newRootCmd(Options{Version: "test"})

	var out bytes.Buffer

	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	root.SilenceUsage = true

	err := root.ExecuteContext(t.Context())

	return out.String(), err
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func TestFirstRunFlow(t *testing.T) {
	home := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AGENTSYNC_HOME", filepath.Join(home, ".agent-sync"))

	writeFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {"alpha": {"type": "stdio", "command": "a"}}}`)
	writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "# claude rules\n")
	writeFile(t, filepath.Join(home, ".config", "opencode", "opencode.json"), `{"mcp": {"beta": {"type": "local", "command": ["b"]}}}`)
	writeFile(t, filepath.Join(home, ".config", "opencode", "AGENTS.md"), "# opencode rules\n")

	out, err := runCLI(t, "init")
	require.NoError(t, err)
	require.Contains(t, out, "[x] Claude Code")
	require.Contains(t, out, "[x] OpenCode")
	require.Contains(t, out, "[ ] Gemini CLI")
	require.Contains(t, out, "agent-sync sync --dry-run")

	out, err = runCLI(t, "sync", "--dry-run")
	require.NoError(t, err)
	require.Contains(t, out, "dry run: nothing was written")
	require.NoFileExists(t, filepath.Join(home, ".agent-sync", "mcp", "servers.json"))

	out, err = runCLI(t, "sync")
	require.NoError(t, err)
	require.Contains(t, out, "1 open conflict(s)", "the two rules files disagree")

	out, err = runCLI(t, "conflicts")
	require.NoError(t, err)
	require.Contains(t, out, "rules")
	require.Contains(t, out, "opencode")

	out, err = runCLI(t, "resolve", "--all", "--take", "vault")
	require.NoError(t, err)
	require.Contains(t, out, "resolved 1 conflict(s)")

	rules, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "AGENTS.md")) //nolint:gosec // G304: test reads its own temp file
	require.NoError(t, err)
	require.Equal(t, "# claude rules\n", string(rules))

	out, err = runCLI(t, "status")
	require.NoError(t, err)
	require.Contains(t, out, "conflicts: none")

	_, err = runCLI(t, "doctor")
	require.NoError(t, err)

	out, err = runCLI(t, "agents", "mode", "opencode", "mcp", "pull")
	require.NoError(t, err)
	require.Contains(t, out, "opencode mcp: pull")

	_, err = runCLI(t, "agents", "mode", "cursor", "rules", "sync")
	require.Error(t, err, "Cursor has no rules file")

	out, err = runCLI(t, "kinds", "disable", "skills")
	require.NoError(t, err)
	require.Contains(t, out, "saved")

	out, err = runCLI(t, "history", "rules")
	require.NoError(t, err)
	require.Contains(t, out, "(current)")
}

func TestResolveNeedsADecision(t *testing.T) {
	home := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("AGENTSYNC_HOME", filepath.Join(home, ".agent-sync"))

	_, err := runCLI(t, "init")
	require.NoError(t, err)

	_, err = runCLI(t, "resolve", "deadbeef")
	require.ErrorContains(t, err, "--take")
}

func TestReloadHintOutput(t *testing.T) {
	home := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AGENTSYNC_HOME", filepath.Join(home, ".agent-sync"))

	writeFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {"alpha": {"type": "stdio", "command": "a"}}}`)
	writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "# rules\n")
	writeFile(t, filepath.Join(home, ".config", "opencode", "opencode.json"), `{"mcp": {"beta": {"type": "local", "command": ["b"]}}}`)
	writeFile(t, filepath.Join(home, ".config", "opencode", "AGENTS.md"), "# rules\n")

	_, err := runCLI(t, "init")
	require.NoError(t, err)

	out, err := runCLI(t, "sync")
	require.NoError(t, err)
	require.Contains(t, out, "↻ new Claude Code sessions load MCP changes")
	require.Contains(t, out, "↻ OpenCode reads its config at startup: restart OpenCode to load the changes")

	out, err = runCLI(t, "sync")
	require.NoError(t, err)
	require.NotContains(t, out, "↻")
}
