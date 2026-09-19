package engine_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

func antigravityServers(t *testing.T, path string) map[string]map[string]any {
	t.Helper()

	var doc struct {
		Servers map[string]map[string]any `json:"mcpServers"`
	}

	require.NoError(t, json.Unmarshal([]byte(read(t, path)), &doc))

	return doc.Servers
}

func TestAntigravityUnion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	f.config.Enable(agent.AntigravityCLIID)
	require.NoError(t, f.config.Save(f.vault.ConfigPath()))

	agyConfig := filepath.Join(f.home, ".gemini", "config", "mcp_config.json")
	write(t, agyConfig, `{"mcpServers": {}}`)

	write(t, f.claudeConfig(), `{"mcpServers": {
		"alpha": {"type": "stdio", "command": "a", "args": ["x"]},
		"gamma": {"type": "http", "url": "https://gamma.example.com/mcp"}
	}}`)
	write(t, f.openCodeConfig(), `{"mcp": {"beta": {"type": "local", "command": ["b"]}}}`)

	report := f.sync(t)
	require.True(t, report.Kind(kind.MCP).VaultChanged)

	servers := antigravityServers(t, agyConfig)
	require.Len(t, servers, 3)
	require.Equal(t, "a", servers["alpha"]["command"])
	require.Equal(t, []any{"x"}, servers["alpha"]["args"])
	require.Equal(t, "b", servers["beta"]["command"])
	require.Equal(t, "https://gamma.example.com/mcp", servers["gamma"]["serverUrl"], "the remote dialect uses serverUrl")

	for _, entry := range servers {
		require.NotContains(t, entry, "url")
		require.NotContains(t, entry, "httpUrl")
	}

	var doc map[string]any

	require.NoError(t, json.Unmarshal([]byte(read(t, agyConfig)), &doc))

	raw, ok := doc["mcpServers"].(map[string]any)
	require.True(t, ok)

	alpha, ok := raw["alpha"].(map[string]any)
	require.True(t, ok)

	alpha["env"] = map[string]any{"TOKEN": "${LOCAL}"}

	edited, err := json.Marshal(doc)
	require.NoError(t, err)

	write(t, agyConfig, string(edited))

	f.sync(t)

	canon, err := mcp.ParseCanonical([]byte(read(t, f.vault.ServersPath())))
	require.NoError(t, err)
	require.Equal(t, "{env:LOCAL}", canon["alpha"].Env["TOKEN"], "an edit through the antigravity dialect reaches the canon")

	report = f.sync(t)
	require.Empty(t, report.Kind(kind.MCP).Pulled)
	require.False(t, report.VaultChanged())

	agy := agent.AntigravityCLI(f.home)

	for _, k := range []kind.ID{kind.Rules, kind.Skills, kind.Permissions} {
		require.Nil(t, agy.Surface(k), "antigravity v1 must not write %s", k)
	}

	var rels []string

	root := filepath.Join(f.home, ".gemini")

	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		require.NoError(t, err)

		rel, err := filepath.Rel(root, path)
		require.NoError(t, err)

		if rel != "." {
			rels = append(rels, rel)
		}

		return nil
	}))

	require.Equal(t, []string{"config", "config/mcp_config.json"}, rels, "no surface files beyond the shared mcp config")
}
