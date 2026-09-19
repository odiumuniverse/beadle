package agent_test

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

const antigravityReloadHint = "Antigravity CLI reads mcp_config.json at startup: restart agy to load the changes"

func antigravityConfig(home string) string {
	return filepath.Join(home, ".gemini", "config", "mcp_config.json")
}

func antigravitySurface(t *testing.T, home string) agent.Surface {
	t.Helper()

	return surfaceOf(t, agent.AntigravityCLI(home), kind.MCP)
}

func antigravityDoc(t *testing.T, home string) map[string]any {
	t.Helper()

	var doc map[string]any

	require.NoError(t, json.Unmarshal([]byte(readFile(t, antigravityConfig(home))), &doc))

	return doc
}

func antigravityEntry(t *testing.T, home, name string) map[string]any {
	t.Helper()

	servers, ok := antigravityDoc(t, home)["mcpServers"].(map[string]any)
	require.True(t, ok)

	entry, ok := servers[name].(map[string]any)
	require.True(t, ok, "no %s entry", name)

	return entry
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()

	data, err := json.Marshal(value)
	require.NoError(t, err)

	return data
}

func TestAntigravityMCPStdioRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	home := t.TempDir()
	writeFile(t, antigravityConfig(home), `{"mcpServers": {}}`)

	surface := antigravitySurface(t, home)

	require.NoError(t, surface.Write(t.Context(), kind.Items{
		"alpha": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"npx", "-y", "alpha"}}),
	}))

	entry := antigravityEntry(t, home, "alpha")
	require.Equal(t, "npx", entry["command"])
	require.Equal(t, []any{"-y", "alpha"}, entry["args"])

	for _, legacy := range []string{"url", "httpUrl", "serverUrl"} {
		require.NotContains(t, entry, legacy)
	}

	snap := snapshot(t, agent.AntigravityCLI(home), kind.MCP)
	require.Equal(t, []string{"npx", "-y", "alpha"}, server(t, snap.Items["alpha"]).Command)
}

func TestAntigravityMCPRemoteUsesServerURL(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	home := t.TempDir()
	writeFile(t, antigravityConfig(home), `{"mcpServers": {}}`)

	surface := antigravitySurface(t, home)

	require.NoError(t, surface.Write(t.Context(), kind.Items{
		"gamma": mcp.Encode(mcp.Server{
			Transport: mcp.TransportHTTP,
			URL:       "https://gamma.example.com/mcp",
			Headers:   map[string]string{"Authorization": "Bearer token"},
		}),
	}))

	entry := antigravityEntry(t, home, "gamma")
	require.Equal(t, "https://gamma.example.com/mcp", entry["serverUrl"])
	require.NotContains(t, entry, "url")
	require.NotContains(t, entry, "httpUrl")
	require.Contains(t, entry, "headers")

	snap := snapshot(t, agent.AntigravityCLI(home), kind.MCP)

	got := server(t, snap.Items["gamma"])
	require.Equal(t, "https://gamma.example.com/mcp", got.URL)
	require.Equal(t, "Bearer token", got.Headers["Authorization"])
}

func TestAntigravityStripsLegacyKeysKeepsAgentOnly(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	home := t.TempDir()
	writeFile(t, antigravityConfig(home), `{"mcpServers": {"alpha": {
		"command": "legacy",
		"args": ["old"],
		"url": "https://legacy.example.com/mcp",
		"httpUrl": "https://legacy2.example.com/mcp",
		"cwd": "/work",
		"disabled": true,
		"disabledTools": ["x"],
		"authProviderType": "oauth"
	}}}`)

	surface := antigravitySurface(t, home)

	require.NoError(t, surface.Write(t.Context(), kind.Items{
		"alpha": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"new"}}),
	}))

	entry := antigravityEntry(t, home, "alpha")
	require.Equal(t, "new", entry["command"])
	require.NotContains(t, string(mustJSON(t, entry)), "legacy")
	require.NotContains(t, entry, "url")
	require.NotContains(t, entry, "httpUrl")

	require.Equal(t, "/work", entry["cwd"], "an agent-only field must survive the rewrite")
	require.Equal(t, true, entry["disabled"])
	require.Equal(t, []any{"x"}, entry["disabledTools"])
	require.Equal(t, "oauth", entry["authProviderType"])
}

func antigravityEnv(t *testing.T, home, name string) map[string]any {
	t.Helper()

	env, ok := antigravityEntry(t, home, name)["env"].(map[string]any)
	require.True(t, ok)

	return env
}

func TestAntigravityEnvRefsFollowGemini(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	home := t.TempDir()
	writeFile(t, antigravityConfig(home), `{"mcpServers": {}}`)

	surface := antigravitySurface(t, home)

	require.NoError(t, surface.Write(t.Context(), kind.Items{
		"alpha": mcp.Encode(mcp.Server{
			Transport: mcp.TransportStdio,
			Command:   []string{"a"},
			Env:       map[string]string{"TOKEN": "{env:SECRET}"}, //nolint:gosec // G101: a canonical reference, not a credential
		}),
	}))

	require.Equal(t, "${SECRET}", antigravityEnv(t, home, "alpha")["TOKEN"])

	snap := snapshot(t, agent.AntigravityCLI(home), kind.MCP)
	require.Equal(t, "{env:SECRET}", server(t, snap.Items["alpha"]).Env["TOKEN"])
}

func TestAntigravityDetect(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	pureGemini := t.TempDir()
	writeFile(t, filepath.Join(pureGemini, ".gemini", "settings.json"), `{"mcpServers": {}}`)
	writeFile(t, filepath.Join(pureGemini, ".gemini", "GEMINI.md"), "# rules\n")
	writeFile(t, filepath.Join(pureGemini, ".gemini", "skills", "alpha", "SKILL.md"), "# alpha\n")

	detected, err := agent.AntigravityCLI(pureGemini).Detect()
	require.NoError(t, err)
	require.False(t, detected, "a pure Gemini install stays inactive")

	detected, err = agent.AntigravityCLI(t.TempDir()).Detect()
	require.NoError(t, err)
	require.False(t, detected, "a home without antigravity artifacts stays inactive")

	stateHome := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stateHome, ".gemini", "antigravity-cli"), 0o750))

	detected, err = agent.AntigravityCLI(stateHome).Detect()
	require.NoError(t, err)
	require.True(t, detected, "the state directory marks the agent as installed")

	configHome := t.TempDir()
	writeFile(t, antigravityConfig(configHome), `{"mcpServers": {}}`)

	detected, err = agent.AntigravityCLI(configHome).Detect()
	require.NoError(t, err)
	require.True(t, detected, "the shared mcp config survives an uninstalled CLI")
}

func TestAntigravitySurfaceShape(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	home := t.TempDir()
	a := agent.AntigravityCLI(home)

	require.Equal(t, antigravityReloadHint, surfaceOf(t, a, kind.MCP).Traits().ReloadHint)
	require.Equal(t, antigravityConfig(home), surfaceOf(t, a, kind.MCP).Path())

	for _, k := range []kind.ID{kind.Rules, kind.Skills, kind.Permissions, kind.Memory, kind.Projects} {
		require.Nil(t, a.Surface(k), "antigravity v1 has no %s surface", k)
	}
}

func TestAntigravityLegacyURLIsNotCanonized(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	home := t.TempDir()
	writeFile(t, antigravityConfig(home), `{"mcpServers": {"dead": {"url": "https://dead.example.com/mcp", "httpUrl": "https://dead2.example.com/mcp"}}}`)

	snap := snapshot(t, agent.AntigravityCLI(home), kind.MCP)
	require.Empty(t, snap.Items, "legacy url/httpUrl entries are not supported by the dialect")
}

func TestAntigravityKeepsUnmanagedEntries(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	home := t.TempDir()
	writeFile(t, antigravityConfig(home), `{"mcpServers": {}, "unrelated": {"keep": true}}`)

	require.NoError(t, antigravitySurface(t, home).Write(t.Context(), kind.Items{
		"alpha": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"a"}}),
	}))

	var doc map[string]any

	require.NoError(t, json.Unmarshal([]byte(readFile(t, antigravityConfig(home))), &doc))
	require.Contains(t, doc, "unrelated")
}
