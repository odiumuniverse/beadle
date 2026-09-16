package engine_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/agent"
	"github.com/odiumuniverse/agents-sync/pkg/engine"
)

func TestDoctorReportsBrokenPluginReferences(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.config.Enable(agent.GeminiCLIID)
	f.config.Enable(agent.CursorID)
	require.NoError(t, f.config.Save(f.vault.ConfigPath()))

	keep := filepath.Join(f.home, ".claude", "plugins", "cache", "acme", "tool", "1.0.0", "keep.sh")
	write(t, keep, "x")

	missing := filepath.Join(f.home, ".claude", "plugins", "marketplaces", "thedotmack", "plugin", "scripts", "worker-service.cjs")
	missingTilde := "~/.claude/plugins/marketplaces/thedotmack/plugin/scripts/tilde.cjs"
	missingCursor := filepath.Join(f.home, ".claude", "plugins", "marketplaces", "thedotmack", "plugin", "scripts", "mcp-server.cjs")

	write(t, f.geminiSettings(), fmt.Sprintf(`{
  "hooks": {
    "SessionStart": [{"hooks": [{"command": "\"bun\" \"%s\" hook"}]}],
    "BeforeAgent": [{"hooks": [{"command": "sh %s"}]}],
    "AfterAgent": [{"hooks": [{"command": "${CLAUDE_PLUGIN_ROOT}/scripts/x.cjs"}]}],
    "BeforeTool": [{"hooks": [{"command": "cat %s"}]}]
  },
  "mcpServers": {}
}`, missing, keep, missingTilde))

	write(t, filepath.Join(f.home, ".cursor", "mcp.json"),
		fmt.Sprintf(`{"mcpServers":{"claude-mem":{"command":"node","args":[%q]}}}`, missingCursor))

	write(t, f.claudeRules(), "see "+missing+" for details\n")

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)

	var broken []engine.Issue

	for _, issue := range issues {
		if strings.Contains(issue.Message, "broken plugin reference") {
			broken = append(broken, issue)
		}
	}

	require.Len(t, broken, 3)

	for _, issue := range broken {
		require.Equal(t, engine.SeverityError, issue.Severity)
		require.NotContains(t, issue.Message, "keep.sh")
		require.NotContains(t, issue.Message, "CLAUDE_PLUGIN_ROOT")
	}

	var gemini, cursor []engine.Issue

	for _, issue := range broken {
		switch issue.Agent {
		case agent.GeminiCLIID:
			gemini = append(gemini, issue)
		case agent.CursorID:
			cursor = append(cursor, issue)
		}
	}

	require.Len(t, gemini, 2)
	require.Len(t, cursor, 1)

	require.Contains(t, gemini[0].Message, "worker-service.cjs")
	require.Contains(t, gemini[0].Message, "/hooks/SessionStart/0/hooks/0/command")
	require.True(t, strings.HasPrefix(gemini[0].Message,
		"broken plugin reference: ~/.claude/plugins/marketplaces/thedotmack/plugin/scripts/worker-service.cjs ("),
		"message: %s", gemini[0].Message)
	require.True(t, strings.HasSuffix(gemini[0].Message, "/hooks/SessionStart/0/hooks/0/command)"),
		"message: %s", gemini[0].Message)
	require.Contains(t, gemini[1].Message, "tilde.cjs")
	require.Contains(t, gemini[1].Message, "/hooks/BeforeTool/0/hooks/0/command")
	require.Contains(t, cursor[0].Message, "mcp-server.cjs")
	require.Contains(t, cursor[0].Message, "/mcpServers/claude-mem/args/0")
}

func TestDoctorPluginReferencesResolve(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.config.Enable(agent.GeminiCLIID)
	f.config.Enable(agent.CursorID)
	require.NoError(t, f.config.Save(f.vault.ConfigPath()))

	keep := filepath.Join(f.home, ".claude", "plugins", "cache", "acme", "tool", "1.0.0", "keep.sh")
	write(t, keep, "x")

	write(t, f.geminiSettings(), fmt.Sprintf(`{
  "hooks": {"SessionStart": [{"hooks": [{"command": "sh %s"}]}]},
  "mcpServers": {}
}`, keep))

	write(t, filepath.Join(f.home, ".cursor", "mcp.json"),
		fmt.Sprintf(`{"mcpServers":{"claude-mem":{"command":"node","args":[%q]}}}`, keep))

	write(t, f.claudeRules(), "# r\n")

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)

	for _, issue := range issues {
		require.NotContains(t, issue.Message, "broken plugin reference")
	}
}
