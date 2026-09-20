package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBundlesCLIStubEndToEnd(t *testing.T) {
	home := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))

	binDir := filepath.Join(t.TempDir(), "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o750))

	logPath := filepath.Join(t.TempDir(), "claude.log")
	script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o700)) //nolint:gosec // G306: the stub must stay executable

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := runCLI(t, "init")
	require.NoError(t, err)

	out, err := runCLI(t, "bundles", "enable", "--host", "claude")
	require.NoError(t, err)
	require.Contains(t, out, "enabled")
	require.Contains(t, out, "(registered)")

	calls, err := os.ReadFile(logPath) //nolint:gosec // G304: test reads its own temp file
	require.NoError(t, err)
	require.Contains(t, string(calls), "plugin marketplace add "+filepath.Join(home, ".beadle", "bundles", "claude"))
	require.Contains(t, string(calls), "plugin install beadle-canon@beadle")

	config, err := os.ReadFile(filepath.Join(home, ".beadle", "config.json")) //nolint:gosec // G304: test reads its own temp file
	require.NoError(t, err)
	require.Contains(t, string(config), `"off"`)

	out, err = runCLI(t, "bundles", "disable", "--host", "claude")
	require.NoError(t, err)
	require.Contains(t, out, "disabled")

	calls, err = os.ReadFile(logPath) //nolint:gosec // G304: test reads its own temp file
	require.NoError(t, err)
	require.Contains(t, string(calls), "plugin uninstall beadle-canon@beadle")
	require.Contains(t, string(calls), "plugin marketplace rm beadle")

	config, err = os.ReadFile(filepath.Join(home, ".beadle", "config.json")) //nolint:gosec // G304: test reads its own temp file
	require.NoError(t, err)
	require.NotContains(t, string(config), `"off"`)
}

func TestBundlesCommands(t *testing.T) {
	home := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))

	_, err := runCLI(t, "init")
	require.NoError(t, err)

	out, err := runCLI(t, "bundles", "status")
	require.NoError(t, err)
	require.Contains(t, out, "host")
	require.Contains(t, out, "claude")
	require.Contains(t, out, "gemini")
	require.Contains(t, out, "antigravity")

	out, err = runCLI(t, "bundles", "enable", "--host", "antigravity")
	require.NoError(t, err)
	require.Contains(t, out, "generated")
	require.Contains(t, out, "ln -s")

	out, err = runCLI(t, "bundles", "status")
	require.NoError(t, err)
	require.Contains(t, out, "yes")

	_, err = runCLI(t, "bundles", "enable")
	require.ErrorContains(t, err, "--host is required")

	_, err = runCLI(t, "bundles", "enable", "--host", "cursor")
	require.ErrorContains(t, err, "unknown bundle host")

	out, err = runCLI(t, "bundles", "disable", "--host", "antigravity")
	require.NoError(t, err)
	require.Contains(t, out, "disabled")

	require.FileExists(t, filepath.Join(home, ".beadle", "bundles", "antigravity", "plugin.json"))
}
