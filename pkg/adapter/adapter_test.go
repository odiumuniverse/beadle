package adapter_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/adapter"
	"github.com/odiumuniverse/agents-sync/pkg/mcp"
	"github.com/odiumuniverse/agents-sync/pkg/permission"
)

const claudeConfigFixture = `{
  "numStartups": 42,
  "userID": "abc",
  "mcpServers": {
    "stdio-srv": {
      "type": "stdio",
      "command": "npx",
      "args": ["-y", "pkg"],
      "env": {"K": "V"},
      "timeout": 5000
    },
    "http-srv": {
      "type": "http",
      "url": "https://example.com/mcp",
      "headers": {"Authorization": "Bearer x"}
    }
  },
  "projects": {"/Users/x/proj": {"history": []}}
}
`

func TestClaudeExport(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude.json"), []byte(claudeConfigFixture), 0o600))

	a := adapter.NewClaudeCode(home)

	snapshot, err := a.Export(context.Background())
	require.NoError(t, err)
	require.True(t, snapshot.MCPPresent)
	require.Len(t, snapshot.MCP, 2)

	stdio := snapshot.MCP["stdio-srv"]
	require.Equal(t, "stdio", stdio.Transport)
	require.Equal(t, []string{"npx", "-y", "pkg"}, stdio.Command)
	require.Equal(t, map[string]string{"K": "V"}, stdio.Env)
	require.Contains(t, string(stdio.Extensions["claude-code"]), "5000")

	httpSrv := snapshot.MCP["http-srv"]
	require.Equal(t, "http", httpSrv.Transport)
	require.Equal(t, "https://example.com/mcp", httpSrv.URL)
}

func TestClaudeApplyPreservesUnknownKeys(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configPath := filepath.Join(home, ".claude.json")
	require.NoError(t, os.WriteFile(configPath, []byte(claudeConfigFixture), 0o600))

	a := adapter.NewClaudeCode(home)

	servers := mcp.Servers{
		"stdio-srv": {
			Transport:  "stdio",
			Command:    []string{"npx", "-y", "other"},
			Extensions: map[string]json.RawMessage{"claude-code": json.RawMessage(`{"timeout":7000}`)},
		},
	}

	require.NoError(t, a.Apply(context.Background(), adapter.Update{MCP: servers}))

	text := string(mustReadFile(t, configPath))

	require.Contains(t, text, `"numStartups"`)
	require.Contains(t, text, `"projects"`)
	require.Contains(t, text, "other")
	require.Contains(t, text, "7000")
	require.NotContains(t, text, "http-srv")
}

const openCodeFixture = `{
  // opencode config, keep this comment
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "local-srv": {
      "type": "local",
      "command": ["npx", "-y", "pkg"],
      "environment": {"K": "V"},
      "enabled": true,
      "timeout": 30000
    },
    "remote-srv": {
      "type": "remote",
      "url": "https://example.com/mcp",
      "headers": {"Authorization": "Bearer y"}
    },
    "override-only": {"enabled": false}
  },
  "permission": {"bash": {"*": "allow"}}
}
`

func TestOpenCodeExportApply(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	home := t.TempDir()
	dir := filepath.Join(home, ".config", "opencode")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	configPath := filepath.Join(dir, "opencode.jsonc")
	require.NoError(t, os.WriteFile(configPath, []byte(openCodeFixture), 0o600))

	a := adapter.NewOpenCode(home)

	snapshot, err := a.Export(context.Background())
	require.NoError(t, err)
	require.True(t, snapshot.MCPPresent)
	require.Len(t, snapshot.MCP, 3)

	require.Equal(t, "stdio", snapshot.MCP["local-srv"].Transport)
	require.Equal(t, []string{"npx", "-y", "pkg"}, snapshot.MCP["local-srv"].Command)
	require.Equal(t, map[string]string{"K": "V"}, snapshot.MCP["local-srv"].Env)
	require.Contains(t, string(snapshot.MCP["local-srv"].Extensions["opencode"]), "30000")

	override := snapshot.MCP["override-only"]
	require.Empty(t, override.Transport)
	require.Contains(t, string(override.Extensions["opencode"]), "false")

	servers := mcp.Servers{
		"local-srv":  snapshot.MCP["local-srv"],
		"remote-srv": snapshot.MCP["remote-srv"],
		"added-srv":  {Transport: "stdio", Command: []string{"run"}},
	}

	require.NoError(t, a.Apply(context.Background(), adapter.Update{MCP: servers}))

	text := string(mustReadFile(t, configPath))

	require.Contains(t, text, "keep this comment")
	require.Contains(t, text, `"$schema"`)
	require.Contains(t, text, `"permission"`)
	require.Contains(t, text, "added-srv")
	require.Contains(t, text, "30000")
}

func TestApplyRules(t *testing.T) {
	t.Parallel()

	home := t.TempDir()

	claude := adapter.NewClaudeCode(home)
	require.NoError(t, claude.Apply(context.Background(), adapter.Update{Rules: []byte("# rules\n")}))

	require.Equal(t, "# rules\n", string(mustReadFile(t, filepath.Join(home, ".claude", "CLAUDE.md"))))

	snapshot, err := claude.Export(context.Background())
	require.NoError(t, err)
	require.Equal(t, "# rules\n", string(snapshot.Rules))
}

func TestClaudeMissingConfig(t *testing.T) {
	t.Parallel()

	a := adapter.NewClaudeCode(t.TempDir())

	snapshot, err := a.Export(context.Background())
	require.NoError(t, err)
	require.False(t, snapshot.MCPPresent)
	require.Empty(t, snapshot.MCP)

	err = a.Apply(context.Background(), adapter.Update{MCP: mcp.Servers{}})
	require.ErrorIs(t, err, adapter.ErrNotConfigured)
}

func TestClaudePermissions(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o750))

	settings := `{
  "permissions": {
    "defaultMode": "auto",
    "allow": ["Read", "Edit(~/topscan/**)", "mcp__obsidian__write_note"],
    "ask": ["Bash(git commit:*)"],
    "deny": []
  }
}`
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(settings), 0o600))

	a := adapter.NewClaudeCode(home)

	snapshot, err := a.Export(context.Background())
	require.NoError(t, err)
	require.True(t, snapshot.PermissionsPresent)
	require.Equal(t, map[string]string{
		"tool:read":               "allow",
		"mcp:obsidian:write_note": "allow",
		"bash:git commit*":        "ask",
	}, map[string]string(snapshot.Permissions))
	require.Equal(t, []string{"Edit(~/topscan/**)"}, snapshot.PermissionsOverride.Allow)

	rules := permission.Rules{
		"tool:read":        "allow",
		"bash:git commit*": "deny",
		"tool:task":        "allow",
	}

	require.NoError(t, a.Apply(context.Background(), adapter.Update{
		Permissions: rules,
		Overrides:   snapshot.PermissionsOverride,
	}))

	text := string(mustReadFile(t, filepath.Join(home, ".claude", "settings.json")))
	require.Contains(t, text, `"defaultMode": "auto"`)
	require.Contains(t, text, "Edit(~/topscan/**)")
	require.Contains(t, text, "Bash(git commit:*)")
	require.Contains(t, text, "Task")

	updated, err := a.Export(context.Background())
	require.NoError(t, err)
	require.Equal(t, "deny", updated.Permissions["bash:git commit*"])
	require.Equal(t, "allow", updated.Permissions["tool:task"])
}

func TestOpenCodePermissions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	home := t.TempDir()
	dir := filepath.Join(home, ".config", "opencode")
	require.NoError(t, os.MkdirAll(dir, 0o750))

	configPath := filepath.Join(dir, "opencode.jsonc")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
  // permissions
  "permission": {
    "read": "allow",
    "bash": {"*": "allow", "git commit*": "ask"},
    "external_directory": "allow",
    "codegraph_*": "allow"
  }
}`), 0o600))

	a := adapter.NewOpenCode(home)

	snapshot, err := a.Export(context.Background())
	require.NoError(t, err)
	require.True(t, snapshot.PermissionsPresent)
	require.Equal(t, "allow", snapshot.Permissions["tool:read"])
	require.Equal(t, "ask", snapshot.Permissions["bash:git commit*"])
	require.Equal(t, "allow", snapshot.Permissions["mcp:codegraph:*"])
	require.Equal(t, "allow", snapshot.PermissionsOverride.BashDefault)
	require.Contains(t, snapshot.PermissionsOverride.Extra, "external_directory")

	require.NoError(t, a.Apply(context.Background(), adapter.Update{
		Permissions: snapshot.Permissions,
		Overrides:   snapshot.PermissionsOverride,
	}))

	text := string(mustReadFile(t, configPath))
	require.Contains(t, text, "// permissions")
	require.Contains(t, text, `"*":"allow"`)
	require.Contains(t, text, `"git commit*":"ask"`)
	require.Contains(t, text, `"external_directory":"allow"`)
	require.Contains(t, text, `"codegraph_*":"allow"`)
}

func TestGeminiRoundTrip(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".gemini"), 0o750))

	settings := `{
  "mcpServers": {
    "stdio-srv": {"command": "npx", "args": ["-y", "pkg"], "env": {"K": "V"}, "timeout": 600000},
    "http-srv": {"httpUrl": "https://example.com/mcp", "headers": {"Authorization": "Bearer x"}, "trust": true},
    "sse-srv": {"url": "https://example.com/sse", "headers": {"X-Token": "y"}}
  }
}`
	require.NoError(t, os.WriteFile(filepath.Join(home, ".gemini", "settings.json"), []byte(settings), 0o600))

	a := adapter.NewGeminiCLI(home)

	snapshot, err := a.Export(context.Background())
	require.NoError(t, err)
	require.True(t, snapshot.MCPPresent)
	require.Equal(t, "stdio", snapshot.MCP["stdio-srv"].Transport)
	require.Equal(t, "http", snapshot.MCP["http-srv"].Transport)
	require.Equal(t, "https://example.com/mcp", snapshot.MCP["http-srv"].URL)
	require.Contains(t, string(snapshot.MCP["http-srv"].Extensions["gemini-cli"]), "true")

	require.Equal(t, "sse", snapshot.MCP["sse-srv"].Transport)
	require.Equal(t, "https://example.com/sse", snapshot.MCP["sse-srv"].URL)

	require.NoError(t, a.Apply(context.Background(), adapter.Update{MCP: snapshot.MCP}))

	text := string(mustReadFile(t, filepath.Join(home, ".gemini", "settings.json")))
	require.Contains(t, text, `"httpUrl"`)
	require.Contains(t, text, `"timeout"`)
	require.Contains(t, text, `"trust"`)
	require.Contains(t, text, `"type":"sse"`)
	require.Equal(t, 1, strings.Count(text, `"httpUrl"`), "the SSE server must not gain an httpUrl: %s", text)
}

func TestCursorRoundTrip(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".cursor"), 0o750))

	mcp := `{"mcpServers": {"local-tool": {"command": "npx", "args": ["-y", "pkg"], "envFile": ".env"}, "remote-tool": {"url": "https://example.com/mcp", "auth": {"CLIENT_ID": "id"}}}}`
	require.NoError(t, os.WriteFile(filepath.Join(home, ".cursor", "mcp.json"), []byte(mcp), 0o600))

	a := adapter.NewCursor(home)

	snapshot, err := a.Export(context.Background())
	require.NoError(t, err)
	require.True(t, snapshot.MCPPresent)
	require.Equal(t, "stdio", snapshot.MCP["local-tool"].Transport)
	require.Equal(t, "http", snapshot.MCP["remote-tool"].Transport)
	require.False(t, a.RulesPush(), "cursor user rules have no file to sync")

	require.NoError(t, a.Apply(context.Background(), adapter.Update{MCP: snapshot.MCP}))

	text := string(mustReadFile(t, filepath.Join(home, ".cursor", "mcp.json")))
	require.Contains(t, text, `"envFile"`)
	require.Contains(t, text, `"auth"`)
	require.Contains(t, text, `"type":"stdio"`)
}

func TestEnvRefTranslation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	tests := map[string]struct {
		file    func(home string) (string, string)
		adapter func(home string) adapter.Adapter
		wantEnv string
	}{
		"claude": {
			file: func(home string) (string, string) {
				return filepath.Join(home, ".claude.json"), `{"mcpServers": {"s": {"command": "x", "env": {"TOKEN": "${SECRET}"}}}}`
			},
			adapter: func(home string) adapter.Adapter { return adapter.NewClaudeCode(home) },
		},
		"cursor": {
			file: func(home string) (string, string) {
				return filepath.Join(home, ".cursor", "mcp.json"), `{"mcpServers": {"s": {"type": "stdio", "command": "x", "env": {"TOKEN": "${env:SECRET}"}}}}`
			},
			adapter: func(home string) adapter.Adapter { return adapter.NewCursor(home) },
		},
		"gemini": {
			file: func(home string) (string, string) {
				return filepath.Join(home, ".gemini", "settings.json"), `{"mcpServers": {"s": {"command": "x", "env": {"TOKEN": "$SECRET"}}}}`
			},
			adapter: func(home string) adapter.Adapter { return adapter.NewGeminiCLI(home) },
		},
		"gemini braces": {
			file: func(home string) (string, string) {
				return filepath.Join(home, ".gemini", "settings.json"), `{"mcpServers": {"s": {"command": "x", "env": {"TOKEN": "${SECRET}"}}}}`
			},
			adapter: func(home string) adapter.Adapter { return adapter.NewGeminiCLI(home) },
		},
		"opencode": {
			file: func(home string) (string, string) {
				return filepath.Join(home, ".config", "opencode", "opencode.jsonc"), `{"mcp": {"s": {"type": "local", "command": ["x"], "environment": {"TOKEN": "{env:SECRET}"}}}}`
			},
			adapter: func(home string) adapter.Adapter { return adapter.NewOpenCode(home) },
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			path, content := tt.file(home)

			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

			a := tt.adapter(home)

			snapshot, err := a.Export(context.Background())
			require.NoError(t, err)
			require.Equal(t, "{env:SECRET}", snapshot.MCP["s"].Env["TOKEN"], "canonical ref")

			require.NoError(t, a.Apply(context.Background(), adapter.Update{MCP: snapshot.MCP}))

			text := string(mustReadFile(t, path))

			switch name {
			case "claude", "gemini", "gemini braces":
				require.Contains(t, text, `${SECRET}`)
			case "cursor":
				require.Contains(t, text, `${env:SECRET}`)
			case "opencode":
				require.Contains(t, text, `{env:SECRET}`)
			}
		})
	}
}

func TestGeminiPermissions(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".gemini"), 0o750))

	path := filepath.Join(home, ".gemini", "settings.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
  "mcpServers": {},
  "tools": {
    "core": ["ReadFileTool"],
    "allowed": ["run_shell_command(git status)", "ReadFileTool"],
    "exclude": ["run_shell_command(rm -rf)"]
  }
}`), 0o600))

	a := adapter.NewGeminiCLI(home)

	snapshot, err := a.Export(context.Background())
	require.NoError(t, err)
	require.True(t, snapshot.PermissionsPresent)
	require.Equal(t, "allow", snapshot.Permissions["bash:git status"])
	require.Equal(t, "deny", snapshot.Permissions["bash:rm -rf"])
	require.Equal(t, []string{"ReadFileTool"}, snapshot.PermissionsOverride.Allow)

	rules := permission.Rules{
		"bash:git status": "allow",
		"bash:npm test":   "ask",
	}

	require.NoError(t, a.Apply(context.Background(), adapter.Update{
		Permissions: rules,
		Overrides:   snapshot.PermissionsOverride,
	}))

	text := string(mustReadFile(t, path))
	require.Contains(t, text, `"run_shell_command(npm test)"`)
	require.Contains(t, text, `"ReadFileTool"`)
	require.Contains(t, text, `"core": ["ReadFileTool"]`, "untouched tools keys survive")
	require.NotContains(t, text, "rm -rf")
}

func TestCursorPermissions(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".cursor"), 0o750))

	path := filepath.Join(home, ".cursor", "cli-config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
  "approvalMode": "allowlist",
  "permissions": {
    "allow": ["Shell(ls)", "Read(.env*)"],
    "deny": ["Mcp(dangerous:run)"]
  }
}`), 0o600))

	a := adapter.NewCursor(home)

	snapshot, err := a.Export(context.Background())
	require.NoError(t, err)
	require.True(t, snapshot.PermissionsPresent)
	require.Equal(t, "allow", snapshot.Permissions["bash:ls"])
	require.Equal(t, "deny", snapshot.Permissions["mcp:dangerous:run"])
	require.Equal(t, []string{"Read(.env*)"}, snapshot.PermissionsOverride.Allow)

	rules := permission.Rules{
		"bash:ls":           "allow",
		"mcp:dangerous:run": "deny",
		"bash:test":         "ask",
	}

	require.NoError(t, a.Apply(context.Background(), adapter.Update{
		Permissions: rules,
		Overrides:   snapshot.PermissionsOverride,
	}))

	text := string(mustReadFile(t, path))
	require.Contains(t, text, `"Shell(ls)"`)
	require.Contains(t, text, `"Read(.env*)"`)
	require.Contains(t, text, `"Mcp(dangerous:run)"`)
	require.Contains(t, text, `"approvalMode": "allowlist"`)
	require.NotContains(t, text, "Shell(test)", "cursor has no ask tier")
}

func TestDetect(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	home := t.TempDir()

	claude := adapter.NewClaudeCode(home)

	detected, err := claude.Detect()
	require.NoError(t, err)
	require.False(t, detected)

	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o750))

	detected, err = claude.Detect()
	require.NoError(t, err)
	require.True(t, detected)

	openCode := adapter.NewOpenCode(home)

	detected, err = openCode.Detect()
	require.NoError(t, err)
	require.False(t, detected)
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp files
	require.NoError(t, err)

	return data
}
