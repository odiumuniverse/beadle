package agent_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/agent"
)

func TestInboxPath(t *testing.T) {
	home := t.TempDir()

	t.Setenv("XDG_CONFIG_HOME", "")

	require.Equal(t, filepath.Join(home, ".config", "opencode", "inbox.md"), agent.InboxPath(home, agent.OpenCodeID))
	require.Equal(t, filepath.Join(home, ".gemini", "inbox.md"), agent.InboxPath(home, agent.GeminiCLIID))
	require.Equal(t, filepath.Join(home, ".cursor", "inbox.md"), agent.InboxPath(home, agent.CursorID))
	require.Empty(t, agent.InboxPath(home, agent.ClaudeCodeID), "claude-code has no inbox")
	require.Empty(t, agent.InboxPath(home, agent.SharedID))

	sandbox := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", sandbox)

	require.Equal(t, filepath.Join(sandbox, "opencode", "inbox.md"), agent.InboxPath(home, agent.OpenCodeID), "the opencode inbox honors XDG_CONFIG_HOME")
	require.Equal(t, filepath.Join(home, ".gemini", "inbox.md"), agent.InboxPath(home, agent.GeminiCLIID), "gemini ignores XDG_CONFIG_HOME")
}
