package adapter_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/adapter"
)

func TestClaudeProjectMCP(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()

	missing := adapter.NewClaudeCode(t.TempDir())
	servers, present, err := missing.ProjectMCP(filepath.Join(repo, "no-such-dir"))
	require.NoError(t, err)
	require.False(t, present)
	require.Empty(t, servers)

	claude := adapter.NewClaudeCode(t.TempDir())

	servers, present, err = claude.ProjectMCP(repo)
	require.NoError(t, err)
	require.False(t, present)
	require.Empty(t, servers)

	require.NoError(t, os.WriteFile(
		filepath.Join(repo, ".mcp.json"),
		[]byte(`{"other": 1}`),
		0o600,
	))

	servers, present, err = claude.ProjectMCP(repo)
	require.NoError(t, err)
	require.True(t, present)
	require.Empty(t, servers)

	require.NoError(t, os.WriteFile(
		filepath.Join(repo, ".mcp.json"),
		[]byte(`{"mcpServers": {
			"team-srv": {"type": "stdio", "command": "srv", "args": ["--x"], "env": {"TEAM_VAR": "${TEAM_VAR}"}},
			"web-srv": {"type": "http", "url": "https://example.com/mcp"}
		}}`),
		0o600,
	))

	servers, present, err = claude.ProjectMCP(repo)
	require.NoError(t, err)
	require.True(t, present)
	require.Len(t, servers, 2)

	team := servers["team-srv"]
	require.Equal(t, "stdio", team.Transport)
	require.Equal(t, []string{"srv", "--x"}, team.Command)
	require.Equal(t, map[string]string{"TEAM_VAR": "{env:TEAM_VAR}"}, team.Env)

	web := servers["web-srv"]
	require.Equal(t, "http", web.Transport)
	require.Equal(t, "https://example.com/mcp", web.URL)
}

func TestClaudeLocalProjectMCP(t *testing.T) {
	t.Parallel()

	const entry = `{"type": "stdio", "command": "local-srv", "timeout": 3000}`

	tests := []struct {
		name      string
		config    string
		dir       string
		present   bool
		wantNames []string
		wantErr   bool
	}{
		{
			name:    "missing config",
			dir:     "/repo",
			present: false,
		},
		{
			name:    "no projects section",
			config:  `{"mcpServers": {}}`,
			dir:     "/repo",
			present: false,
		},
		{
			name:    "exact dir match",
			config:  `{"projects": {"/repo": {"mcpServers": {"local-srv": ` + entry + `}}}}`,
			dir:     "/repo",
			present: true,
			wantNames: []string{
				"local-srv",
			},
		},
		{
			name:    "different dir does not match",
			config:  `{"projects": {"/other": {"mcpServers": {"local-srv": ` + entry + `}}}}`,
			dir:     "/repo",
			present: false,
		},
		{
			name:    "matching dir without servers",
			config:  `{"projects": {"/repo": {"history": []}}}`,
			dir:     "/repo",
			present: false,
		},
		{
			name:    "malformed json",
			config:  `{"projects": {`,
			dir:     "/repo",
			present: false,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()

			if tt.config != "" {
				require.NoError(t, os.WriteFile(filepath.Join(home, ".claude.json"), []byte(tt.config), 0o600))
			}

			servers, present, err := adapter.NewClaudeCode(home).LocalProjectMCP(tt.dir)

			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.present, present)

			if !tt.present {
				require.Empty(t, servers)

				return
			}

			require.Len(t, servers, len(tt.wantNames))

			for _, name := range tt.wantNames {
				require.Contains(t, servers, name)
			}

			local := servers["local-srv"]
			require.Equal(t, "stdio", local.Transport)
			require.Equal(t, []string{"local-srv"}, local.Command)
			require.Contains(t, string(local.Extensions["claude-code"]), "3000")
		})
	}
}
