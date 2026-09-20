package cli

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHooksCommands(t *testing.T) {
	home := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))

	_, err := runCLI(t, "init")
	require.NoError(t, err)

	out, err := runCLI(t, "hooks", "list")
	require.NoError(t, err)
	require.Contains(t, out, "no hooks")

	out, err = runCLI(t, "hooks", "add", "notify", "--event", "notification", "--command", "echo done", "--timeout", "5")
	require.NoError(t, err)
	require.Contains(t, out, "hook notify saved")

	out, err = runCLI(t, "hooks", "list")
	require.NoError(t, err)
	require.Contains(t, out, "notify")
	require.Contains(t, out, "notification")
	require.Contains(t, out, "pending")
	require.NotContains(t, out, "echo done")

	out, err = runCLI(t, "hooks", "list", "--commands")
	require.NoError(t, err)
	require.Contains(t, out, "echo done")

	_, err = runCLI(t, "hooks", "approve", "ghost")
	require.ErrorContains(t, err, "unknown hook")

	out, err = runCLI(t, "hooks", "approve", "notify")
	require.NoError(t, err)
	require.Contains(t, out, "hook notify approved")

	out, err = runCLI(t, "hooks", "list")
	require.NoError(t, err)
	require.Contains(t, out, "approved")

	_, err = runCLI(t, "hooks", "add", "bad", "--event", "nope", "--command", "true")
	require.ErrorContains(t, err, "unknown event")

	_, err = runCLI(t, "hooks", "add", "BadName", "--event", "stop", "--command", "true")
	require.ErrorContains(t, err, "invalid hook name")

	out, err = runCLI(t, "hooks", "revoke", "notify")
	require.NoError(t, err)
	require.Contains(t, out, "hook notify revoked")

	out, err = runCLI(t, "hooks", "list")
	require.NoError(t, err)
	require.Contains(t, out, "pending")

	out, err = runCLI(t, "hooks", "rm", "notify")
	require.NoError(t, err)
	require.Contains(t, out, "hook notify removed")

	_, err = runCLI(t, "hooks", "rm", "notify")
	require.ErrorContains(t, err, "unknown hook")

	out, err = runCLI(t, "hooks", "add", "notify", "--event", "notification", "--command", "echo again")
	require.NoError(t, err)
	require.Contains(t, out, "hook notify saved")

	out, err = runCLI(t, "hooks", "list")
	require.NoError(t, err)
	require.Contains(t, out, "pending", "removing a hook must drop its approval")
}
