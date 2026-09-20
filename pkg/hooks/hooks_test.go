package hooks_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/hooks"
)

func TestHooksLoadSaveRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "hooks", "hooks.json")

	canon, err := hooks.Load(path)
	require.NoError(t, err)
	require.Empty(t, canon)

	canon["notify"] = hooks.Hook{Event: "notification", Matcher: "Bash", Command: "echo done", Timeout: 10}
	canon["lint"] = hooks.Hook{Event: "post-tool", Command: "make lint"}
	require.NoError(t, hooks.Save(path, canon))

	loaded, err := hooks.Load(path)
	require.NoError(t, err)
	require.Equal(t, canon, loaded)
}

func TestHooksValidate(t *testing.T) {
	t.Parallel()

	require.NoError(t, hooks.Validate("lint-check", hooks.Hook{Event: "pre-tool", Command: "make lint", Timeout: hooks.MaxTimeout}))

	badNames := []string{"", "Lint", "lint_check", "lint check", "../lint"}
	for _, name := range badNames {
		require.Error(t, hooks.Validate(name, hooks.Hook{Event: "stop", Command: "true"}), name)
	}

	require.Error(t, hooks.Validate("x", hooks.Hook{Event: "before-tool", Command: "true"}))
	require.Error(t, hooks.Validate("x", hooks.Hook{Event: "stop"}))
	require.Error(t, hooks.Validate("x", hooks.Hook{Event: "stop", Command: "  "}))
	require.Error(t, hooks.Validate("x", hooks.Hook{Event: "stop", Command: "true", Timeout: -1}))
	require.Error(t, hooks.Validate("x", hooks.Hook{Event: "stop", Command: "true", Timeout: hooks.MaxTimeout + 1}))

	require.True(t, hooks.ValidEvent("session-start"))
	require.False(t, hooks.ValidEvent("session_end"))
	require.True(t, hooks.ValidName("a-b-1"))
}

func TestHooksApproved(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.ApproveHook("b")
	cfg.ApproveHook("a")
	cfg.ApproveHook("a")

	require.Equal(t, []string{"a", "b"}, cfg.ApprovedHooks)
	require.True(t, cfg.HookApproved("a"))
	require.False(t, cfg.HookApproved("c"))

	approved := hooks.Approved(cfg)
	require.Equal(t, map[string]bool{"a": true, "b": true}, approved)

	cfg.RevokeHook("a")
	require.Equal(t, []string{"b"}, cfg.ApprovedHooks)

	cfg.RevokeHook("b")
	require.Nil(t, cfg.ApprovedHooks)
	require.Empty(t, hooks.Approved(nil))

	path := filepath.Join(t.TempDir(), config.FileName)
	require.NoError(t, cfg.Save(path))

	data, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
	require.NoError(t, err)
	require.NotContains(t, string(data), "approved_hooks")
}
