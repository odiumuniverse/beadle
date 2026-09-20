package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPluginsPinCommands(t *testing.T) {
	home := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))

	_, err := runCLI(t, "init")
	require.NoError(t, err)

	out, err := runCLI(t, "plugins", "pin", "acme/tool", "1.0.0", "--agent", "opencode")
	require.NoError(t, err)
	require.Contains(t, out, "opencode: acme/tool pinned to 1.0.0")

	out, err = runCLI(t, "plugins", "pins")
	require.NoError(t, err)
	require.Contains(t, out, "opencode")
	require.Contains(t, out, "acme/tool")
	require.Contains(t, out, "unknown-plugin")

	cache := filepath.Join(home, ".claude", "plugins", "cache", "acme", "tool", "1.0.0")
	require.NoError(t, os.MkdirAll(cache, 0o750))

	writeFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), `{
  "plugins": {"tool@acme": [{"scope": "user", "installPath": "`+cache+`", "version": "1.0.0"}]}
}`)

	out, err = runCLI(t, "plugins", "pins")
	require.NoError(t, err)
	require.Contains(t, out, "ok")

	out, err = runCLI(t, "plugins", "pins", "--agent", "claude-code")
	require.NoError(t, err)
	require.Contains(t, out, "no plugin pins")

	_, err = runCLI(t, "plugins", "pin", "acme/tool", "../bad", "--agent", "opencode")
	require.ErrorContains(t, err, "invalid version")

	_, err = runCLI(t, "plugins", "pin", "acme/tool", "1.0.0", "--agent", "ghost")
	require.ErrorContains(t, err, "unknown agent")

	_, err = runCLI(t, "plugins", "pin", "acme/tool", "1.0.0")
	require.ErrorContains(t, err, "--agent is required")

	out, err = runCLI(t, "plugins", "unpin", "acme/tool", "--agent", "opencode")
	require.NoError(t, err)
	require.Contains(t, out, "opencode: acme/tool unpinned")

	out, err = runCLI(t, "plugins", "pins")
	require.NoError(t, err)
	require.Contains(t, out, "no plugin pins")
}
