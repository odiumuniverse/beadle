package agent_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: tests read their own temp files
	require.NoError(t, err)

	return string(data)
}

func surfaceOf(t *testing.T, a *agent.Agent, k kind.ID) agent.Surface {
	t.Helper()

	surface := a.Surface(k)
	require.NotNil(t, surface, "%s has no %s surface", a.ID, k)

	return surface
}

func snapshot(t *testing.T, a *agent.Agent, k kind.ID) agent.Snapshot {
	t.Helper()

	snap, err := surfaceOf(t, a, k).Read(t.Context())
	require.NoError(t, err)

	return snap
}

func server(t *testing.T, data []byte) mcp.Server {
	t.Helper()

	s, err := mcp.Decode(data)
	require.NoError(t, err)

	return s
}

func project(t *testing.T, a *agent.Agent, k kind.ID, key string, value []byte) (string, []byte, bool) {
	t.Helper()

	projector, ok := surfaceOf(t, a, k).(agent.Projector)
	require.True(t, ok, "%s %s surface is not a projector", a.ID, k)

	return projector.Project(key, value)
}

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

func TestClaudeMCPRead(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude.json"), claudeConfigFixture)

	snap := snapshot(t, agent.ClaudeCode(home), kind.MCP)
	require.True(t, snap.Present)
	require.Len(t, snap.Items, 2)

	stdio := server(t, snap.Items["stdio-srv"])
	require.Equal(t, mcp.TransportStdio, stdio.Transport)
	require.Equal(t, []string{"npx", "-y", "pkg"}, stdio.Command)
	require.Equal(t, map[string]string{"K": "V"}, stdio.Env)

	remote := server(t, snap.Items["http-srv"])
	require.Equal(t, mcp.TransportHTTP, remote.Transport)
	require.Equal(t, "https://example.com/mcp", remote.URL)
}

func TestClaudeMCPWritePreservesEverythingElse(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := filepath.Join(home, ".claude.json")
	writeFile(t, path, claudeConfigFixture)

	desired := kind.Items{
		"stdio-srv": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"npx", "-y", "other"}}),
		"added":     mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"run"}}),
	}

	require.NoError(t, surfaceOf(t, agent.ClaudeCode(home), kind.MCP).Write(t.Context(), desired))

	text := readFile(t, path)
	require.Contains(t, text, `"numStartups"`)
	require.Contains(t, text, `"projects"`)
	require.Contains(t, text, "other")
	require.Contains(t, text, "5000", "an agent-local field of a rewritten entry survives")
	require.Contains(t, text, `"added"`)
	require.NotContains(t, text, "http-srv")

	snap := snapshot(t, agent.ClaudeCode(home), kind.MCP)
	require.True(t, desired.Equal(snap.Items), "what was written reads back identically")
}

func TestClaudeMCPWriteWithoutConfigFile(t *testing.T) {
	t.Parallel()

	surface := surfaceOf(t, agent.ClaudeCode(t.TempDir()), kind.MCP)

	snap, err := surface.Read(t.Context())
	require.NoError(t, err)
	require.False(t, snap.Present)

	err = surface.Write(t.Context(), kind.Items{})
	require.ErrorIs(t, err, agent.ErrNotConfigured)
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

func TestOpenCodeMCPKeepsCommentsAndToggles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	home := t.TempDir()
	path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
	writeFile(t, path, openCodeFixture)

	a := agent.OpenCode(home, t.TempDir())

	snap := snapshot(t, a, kind.MCP)
	require.Len(t, snap.Items, 2, "a bare enablement toggle is not a server definition")
	require.Equal(t, []string{"npx", "-y", "pkg"}, server(t, snap.Items["local-srv"]).Command)
	require.Equal(t, mcp.TransportHTTP, server(t, snap.Items["remote-srv"]).Transport)

	desired := kind.Items{
		"local-srv":  snap.Items["local-srv"],
		"remote-srv": snap.Items["remote-srv"],
		"added-srv":  mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"run"}}),
	}

	require.NoError(t, surfaceOf(t, a, kind.MCP).Write(t.Context(), desired))

	text := readFile(t, path)
	require.Contains(t, text, "keep this comment")
	require.Contains(t, text, `"$schema"`)
	require.Contains(t, text, `"override-only": {"enabled": false}`, "unmanaged entries are untouched")
	require.Contains(t, text, `"timeout": 30000`, "untouched entries keep their formatting")
	require.Contains(t, text, "added-srv")
}

func TestOpenCodeProjectsSSEAsRemote(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	sse := mcp.Encode(mcp.Server{Transport: mcp.TransportSSE, URL: "https://example.com/sse"})

	key, value, ok := project(t, agent.OpenCode(t.TempDir(), t.TempDir()), kind.MCP, "legacy", sse)
	require.True(t, ok)
	require.Equal(t, "legacy", key)
	require.Equal(t, mcp.TransportHTTP, server(t, value).Transport, "OpenCode cannot tell SSE from HTTP")
}

func TestGeminiMCPTransports(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := filepath.Join(home, ".gemini", "settings.json")
	original := `{
  "mcpServers": {
    "stdio-srv": {"command": "npx", "args": ["-y", "pkg"], "env": {"K": "V"}, "timeout": 600000},
    "http-srv": {"httpUrl": "https://example.com/mcp", "headers": {"Authorization": "Bearer x"}, "trust": true},
    "sse-srv": {"url": "https://example.com/sse", "headers": {"X-Token": "y"}}
  }
}`
	writeFile(t, path, original)

	a := agent.GeminiCLI(home, t.TempDir())
	snap := snapshot(t, a, kind.MCP)

	require.Equal(t, mcp.TransportStdio, server(t, snap.Items["stdio-srv"]).Transport)
	require.Equal(t, mcp.TransportHTTP, server(t, snap.Items["http-srv"]).Transport)
	require.Equal(t, mcp.TransportSSE, server(t, snap.Items["sse-srv"]).Transport, "a bare url is legacy SSE")

	require.NoError(t, surfaceOf(t, a, kind.MCP).Write(t.Context(), snap.Items))
	require.Equal(t, original, readFile(t, path), "writing what is already there changes nothing")

	moved := snap.Items["sse-srv"]
	sse := server(t, moved)
	sse.URL = "https://example.com/sse2"
	snap.Items["sse-srv"] = mcp.Encode(sse)

	require.NoError(t, surfaceOf(t, a, kind.MCP).Write(t.Context(), snap.Items))

	text := readFile(t, path)
	require.Contains(t, text, "sse2")
	require.Contains(t, text, `"trust": true`)
	require.Equal(t, 1, strings.Count(text, `"httpUrl"`), "the SSE server must not gain an httpUrl: %s", text)
	require.Equal(t, mcp.TransportSSE, server(t, snapshot(t, a, kind.MCP).Items["sse-srv"]).Transport)
}

func TestCursorMCPRoundTrip(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := filepath.Join(home, ".cursor", "mcp.json")
	original := `{"mcpServers": {"local-tool": {"command": "npx", "args": ["-y", "pkg"], "envFile": ".env"}, "remote-tool": {"url": "https://example.com/mcp", "auth": {"CLIENT_ID": "id"}}}}`
	writeFile(t, path, original)

	a := agent.Cursor(home)
	snap := snapshot(t, a, kind.MCP)

	require.Equal(t, mcp.TransportStdio, server(t, snap.Items["local-tool"]).Transport)
	require.Equal(t, mcp.TransportHTTP, server(t, snap.Items["remote-tool"]).Transport)
	require.Nil(t, a.Surface(kind.Rules), "Cursor user rules have no file to sync")

	changed := server(t, snap.Items["local-tool"])
	changed.Command = []string{"npx", "-y", "pkg@2"}
	snap.Items["local-tool"] = mcp.Encode(changed)

	require.NoError(t, surfaceOf(t, a, kind.MCP).Write(t.Context(), snap.Items))

	text := readFile(t, path)
	require.Contains(t, text, `"envFile":".env"`)
	require.Contains(t, text, `"auth": {"CLIENT_ID": "id"}`)
	require.Contains(t, text, "pkg@2")
}

func TestEnvRefTranslation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	tests := map[string]struct {
		file   func(home string) (string, string)
		agent  func(home, cwd string) *agent.Agent
		syntax string
	}{
		"claude": {
			file: func(home string) (string, string) {
				return filepath.Join(home, ".claude.json"), `{"mcpServers": {"s": {"command": "x", "env": {"TOKEN": "${SECRET}"}}}}`
			},
			agent:  func(home, _ string) *agent.Agent { return agent.ClaudeCode(home) },
			syntax: "${SECRET}",
		},
		"cursor": {
			file: func(home string) (string, string) {
				return filepath.Join(home, ".cursor", "mcp.json"), `{"mcpServers": {"s": {"type": "stdio", "command": "x", "env": {"TOKEN": "${env:SECRET}"}}}}`
			},
			agent:  func(home, _ string) *agent.Agent { return agent.Cursor(home) },
			syntax: "${env:SECRET}",
		},
		"gemini bare": {
			file: func(home string) (string, string) {
				return filepath.Join(home, ".gemini", "settings.json"), `{"mcpServers": {"s": {"command": "x", "env": {"TOKEN": "$SECRET"}}}}`
			},
			agent:  agent.GeminiCLI,
			syntax: "${SECRET}",
		},
		"opencode": {
			file: func(home string) (string, string) {
				return filepath.Join(home, ".config", "opencode", "opencode.jsonc"), `{"mcp": {"s": {"type": "local", "command": ["x"], "environment": {"TOKEN": "{env:SECRET}"}}}}`
			},
			agent:  agent.OpenCode,
			syntax: "{env:SECRET}",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			path, content := tt.file(home)
			writeFile(t, path, content)

			a := tt.agent(home, t.TempDir())
			snap := snapshot(t, a, kind.MCP)
			require.Equal(t, "{env:SECRET}", server(t, snap.Items["s"]).Env["TOKEN"], "canonical reference")

			snap.Items["n"] = mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"y"}, Env: map[string]string{"KEY": "{env:SECRET}"}})
			require.NoError(t, surfaceOf(t, a, kind.MCP).Write(t.Context(), snap.Items))

			require.Contains(t, readFile(t, path), `"KEY":"`+tt.syntax+`"`)
		})
	}
}

func TestClaudePermissions(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{
  "permissions": {
    "defaultMode": "auto",
    "allow": ["Read", "Edit(~/topscan/**)", "mcp__obsidian__write_note"],
    "ask": ["Bash(git commit:*)"],
    "deny": []
  }
}`)

	a := agent.ClaudeCode(home)

	snap := snapshot(t, a, kind.Permissions)
	require.Equal(t, kind.Items{
		"tool:read":               []byte("allow"),
		"mcp:obsidian:write_note": []byte("allow"),
		"bash:git commit*":        []byte("ask"),
	}, snap.Items)

	desired := kind.Items{
		"tool:read":        []byte("allow"),
		"bash:git commit*": []byte("deny"),
		"tool:task":        []byte("allow"),
	}

	require.NoError(t, surfaceOf(t, a, kind.Permissions).Write(t.Context(), desired))

	text := readFile(t, path)
	require.Contains(t, text, `"defaultMode": "auto"`)
	require.NotContains(t, text, "mcp__obsidian__write_note")

	var settings struct {
		Permissions map[string][]string `json:"permissions"`
	}

	require.NoError(t, json.Unmarshal([]byte(strings.Replace(text, `"defaultMode": "auto",`, "", 1)), &settings))
	require.Equal(t, []string{"Read", "Edit(~/topscan/**)", "Task"}, settings.Permissions["allow"], "a rule with a path specifier stays local and in place")
	require.Equal(t, []string{"Bash(git commit:*)"}, settings.Permissions["deny"])
	require.Empty(t, settings.Permissions["ask"])

	require.True(t, desired.Equal(snapshot(t, a, kind.Permissions).Items))

	key, _, ok := project(t, a, kind.Permissions, "bash:git commit *", []byte("ask"))
	require.True(t, ok)
	require.Equal(t, "bash:git commit*", key, "Claude's prefix syntax normalizes the pattern")
}

func TestOpenCodePermissions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	home := t.TempDir()
	path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
	writeFile(t, path, `{
  // permissions
  "permission": {
    "read": "allow",
    "bash": {"*": "allow", "git commit*": "ask"},
    "external_directory": "allow",
    "codegraph_*": "allow"
  }
}`)

	a := agent.OpenCode(home, t.TempDir())

	snap := snapshot(t, a, kind.Permissions)
	require.Equal(t, kind.Items{
		"tool:read":        []byte("allow"),
		"bash:git commit*": []byte("ask"),
		"mcp:codegraph:*":  []byte("allow"),
	}, snap.Items, "the bash default and non-portable keys stay local")

	desired := kind.Items{
		"bash:git commit*": []byte("ask"),
		"mcp:codegraph:*":  []byte("allow"),
		"bash:npm test":    []byte("allow"),
	}

	require.NoError(t, surfaceOf(t, a, kind.Permissions).Write(t.Context(), desired))

	text := readFile(t, path)
	require.Contains(t, text, "// permissions")
	require.Contains(t, text, `"*": "allow"`)
	require.Contains(t, text, `"external_directory": "allow"`)
	require.Contains(t, text, `"npm test":"allow"`)
	require.NotContains(t, text, `"read"`)

	require.True(t, desired.Equal(snapshot(t, a, kind.Permissions).Items))

	_, _, ok := project(t, a, kind.Permissions, "mcp:Bad_Server:x", []byte("allow"))
	require.False(t, ok, "a server name OpenCode cannot split is not representable")
}

func TestGeminiPermissions(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := filepath.Join(home, ".gemini", "settings.json")
	writeFile(t, path, `{
  "mcpServers": {},
  "tools": {
    "core": ["ReadFileTool"],
    "allowed": ["run_shell_command(git status)", "ReadFileTool"],
    "exclude": ["run_shell_command(rm -rf)"]
  }
}`)

	a := agent.GeminiCLI(home, t.TempDir())

	snap := snapshot(t, a, kind.Permissions)
	require.Equal(t, kind.Items{"bash:git status": []byte("allow"), "bash:rm -rf": []byte("deny")}, snap.Items)

	desired := kind.Items{"bash:git status": []byte("allow"), "bash:npm test": []byte("ask")}

	require.NoError(t, surfaceOf(t, a, kind.Permissions).Write(t.Context(), desired))

	text := readFile(t, path)
	require.Contains(t, text, `"run_shell_command(npm test)"`)
	require.Contains(t, text, `"ReadFileTool"`)
	require.Contains(t, text, `"core": ["ReadFileTool"]`, "untouched tools keys survive")
	require.NotContains(t, text, "rm -rf")

	_, _, ok := project(t, a, kind.Permissions, "tool:read", []byte("allow"))
	require.False(t, ok, "Gemini tool names are not portable")
}

func TestCursorPermissions(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := filepath.Join(home, ".cursor", "cli-config.json")
	writeFile(t, path, `{
  "approvalMode": "allowlist",
  "permissions": {
    "allow": ["Shell(ls)", "Read(.env*)"],
    "deny": ["Mcp(dangerous:run)"]
  }
}`)

	a := agent.Cursor(home)

	snap := snapshot(t, a, kind.Permissions)
	require.Equal(t, kind.Items{"bash:ls": []byte("allow"), "mcp:dangerous:run": []byte("deny")}, snap.Items)

	require.NoError(t, surfaceOf(t, a, kind.Permissions).Write(t.Context(), kind.Items{
		"bash:ls":           []byte("allow"),
		"mcp:dangerous:run": []byte("deny"),
		"bash:make":         []byte("allow"),
	}))

	text := readFile(t, path)
	require.Contains(t, text, `"Shell(make)"`)
	require.Contains(t, text, `"Read(.env*)"`)
	require.Contains(t, text, `"approvalMode": "allowlist"`)

	_, _, ok := project(t, a, kind.Permissions, "bash:test", []byte("ask"))
	require.False(t, ok, "Cursor has no ask tier")
}

func TestRulesWriteThroughSymlink(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	target := filepath.Join(home, "dotfiles", "CLAUDE.md")
	link := filepath.Join(home, ".claude", "CLAUDE.md")

	writeFile(t, target, "# v1\n")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o750))
	require.NoError(t, os.Symlink(target, link))

	a := agent.ClaudeCode(home)
	require.Equal(t, "# v1\n", string(snapshot(t, a, kind.Rules).Items[kind.RulesKey]))

	require.NoError(t, surfaceOf(t, a, kind.Rules).Write(t.Context(), kind.Items{kind.RulesKey: []byte("# v2\n")}))

	info, err := os.Lstat(link)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink, "the user's symlink stays in place")
	require.Equal(t, "# v2\n", readFile(t, target))
}

func TestRulesAreNeverDeleted(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := filepath.Join(home, ".claude", "CLAUDE.md")
	writeFile(t, path, "# keep\n")

	require.NoError(t, surfaceOf(t, agent.ClaudeCode(home), kind.Rules).Write(t.Context(), kind.Items{}))
	require.Equal(t, "# keep\n", readFile(t, path))
}

func TestSkillsSurface(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "skills")

	writeFile(t, filepath.Join(dir, "real", "SKILL.md"), "real\n")
	writeFile(t, filepath.Join(dir, "real", ".DS_Store"), "junk")
	writeFile(t, filepath.Join(dir, "gone", "SKILL.md"), "gone\n")
	writeFile(t, filepath.Join(dir, "Invalid_Name", "SKILL.md"), "ignored\n")

	external := filepath.Join(home, "skills-src", "linked")
	writeFile(t, filepath.Join(external, "SKILL.md"), "linked\n")
	require.NoError(t, os.Symlink(external, filepath.Join(dir, "linked")))

	plugin := filepath.Join(home, ".claude", "plugins", "cache", "m", "p", "1.0", "skills", "plugged")
	writeFile(t, filepath.Join(plugin, "SKILL.md"), "plugged\n")
	require.NoError(t, os.Symlink(plugin, filepath.Join(dir, "plugged")))

	a := agent.ClaudeCode(home)
	snap := snapshot(t, a, kind.Skills)

	require.Equal(t, kind.Items{
		"real/SKILL.md":   []byte("real\n"),
		"gone/SKILL.md":   []byte("gone\n"),
		"linked/SKILL.md": []byte("linked\n"),
	}, snap.Items, "junk files, invalid names and plugin-owned skills are not items")
	require.Contains(t, snap.ReadOnly, "linked")

	require.NoError(t, surfaceOf(t, a, kind.Skills).Write(t.Context(), kind.Items{
		"real/SKILL.md":   []byte("real v2\n"),
		"linked/SKILL.md": []byte("never written\n"),
		"new/SKILL.md":    []byte("new\n"),
	}))

	require.Equal(t, "real v2\n", readFile(t, filepath.Join(dir, "real", "SKILL.md")))
	require.FileExists(t, filepath.Join(dir, "real", ".DS_Store"), "junk is left alone")
	require.NoDirExists(t, filepath.Join(dir, "gone"))
	require.Equal(t, "new\n", readFile(t, filepath.Join(dir, "new", "SKILL.md")))
	require.Equal(t, "linked\n", readFile(t, filepath.Join(external, "SKILL.md")), "a symlinked skill is never written through")
	require.Equal(t, "plugged\n", readFile(t, filepath.Join(plugin, "SKILL.md")))
}

func TestDetect(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	home := t.TempDir()

	detected, err := agent.ClaudeCode(home).Detect()
	require.NoError(t, err)
	require.False(t, detected)

	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o750))

	detected, err = agent.ClaudeCode(home).Detect()
	require.NoError(t, err)
	require.True(t, detected)

	detected, err = agent.OpenCode(home, t.TempDir()).Detect()
	require.NoError(t, err)
	require.False(t, detected)
}

func TestClaudeProjectScope(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	items, present, err := agent.ClaudeProjectMCP(t.Context(), dir)
	require.NoError(t, err)
	require.False(t, present)
	require.Empty(t, items)

	writeFile(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers": {"repo": {"type": "stdio", "command": "x"}}}`)

	items, present, err = agent.ClaudeProjectMCP(t.Context(), dir)
	require.NoError(t, err)
	require.True(t, present)
	require.Contains(t, items, "repo")

	writeFile(t, filepath.Join(dir, ".mcp.json"), `{`)

	_, _, err = agent.ClaudeProjectMCP(t.Context(), dir)
	require.Error(t, err)

	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude.json"), `{"projects": {"/Users/x/proj": {"mcpServers": {"local": {"type": "stdio", "command": "y"}}}}}`)

	items, present, err = agent.ClaudeLocalMCP(home, "/Users/x/proj")
	require.NoError(t, err)
	require.True(t, present)
	require.Contains(t, items, "local")

	_, present, err = agent.ClaudeLocalMCP(home, "/Users/x/other")
	require.NoError(t, err)
	require.False(t, present, "only the exact recorded path matches")
}

func TestReloadHints(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	home := t.TempDir()
	cwd := t.TempDir()

	tests := []struct {
		name string
		a    func() *agent.Agent
		k    kind.ID
		want string
	}{
		{
			name: "gemini mcp",
			a:    func() *agent.Agent { return agent.GeminiCLI(home, cwd) },
			k:    kind.MCP,
			want: "Gemini CLI: run /mcp reload or restart Gemini CLI to load MCP changes",
		},
		{
			name: "gemini permissions",
			a:    func() *agent.Agent { return agent.GeminiCLI(home, cwd) },
			k:    kind.Permissions,
			want: "Gemini CLI reads settings.json at startup: restart Gemini CLI to load the changes",
		},
		{
			name: "opencode mcp",
			a:    func() *agent.Agent { return agent.OpenCode(home, cwd) },
			k:    kind.MCP,
			want: "OpenCode reads its config at startup: restart OpenCode to load the changes",
		},
		{
			name: "opencode permissions",
			a:    func() *agent.Agent { return agent.OpenCode(home, cwd) },
			k:    kind.Permissions,
			want: "OpenCode reads its config at startup: restart OpenCode to load the changes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", "")

			require.Equal(t, tt.want, surfaceOf(t, tt.a(), tt.k).Traits().ReloadHint)
		})
	}
}

func readableRefs(t *testing.T, a *agent.Agent) []agent.SkillRef {
	t.Helper()

	surface := surfaceOf(t, a, kind.Skills)

	reader, ok := surface.(agent.SkillReader)
	require.True(t, ok)

	refs, err := reader.ReadableSkills()
	require.NoError(t, err)

	return refs
}

func readableDirs(refs []agent.SkillRef) []string {
	dirs := make([]string, 0, len(refs))

	for _, ref := range refs {
		dirs = append(dirs, ref.Dir)
	}

	return dirs
}

func TestReadableSkills(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	home := t.TempDir()
	cwd := t.TempDir()

	writeFile(t, filepath.Join(home, ".config", "opencode", "skills", "oc-own", "SKILL.md"), "# oc\n")
	writeFile(t, filepath.Join(home, ".claude", "skills", "claude-own", "SKILL.md"), "# claude\n")
	writeFile(t, filepath.Join(home, ".agents", "skills", "shared-own", "SKILL.md"), "# shared\n")
	writeFile(t, filepath.Join(home, ".gemini", "skills", "gemini-own", "SKILL.md"), "# gemini\n")
	writeFile(t, filepath.Join(home, ".cursor", "skills", "cursor-own", "SKILL.md"), "# cursor\n")

	ocOwn := filepath.Join(home, ".config", "opencode", "skills")
	claudeOwn := filepath.Join(home, ".claude", "skills")
	sharedOwn := filepath.Join(home, ".agents", "skills")
	geminiOwn := filepath.Join(home, ".gemini", "skills")
	cursorOwn := filepath.Join(home, ".cursor", "skills")

	t.Run("opencode", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		require.Equal(t, []string{ocOwn, claudeOwn, sharedOwn}, readableDirs(readableRefs(t, agent.OpenCode(home, cwd))))
	})

	t.Run("cursor", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		require.Equal(t, []string{cursorOwn, claudeOwn, sharedOwn}, readableDirs(readableRefs(t, agent.Cursor(home))))
	})

	t.Run("gemini", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		require.Equal(t, []string{geminiOwn, sharedOwn}, readableDirs(readableRefs(t, agent.GeminiCLI(home, cwd))))
	})

	t.Run("claude", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		require.Equal(t, []string{claudeOwn}, readableDirs(readableRefs(t, agent.ClaudeCode(home))))
	})

	t.Run("shared", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		require.Equal(t, []string{sharedOwn}, readableDirs(readableRefs(t, agent.SharedSkills(home))))
	})

	t.Run("xdg config dir", func(t *testing.T) {
		xdg := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", xdg)

		writeFile(t, filepath.Join(xdg, "opencode", "skills", "xdg-own", "SKILL.md"), "# xdg\n")

		require.Equal(t, []string{filepath.Join(xdg, "opencode", "skills"), claudeOwn, sharedOwn}, readableDirs(readableRefs(t, agent.OpenCode(home, cwd))))
	})
}

func TestReadableSkillsGates(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	home := t.TempDir()
	cwd := t.TempDir()

	skills := filepath.Join(home, ".config", "opencode", "skills")

	writeFile(t, filepath.Join(skills, "own", "SKILL.md"), "# own\n")

	linked := filepath.Join(home, "elsewhere", "linked")
	writeFile(t, filepath.Join(linked, "SKILL.md"), "# linked\n")
	require.NoError(t, os.Symlink(linked, filepath.Join(skills, "linked")))

	plugged := filepath.Join(home, ".claude", "plugins", "cache", "acme", "tool", "1.0.0", "skills", "plugged")
	writeFile(t, filepath.Join(plugged, "SKILL.md"), "# plugged\n")
	require.NoError(t, os.Symlink(plugged, filepath.Join(skills, "plugged")))

	writeFile(t, filepath.Join(skills, "stub", "SKILL.md"), "# stub\n")
	writeFile(t, filepath.Join(skills, "stub", ".agent-sync-quarantine"), "v1 acme/tool x 2026-01-01T00:00:00Z\n")

	byName := map[string]string{}
	for _, ref := range readableRefs(t, agent.OpenCode(home, cwd)) {
		byName[ref.Name] = ref.Root
	}

	require.Equal(t, filepath.Join(skills, "own"), byName["own"])
	require.Equal(t, agent.RealPath(linked), byName["linked"], "a symlinked skill resolves to its real root")
	require.NotContains(t, byName, "plugged", "plugin cache links are not user duplicates")
	require.NotContains(t, byName, "stub", "quarantine stubs are skipped")

	require.Empty(t, readableRefs(t, agent.OpenCode(t.TempDir(), cwd)), "missing directories are not an error")

	require.NoError(t, os.Chmod(skills, 0o000))
	t.Cleanup(func() { _ = os.Chmod(skills, 0o750) }) //nolint:gosec // G302: restoring the fixture directory mode

	reader, ok := surfaceOf(t, agent.OpenCode(home, cwd), kind.Skills).(agent.SkillReader)
	require.True(t, ok)

	_, err := reader.ReadableSkills()
	require.Error(t, err, "a real read error is reported")
}
