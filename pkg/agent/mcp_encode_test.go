package agent_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

func TestEncodeMCPServers(t *testing.T) {
	t.Parallel()

	servers := kind.Items{
		"plug": mcp.Encode(mcp.Server{Command: []string{"node", "srv.js"}, Env: map[string]string{"TOKEN": "x"}}),
		"web":  mcp.Encode(mcp.Server{Transport: mcp.TransportHTTP, URL: "https://example.com/mcp"}),
	}

	claude, err := agent.EncodeMCPServers(agent.ClaudeCodeID, servers)
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"type":    "stdio",
		"command": "node",
		"args":    []string{"srv.js"},
		"env":     map[string]string{"TOKEN": "x"},
	}, claude["plug"])
	require.Equal(t, map[string]any{"type": "http", "url": "https://example.com/mcp"}, claude["web"])

	gemini, err := agent.EncodeMCPServers(agent.GeminiCLIID, servers)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"httpUrl": "https://example.com/mcp"}, gemini["web"])

	antigravity, err := agent.EncodeMCPServers(agent.AntigravityCLIID, servers)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"serverUrl": "https://example.com/mcp"}, antigravity["web"])
}

func TestEncodeMCPServersUnknownAgent(t *testing.T) {
	t.Parallel()

	_, err := agent.EncodeMCPServers(agent.CursorID, kind.Items{})
	require.ErrorContains(t, err, "no MCP dialect")
}

func TestEncodeMCPServersRejectsBrokenItem(t *testing.T) {
	t.Parallel()

	_, err := agent.EncodeMCPServers(agent.ClaudeCodeID, kind.Items{"plug": []byte("{")})
	require.ErrorContains(t, err, "server plug")
}
