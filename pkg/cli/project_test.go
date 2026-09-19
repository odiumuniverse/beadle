package cli

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func newProjectCLIRepo(t *testing.T) string {
	t.Helper()

	home := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))
	t.Setenv("XDG_CONFIG_HOME", "")

	writeFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {}}`)

	repo := t.TempDir()

	cmd := exec.CommandContext(t.Context(), "git", "-C", repo, "init", "-q") //nolint:gosec // G204: fixed git subcommand in a test
	require.NoError(t, cmd.Run())

	t.Chdir(repo)

	_, err := runCLI(t, "init")
	require.NoError(t, err)

	return repo
}

func TestProjectCLIStatusEnableForget(t *testing.T) { //nolint:paralleltest // mutates HOME and the working directory
	repo := newProjectCLIRepo(t)

	out, err := runCLI(t, "project", "status")
	require.NoError(t, err)
	require.Contains(t, out, "project: ")
	require.Contains(t, out, "FILE")
	require.Contains(t, out, "ENABLED")
	require.Contains(t, out, "PUBLISHABLE")
	require.Contains(t, out, "AGENTS.md")
	require.Contains(t, out, ".mcp.json")

	out, err = runCLI(t, "project", "enable", ".mcp.json")
	require.NoError(t, err)
	require.Contains(t, out, "enabled .mcp.json")

	out, err = runCLI(t, "project", "status")
	require.NoError(t, err)
	require.Regexp(t, `(?m)^\.mcp\.json\s+yes`, out)

	require.FileExists(t, filepath.Join(repo, ".mcp.json"), "enable materializes the skeleton")

	_, err = runCLI(t, "sync")
	require.NoError(t, err)

	out, err = runCLI(t, "project", "disable", ".mcp.json")
	require.NoError(t, err)
	require.Contains(t, out, "disabled .mcp.json")

	out, err = runCLI(t, "project", "status")
	require.NoError(t, err)
	require.Regexp(t, `(?m)^\.mcp\.json\s+no`, out)

	_, err = runCLI(t, "project", "forget", ".mcp.json")
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(repo, ".mcp.json"))

	_, err = runCLI(t, "project", "enable", "nope.md")
	require.ErrorContains(t, err, "unknown project file")
}

func TestProjectCLIStatusPathSlug(t *testing.T) {
	home := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))
	t.Setenv("XDG_CONFIG_HOME", "")

	dir := t.TempDir()
	t.Chdir(dir)

	_, err := runCLI(t, "init")
	require.NoError(t, err)

	out, err := runCLI(t, "project", "status")
	require.NoError(t, err)
	require.Contains(t, out, "not a git checkout")
}
