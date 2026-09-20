package agent_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: tests read their own temp files
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(data)
}

func surfaceOf(t *testing.T, a *agent.Agent, k kind.ID) agent.Surface {
	t.Helper()

	surface := a.Surface(k)
	if surface == nil {
		t.Fatalf("%s has no %s surface", a.ID, k)
	}

	return surface
}

func snapshot(t *testing.T, a *agent.Agent, k kind.ID) agent.Snapshot {
	t.Helper()

	snap, err := surfaceOf(t, a, k).Read(t.Context())
	if err != nil {
		t.Fatalf("read %s %s: %v", a.ID, k, err)
	}

	return snap
}

func server(t *testing.T, data []byte) mcp.Server {
	t.Helper()

	s, err := mcp.Decode(data)
	if err != nil {
		t.Fatalf("decode server: %v", err)
	}

	return s
}

func project(t *testing.T, a *agent.Agent, k kind.ID, key string, value []byte) (string, []byte, bool) {
	t.Helper()

	projector, ok := surfaceOf(t, a, k).(agent.Projector)
	if !ok {
		t.Fatalf("%s %s surface is not a projector", a.ID, k)
	}

	return projector.Project(key, value)
}

func readableRefs(t *testing.T, a *agent.Agent) []agent.SkillRef {
	t.Helper()

	surface := surfaceOf(t, a, kind.Skills)

	reader, ok := surface.(agent.SkillReader)
	if !ok {
		t.Fatal("skills surface is not a SkillReader")
	}

	refs, err := reader.ReadableSkills()
	if err != nil {
		t.Fatalf("readable skills: %v", err)
	}

	return refs
}

func readableDirs(refs []agent.SkillRef) []string {
	dirs := make([]string, 0, len(refs))

	for _, ref := range refs {
		dirs = append(dirs, ref.Dir)
	}

	return dirs
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
	Convey("Given a Claude config with two MCP servers", t, func() {
		home := t.TempDir()
		writeFile(t, filepath.Join(home, ".claude.json"), claudeConfigFixture)

		snap := snapshot(t, agent.ClaudeCode(home, t.TempDir()), kind.MCP)

		Convey("When it is read", func() {
			stdio := server(t, snap.Items["stdio-srv"])
			remote := server(t, snap.Items["http-srv"])

			Convey("Then stdio and remote servers decode", func() {
				So(snap.Present, ShouldBeTrue)
				So(snap.Items, ShouldHaveLength, 2)

				So(stdio.Transport, ShouldEqual, mcp.TransportStdio)
				So(stdio.Command, ShouldResemble, []string{"npx", "-y", "pkg"})
				So(stdio.Env, ShouldResemble, map[string]string{"K": "V"})

				So(remote.Transport, ShouldEqual, mcp.TransportHTTP)
				So(remote.URL, ShouldEqual, "https://example.com/mcp")
			})
		})
	})
}

func TestClaudeMCPWritePreservesEverythingElse(t *testing.T) {
	Convey("Given a Claude config", t, func() {
		home := t.TempDir()
		path := filepath.Join(home, ".claude.json")
		writeFile(t, path, claudeConfigFixture)

		desired := kind.Items{
			"stdio-srv": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"npx", "-y", "other"}}),
			"added":     mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"run"}}),
		}

		Convey("When a server is rewritten and one added", func() {
			So(surfaceOf(t, agent.ClaudeCode(home, t.TempDir()), kind.MCP).Write(t.Context(), desired), ShouldBeNil)

			text := readFile(t, path)

			Convey("Then foreign keys and agent-local fields survive", func() {
				So(text, ShouldContainSubstring, `"numStartups"`)
				So(text, ShouldContainSubstring, `"projects"`)
				So(text, ShouldContainSubstring, "other")
				So(text, ShouldContainSubstring, "5000")
				So(text, ShouldContainSubstring, `"added"`)
				So(text, ShouldNotContainSubstring, "http-srv")

				snap := snapshot(t, agent.ClaudeCode(home, t.TempDir()), kind.MCP)
				So(desired.Equal(snap.Items), ShouldBeTrue)
			})
		})
	})
}

func TestClaudeMCPWriteWithoutConfigFile(t *testing.T) {
	Convey("Given no Claude config file", t, func() {
		surface := surfaceOf(t, agent.ClaudeCode(t.TempDir(), t.TempDir()), kind.MCP)

		Convey("When read and written", func() {
			snap, err := surface.Read(t.Context())
			So(err, ShouldBeNil)

			err = surface.Write(t.Context(), kind.Items{})

			Convey("Then it is absent and writing refuses", func() {
				So(snap.Present, ShouldBeFalse)
				So(errors.Is(err, agent.ErrNotConfigured), ShouldBeTrue)
			})
		})
	})
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
	Convey("Given an OpenCode config with comments and toggles", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, openCodeFixture)

		a := agent.OpenCode(home, t.TempDir())

		snap := snapshot(t, a, kind.MCP)

		Convey("When it is read and written", func() {
			So(snap.Items, ShouldHaveLength, 2)
			So(server(t, snap.Items["local-srv"]).Command, ShouldResemble, []string{"npx", "-y", "pkg"})
			So(server(t, snap.Items["remote-srv"]).Transport, ShouldEqual, mcp.TransportHTTP)

			desired := kind.Items{
				"local-srv":  snap.Items["local-srv"],
				"remote-srv": snap.Items["remote-srv"],
				"added-srv":  mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"run"}}),
			}

			So(surfaceOf(t, a, kind.MCP).Write(t.Context(), desired), ShouldBeNil)

			text := readFile(t, path)

			Convey("Then comments, schema and unmanaged entries survive", func() {
				So(text, ShouldContainSubstring, "keep this comment")
				So(text, ShouldContainSubstring, `"$schema"`)
				So(text, ShouldContainSubstring, `"override-only": {"enabled": false}`)
				So(text, ShouldContainSubstring, `"timeout": 30000`)
				So(text, ShouldContainSubstring, "added-srv")
			})
		})
	})
}

func TestOpenCodeProjectsSSEAsRemote(t *testing.T) {
	Convey("Given an SSE server encoded for OpenCode", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		sse := mcp.Encode(mcp.Server{Transport: mcp.TransportSSE, URL: "https://example.com/sse"})

		key, value, ok := project(t, agent.OpenCode(t.TempDir(), t.TempDir()), kind.MCP, "legacy", sse)

		Convey("When projected", func() {
			Convey("Then SSE collapses to remote HTTP", func() {
				So(ok, ShouldBeTrue)
				So(key, ShouldEqual, "legacy")
				So(server(t, value).Transport, ShouldEqual, mcp.TransportHTTP)
			})
		})
	})
}

func TestGeminiMCPTransports(t *testing.T) {
	Convey("Given a Gemini settings file with three transports", t, func() {
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

		Convey("When it is read and rewritten unchanged", func() {
			So(server(t, snap.Items["stdio-srv"]).Transport, ShouldEqual, mcp.TransportStdio)
			So(server(t, snap.Items["http-srv"]).Transport, ShouldEqual, mcp.TransportHTTP)
			So(server(t, snap.Items["sse-srv"]).Transport, ShouldEqual, mcp.TransportSSE)

			So(surfaceOf(t, a, kind.MCP).Write(t.Context(), snap.Items), ShouldBeNil)
			So(readFile(t, path), ShouldEqual, original)

			Convey("And moving the SSE url keeps it SSE", func() {
				moved := snap.Items["sse-srv"]
				sse := server(t, moved)
				sse.URL = "https://example.com/sse2"
				snap.Items["sse-srv"] = mcp.Encode(sse)

				So(surfaceOf(t, a, kind.MCP).Write(t.Context(), snap.Items), ShouldBeNil)

				text := readFile(t, path)
				So(text, ShouldContainSubstring, "sse2")
				So(text, ShouldContainSubstring, `"trust": true`)
				So(strings.Count(text, `"httpUrl"`), ShouldEqual, 1)
				So(server(t, snapshot(t, a, kind.MCP).Items["sse-srv"]).Transport, ShouldEqual, mcp.TransportSSE)
			})
		})
	})
}

func TestCursorMCPRoundTrip(t *testing.T) {
	Convey("Given a Cursor mcp.json", t, func() {
		home := t.TempDir()
		path := filepath.Join(home, ".cursor", "mcp.json")
		original := `{"mcpServers": {"local-tool": {"command": "npx", "args": ["-y", "pkg"], "envFile": ".env"}, "remote-tool": {"url": "https://example.com/mcp", "auth": {"CLIENT_ID": "id"}}}}`
		writeFile(t, path, original)

		a := agent.Cursor(home, t.TempDir())
		snap := snapshot(t, a, kind.MCP)

		Convey("When a server is changed", func() {
			So(server(t, snap.Items["local-tool"]).Transport, ShouldEqual, mcp.TransportStdio)
			So(server(t, snap.Items["remote-tool"]).Transport, ShouldEqual, mcp.TransportHTTP)
			So(a.Surface(kind.Rules), ShouldBeNil)

			changed := server(t, snap.Items["local-tool"])
			changed.Command = []string{"npx", "-y", "pkg@2"}
			snap.Items["local-tool"] = mcp.Encode(changed)

			So(surfaceOf(t, a, kind.MCP).Write(t.Context(), snap.Items), ShouldBeNil)

			text := readFile(t, path)

			Convey("Then foreign fields survive", func() {
				So(text, ShouldContainSubstring, `"envFile":".env"`)
				So(text, ShouldContainSubstring, `"auth": {"CLIENT_ID": "id"}`)
				So(text, ShouldContainSubstring, "pkg@2")
			})
		})
	})
}

func TestEnvRefTranslation(t *testing.T) {
	Convey("Given a table of agent env-ref syntaxes", t, func() {
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
				agent:  agent.ClaudeCode,
				syntax: "${SECRET}",
			},
			"cursor": {
				file: func(home string) (string, string) {
					return filepath.Join(home, ".cursor", "mcp.json"), `{"mcpServers": {"s": {"type": "stdio", "command": "x", "env": {"TOKEN": "${env:SECRET}"}}}}`
				},
				agent:  func(home, _ string) *agent.Agent { return agent.Cursor(home, t.TempDir()) },
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
			Convey("When translating the "+name+" syntax", func() {
				home := t.TempDir()
				path, content := tt.file(home)
				writeFile(t, path, content)

				a := tt.agent(home, t.TempDir())
				snap := snapshot(t, a, kind.MCP)
				So(server(t, snap.Items["s"]).Env["TOKEN"], ShouldEqual, "{env:SECRET}")

				snap.Items["n"] = mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"y"}, Env: map[string]string{"KEY": "{env:SECRET}"}})
				So(surfaceOf(t, a, kind.MCP).Write(t.Context(), snap.Items), ShouldBeNil)

				Convey("Then the canonical ref renders in the agent syntax", func() {
					So(readFile(t, path), ShouldContainSubstring, `"KEY":"`+tt.syntax+`"`)
				})
			})
		}
	})
}

func TestClaudePermissions(t *testing.T) {
	Convey("Given a Claude settings file with permissions", t, func() {
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

		a := agent.ClaudeCode(home, t.TempDir())

		snap := snapshot(t, a, kind.Permissions)

		Convey("When it is read and rewritten", func() {
			So(snap.Items, ShouldResemble, kind.Items{
				"tool:read":               []byte("allow"),
				"mcp:obsidian:write_note": []byte("allow"),
				"bash:git commit*":        []byte("ask"),
			})

			desired := kind.Items{
				"tool:read":        []byte("allow"),
				"bash:git commit*": []byte("deny"),
				"tool:task":        []byte("allow"),
			}

			So(surfaceOf(t, a, kind.Permissions).Write(t.Context(), desired), ShouldBeNil)

			text := readFile(t, path)
			So(text, ShouldContainSubstring, `"defaultMode": "auto"`)
			So(text, ShouldNotContainSubstring, "mcp__obsidian__write_note")

			var settings struct {
				Permissions map[string][]string `json:"permissions"`
			}

			So(json.Unmarshal([]byte(strings.Replace(text, `"defaultMode": "auto",`, "", 1)), &settings), ShouldBeNil)

			key, _, ok := project(t, a, kind.Permissions, "bash:git commit *", []byte("ask"))

			Convey("Then path specs stay local and prefixes normalize", func() {
				So(settings.Permissions["allow"], ShouldResemble, []string{"Read", "Edit(~/topscan/**)", "Task"})
				So(settings.Permissions["deny"], ShouldResemble, []string{"Bash(git commit:*)"})
				So(settings.Permissions["ask"], ShouldBeEmpty)
				So(desired.Equal(snapshot(t, a, kind.Permissions).Items), ShouldBeTrue)

				So(ok, ShouldBeTrue)
				So(key, ShouldEqual, "bash:git commit*")
			})
		})
	})
}

func TestOpenCodePermissions(t *testing.T) {
	Convey("Given an OpenCode config with permissions and comments", t, func() {
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

		Convey("When it is read and rewritten", func() {
			So(snap.Items, ShouldResemble, kind.Items{
				"tool:read":        []byte("allow"),
				"bash:git commit*": []byte("ask"),
				"mcp:codegraph:*":  []byte("allow"),
			})

			desired := kind.Items{
				"bash:git commit*": []byte("ask"),
				"mcp:codegraph:*":  []byte("allow"),
				"bash:npm test":    []byte("allow"),
			}

			So(surfaceOf(t, a, kind.Permissions).Write(t.Context(), desired), ShouldBeNil)

			text := readFile(t, path)

			_, _, ok := project(t, a, kind.Permissions, "mcp:Bad_Server:x", []byte("allow"))

			Convey("Then local defaults survive and unsplittable servers are hidden", func() {
				So(text, ShouldContainSubstring, "// permissions")
				So(text, ShouldContainSubstring, `"*": "allow"`)
				So(text, ShouldContainSubstring, `"external_directory": "allow"`)
				So(text, ShouldContainSubstring, `"npm test":"allow"`)
				So(text, ShouldNotContainSubstring, `"read"`)

				So(desired.Equal(snapshot(t, a, kind.Permissions).Items), ShouldBeTrue)

				So(ok, ShouldBeFalse)
			})
		})
	})
}

func TestGeminiPermissions(t *testing.T) {
	Convey("Given a Gemini settings file with tools", t, func() {
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

		Convey("When it is read and rewritten", func() {
			So(snap.Items, ShouldResemble, kind.Items{"bash:git status": []byte("allow"), "bash:rm -rf": []byte("deny")})

			desired := kind.Items{"bash:git status": []byte("allow"), "bash:npm test": []byte("ask")}

			So(surfaceOf(t, a, kind.Permissions).Write(t.Context(), desired), ShouldBeNil)

			text := readFile(t, path)

			_, _, ok := project(t, a, kind.Permissions, "tool:read", []byte("allow"))

			Convey("Then untouched tool keys survive and tool names stay unportable", func() {
				So(text, ShouldContainSubstring, `"run_shell_command(npm test)"`)
				So(text, ShouldContainSubstring, `"ReadFileTool"`)
				So(text, ShouldContainSubstring, `"core": ["ReadFileTool"]`)
				So(text, ShouldNotContainSubstring, "rm -rf")

				So(ok, ShouldBeFalse)
			})
		})
	})
}

func TestCursorPermissions(t *testing.T) {
	Convey("Given a Cursor cli-config with permissions", t, func() {
		home := t.TempDir()
		path := filepath.Join(home, ".cursor", "cli-config.json")
		writeFile(t, path, `{
  "approvalMode": "allowlist",
  "permissions": {
    "allow": ["Shell(ls)", "Read(.env*)"],
    "deny": ["Mcp(dangerous:run)"]
  }
}`)

		a := agent.Cursor(home, t.TempDir())

		snap := snapshot(t, a, kind.Permissions)

		Convey("When it is read and rewritten", func() {
			So(snap.Items, ShouldResemble, kind.Items{"bash:ls": []byte("allow"), "mcp:dangerous:run": []byte("deny")})

			So(surfaceOf(t, a, kind.Permissions).Write(t.Context(), kind.Items{
				"bash:ls":           []byte("allow"),
				"mcp:dangerous:run": []byte("deny"),
				"bash:make":         []byte("allow"),
			}), ShouldBeNil)

			text := readFile(t, path)

			_, _, ok := project(t, a, kind.Permissions, "bash:test", []byte("ask"))

			Convey("Then untouched rules survive and ask is unportable", func() {
				So(text, ShouldContainSubstring, `"Shell(make)"`)
				So(text, ShouldContainSubstring, `"Read(.env*)"`)
				So(text, ShouldContainSubstring, `"approvalMode": "allowlist"`)
				So(ok, ShouldBeFalse)
			})
		})
	})
}

func TestRulesWriteThroughSymlink(t *testing.T) {
	Convey("Given a symlinked rules file", t, func() {
		home := t.TempDir()
		target := filepath.Join(home, "dotfiles", "CLAUDE.md")
		link := filepath.Join(home, ".claude", "CLAUDE.md")

		writeFile(t, target, "# v1\n")
		So(os.MkdirAll(filepath.Dir(link), 0o750), ShouldBeNil)
		So(os.Symlink(target, link), ShouldBeNil)

		a := agent.ClaudeCode(home, t.TempDir())
		So(string(snapshot(t, a, kind.Rules).Items[kind.RulesKey]), ShouldEqual, "# v1\n")

		Convey("When the rules are written", func() {
			So(surfaceOf(t, a, kind.Rules).Write(t.Context(), kind.Items{kind.RulesKey: []byte("# v2\n")}), ShouldBeNil)

			info, err := os.Lstat(link)

			Convey("Then the symlink stays and the target is updated", func() {
				So(err, ShouldBeNil)
				So(info.Mode()&os.ModeSymlink, ShouldNotBeZeroValue)
				So(readFile(t, target), ShouldEqual, "# v2\n")
			})
		})
	})
}

func TestRulesAreNeverDeleted(t *testing.T) {
	Convey("Given an existing rules file and empty desired items", t, func() {
		home := t.TempDir()
		path := filepath.Join(home, ".claude", "CLAUDE.md")
		writeFile(t, path, "# keep\n")

		Convey("When the surface writes", func() {
			So(surfaceOf(t, agent.ClaudeCode(home, t.TempDir()), kind.Rules).Write(t.Context(), kind.Items{}), ShouldBeNil)

			Convey("Then the file is kept", func() {
				So(readFile(t, path), ShouldEqual, "# keep\n")
			})
		})
	})
}

func TestSkillsSurface(t *testing.T) {
	Convey("Given a skills directory with real, junk, linked and plugin skills", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".claude", "skills")

		writeFile(t, filepath.Join(dir, "real", "SKILL.md"), "real\n")
		writeFile(t, filepath.Join(dir, "real", ".DS_Store"), "junk")
		writeFile(t, filepath.Join(dir, "gone", "SKILL.md"), "gone\n")
		writeFile(t, filepath.Join(dir, "Invalid_Name", "SKILL.md"), "ignored\n")

		external := filepath.Join(home, "skills-src", "linked")
		writeFile(t, filepath.Join(external, "SKILL.md"), "linked\n")
		So(os.Symlink(external, filepath.Join(dir, "linked")), ShouldBeNil)

		plugin := filepath.Join(home, ".claude", "plugins", "cache", "m", "p", "1.0", "skills", "plugged")
		writeFile(t, filepath.Join(plugin, "SKILL.md"), "plugged\n")
		So(os.Symlink(plugin, filepath.Join(dir, "plugged")), ShouldBeNil)

		a := agent.ClaudeCode(home, t.TempDir())
		snap := snapshot(t, a, kind.Skills)

		Convey("When it is read and written", func() {
			So(snap.Items, ShouldResemble, kind.Items{
				"real/SKILL.md":   []byte("real\n"),
				"gone/SKILL.md":   []byte("gone\n"),
				"linked/SKILL.md": []byte("linked\n"),
			})
			So(snap.ReadOnly, ShouldContainKey, "linked")

			So(surfaceOf(t, a, kind.Skills).Write(t.Context(), kind.Items{
				"real/SKILL.md":   []byte("real v2\n"),
				"linked/SKILL.md": []byte("never written\n"),
				"new/SKILL.md":    []byte("new\n"),
			}), ShouldBeNil)

			Convey("Then updates apply, junk and links are protected and deletions work", func() {
				So(readFile(t, filepath.Join(dir, "real", "SKILL.md")), ShouldEqual, "real v2\n")

				_, junkErr := os.Stat(filepath.Join(dir, "real", ".DS_Store"))
				So(junkErr, ShouldBeNil)

				_, goneErr := os.Stat(filepath.Join(dir, "gone"))
				So(goneErr, ShouldNotBeNil)

				So(readFile(t, filepath.Join(dir, "new", "SKILL.md")), ShouldEqual, "new\n")
				So(readFile(t, filepath.Join(external, "SKILL.md")), ShouldEqual, "linked\n")
				So(readFile(t, filepath.Join(plugin, "SKILL.md")), ShouldEqual, "plugged\n")
			})
		})
	})
}

func TestDetect(t *testing.T) {
	Convey("Given a home without agent directories", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()

		detected, err := agent.ClaudeCode(home, t.TempDir()).Detect()
		So(err, ShouldBeNil)
		So(detected, ShouldBeFalse)

		So(os.MkdirAll(filepath.Join(home, ".claude"), 0o750), ShouldBeNil)

		Convey("When the agent directory appears", func() {
			detected, err := agent.ClaudeCode(home, t.TempDir()).Detect()
			So(err, ShouldBeNil)

			openCode, err := agent.OpenCode(home, t.TempDir()).Detect()
			So(err, ShouldBeNil)

			Convey("Then Claude is detected and OpenCode is not", func() {
				So(detected, ShouldBeTrue)
				So(openCode, ShouldBeFalse)
			})
		})
	})
}

func TestClaudeProjectScope(t *testing.T) {
	Convey("Given a project directory", t, func() {
		dir := t.TempDir()

		items, present, err := agent.ClaudeProjectMCP(t.Context(), dir)
		So(err, ShouldBeNil)

		Convey("When a project .mcp.json exists", func() {
			So(present, ShouldBeFalse)
			So(items, ShouldBeEmpty)

			writeFile(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers": {"repo": {"type": "stdio", "command": "x"}}}`)

			items, present, err = agent.ClaudeProjectMCP(t.Context(), dir)
			So(err, ShouldBeNil)

			Convey("Then it is read, and broken JSON errors", func() {
				So(present, ShouldBeTrue)
				So(items, ShouldContainKey, "repo")

				writeFile(t, filepath.Join(dir, ".mcp.json"), `{`)

				_, _, err = agent.ClaudeProjectMCP(t.Context(), dir)
				So(err, ShouldBeError)
			})
		})

		Convey("When local MCP is looked up by recorded path", func() {
			home := t.TempDir()
			writeFile(t, filepath.Join(home, ".claude.json"), `{"projects": {"/Users/x/proj": {"mcpServers": {"local": {"type": "stdio", "command": "y"}}}}}`)

			items, present, err = agent.ClaudeLocalMCP(home, "/Users/x/proj")
			So(err, ShouldBeNil)

			Convey("Then only the exact path matches", func() {
				So(present, ShouldBeTrue)
				So(items, ShouldContainKey, "local")

				_, present, err = agent.ClaudeLocalMCP(home, "/Users/x/other")
				So(err, ShouldBeNil)
				So(present, ShouldBeFalse)
			})
		})
	})
}

func TestReloadHints(t *testing.T) {
	Convey("Given a table of agents and kinds", t, func() {
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
			Convey("When checking the "+tt.name+" hint", func() {
				t.Setenv("XDG_CONFIG_HOME", "")

				Convey("Then it matches", func() {
					So(surfaceOf(t, tt.a(), tt.k).Traits().ReloadHint, ShouldEqual, tt.want)
				})
			})
		}
	})
}

func TestReadableSkills(t *testing.T) {
	Convey("Given a home with per-agent skill directories", t, func() {
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

		Convey("When opencode reads skills", func() {
			t.Setenv("XDG_CONFIG_HOME", "")
			So(readableDirs(readableRefs(t, agent.OpenCode(home, cwd))), ShouldResemble, []string{ocOwn, claudeOwn, sharedOwn})
		})

		Convey("When cursor reads skills", func() {
			t.Setenv("XDG_CONFIG_HOME", "")
			So(readableDirs(readableRefs(t, agent.Cursor(home, t.TempDir()))), ShouldResemble, []string{cursorOwn, claudeOwn, sharedOwn})
		})

		Convey("When gemini reads skills", func() {
			t.Setenv("XDG_CONFIG_HOME", "")
			So(readableDirs(readableRefs(t, agent.GeminiCLI(home, cwd))), ShouldResemble, []string{geminiOwn, sharedOwn})
		})

		Convey("When claude reads skills", func() {
			t.Setenv("XDG_CONFIG_HOME", "")
			So(readableDirs(readableRefs(t, agent.ClaudeCode(home, t.TempDir()))), ShouldResemble, []string{claudeOwn})
		})

		Convey("When shared reads skills", func() {
			t.Setenv("XDG_CONFIG_HOME", "")
			So(readableDirs(readableRefs(t, agent.SharedSkills(home))), ShouldResemble, []string{sharedOwn})
		})

		Convey("When XDG_CONFIG_HOME points elsewhere", func() {
			xdg := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", xdg)

			writeFile(t, filepath.Join(xdg, "opencode", "skills", "xdg-own", "SKILL.md"), "# xdg\n")

			So(readableDirs(readableRefs(t, agent.OpenCode(home, cwd))), ShouldResemble, []string{filepath.Join(xdg, "opencode", "skills"), claudeOwn, sharedOwn})
		})
	})
}

func TestReadableSkillsGates(t *testing.T) {
	Convey("Given a skills directory with links, plugin links and a stub", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		cwd := t.TempDir()

		skills := filepath.Join(home, ".config", "opencode", "skills")

		writeFile(t, filepath.Join(skills, "own", "SKILL.md"), "# own\n")

		linked := filepath.Join(home, "elsewhere", "linked")
		writeFile(t, filepath.Join(linked, "SKILL.md"), "# linked\n")
		So(os.Symlink(linked, filepath.Join(skills, "linked")), ShouldBeNil)

		plugged := filepath.Join(home, ".claude", "plugins", "cache", "acme", "tool", "1.0.0", "skills", "plugged")
		writeFile(t, filepath.Join(plugged, "SKILL.md"), "# plugged\n")
		So(os.Symlink(plugged, filepath.Join(skills, "plugged")), ShouldBeNil)

		writeFile(t, filepath.Join(skills, "stub", "SKILL.md"), "# stub\n")
		writeFile(t, filepath.Join(skills, "stub", ".beadle-quarantine"), "v1 acme/tool x 2026-01-01T00:00:00Z\n")

		byName := map[string]string{}
		for _, ref := range readableRefs(t, agent.OpenCode(home, cwd)) {
			byName[ref.Name] = ref.Root
		}

		Convey("When skills are listed", func() {
			Convey("Then links resolve, plugin links and stubs are skipped and missing dirs are fine", func() {
				So(byName["own"], ShouldEqual, filepath.Join(skills, "own"))
				So(byName["linked"], ShouldEqual, agent.RealPath(linked))
				So(byName, ShouldNotContainKey, "plugged")
				So(byName, ShouldNotContainKey, "stub")

				So(readableRefs(t, agent.OpenCode(t.TempDir(), cwd)), ShouldBeEmpty)
			})
		})

		Convey("When the skills directory is unreadable", func() {
			So(os.Chmod(skills, 0o000), ShouldBeNil)
			t.Cleanup(func() { _ = os.Chmod(skills, 0o750) }) //nolint:gosec // G302: restoring the fixture directory mode

			reader, ok := surfaceOf(t, agent.OpenCode(home, cwd), kind.Skills).(agent.SkillReader)
			So(ok, ShouldBeTrue)

			_, err := reader.ReadableSkills()

			Convey("Then a real read error is reported", func() {
				So(err, ShouldBeError)
			})
		})
	})
}

func TestNewAgentDetect(t *testing.T) {
	Convey("Given a table of new agents and their directories", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		cwd := t.TempDir()

		cases := []struct {
			name string
			a    *agent.Agent
			dir  string
		}{
			{"codex", agent.Codex(home, cwd), filepath.Join(home, ".codex")},
			{"pi", agent.Pi(home, cwd), filepath.Join(home, ".pi", "agent")},
			{"kilo", agent.Kilo(home, cwd), filepath.Join(home, ".config", "kilo")},
		}

		for _, tc := range cases {
			Convey("When the "+tc.name+" directory is absent", func() {
				detected, err := tc.a.Detect()
				So(err, ShouldBeNil)
				So(detected, ShouldBeFalse)

				So(os.MkdirAll(tc.dir, 0o750), ShouldBeNil)

				detected, err = tc.a.Detect()

				Convey("Then it is detected once present", func() {
					So(err, ShouldBeNil)
					So(detected, ShouldBeTrue)
				})
			})
		}
	})
}

func TestNewAgentsRegistered(t *testing.T) {
	Convey("Given the full agent registry", t, func() {
		all := agent.All(t.TempDir(), t.TempDir())

		Convey("When looking up the new agents", func() {
			Convey("Then all three are registered", func() {
				for _, id := range []string{agent.CodexID, agent.PiID, agent.KiloID} {
					So(agent.ByID(all, id), ShouldNotBeNil)
				}
			})
		})
	})
}

func TestCodexMCPUnion(t *testing.T) {
	Convey("Given a Codex config with an undecodable foreign server", t, func() {
		home := t.TempDir()
		path := codexMCPFile(home)
		writeFile(t, path, codexConfigFixture)

		a := agent.Codex(home, t.TempDir())

		snap := snapshot(t, a, kind.MCP)

		Convey("When servers are read and rewritten", func() {
			So(server(t, snap.Items["owned"]).Command, ShouldResemble, []string{"old", "serve"})

			desired := kind.Items{
				"owned": stdioServer("npx", "-y", "beadle"),
				"fresh": stdioServer("fresh"),
			}

			So(surfaceOf(t, a, kind.MCP).Write(t.Context(), desired), ShouldBeNil)

			out := readFile(t, path)
			So(out, ShouldContainSubstring, "enabled = false")
			So(out, ShouldContainSubstring, "tool_timeout_sec = 7")

			back := snapshot(t, a, kind.MCP)

			Convey("Then the union round-trips", func() {
				So(desired.Equal(back.Items), ShouldBeTrue)
			})
		})
	})
}

func TestCodexRules(t *testing.T) {
	Convey("Given a Codex home directory", t, func() {
		home := t.TempDir()
		a := agent.Codex(home, t.TempDir())

		Convey("When rules are written", func() {
			So(os.MkdirAll(filepath.Join(home, ".codex"), 0o750), ShouldBeNil)
			So(surfaceOf(t, a, kind.Rules).Write(t.Context(), kind.Items{kind.RulesKey: []byte("# codex rules\n")}), ShouldBeNil)

			snap := snapshot(t, a, kind.Rules)

			Convey("Then they read back", func() {
				So(snap.Items[kind.RulesKey], ShouldResemble, []byte("# codex rules\n"))
			})
		})
	})
}

func TestNewAgentSkillsAlsoReads(t *testing.T) {
	Convey("Given skill directories for Codex, Pi, Kilo and shared", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		cwd := t.TempDir()

		codexOwn := filepath.Join(home, ".codex", "skills")
		piOwn := filepath.Join(home, ".pi", "agent", "skills")
		kiloOwn := filepath.Join(home, ".kilo", "skills")
		sharedOwn := filepath.Join(home, ".agents", "skills")

		writeFile(t, filepath.Join(codexOwn, "codex-own", "SKILL.md"), "# codex\n")
		writeFile(t, filepath.Join(piOwn, "pi-own", "SKILL.md"), "# pi\n")
		writeFile(t, filepath.Join(kiloOwn, "kilo-own", "SKILL.md"), "# kilo\n")
		writeFile(t, filepath.Join(sharedOwn, "shared-own", "SKILL.md"), "# shared\n")

		cases := []struct {
			name string
			a    *agent.Agent
			own  string
		}{
			{"codex", agent.Codex(home, cwd), codexOwn},
			{"pi", agent.Pi(home, cwd), piOwn},
			{"kilo", agent.Kilo(home, cwd), kiloOwn},
		}

		for _, tc := range cases {
			Convey("When "+tc.name+" reads skills", func() {
				refs := readableRefs(t, tc.a)
				mode := surfaceOf(t, tc.a, kind.Skills).Traits().DefaultMode
				snap := snapshot(t, tc.a, kind.Skills)

				Convey("Then its own and the shared dir are readable and the mode is pull", func() {
					So(readableDirs(refs), ShouldResemble, []string{tc.own, sharedOwn})
					So(mode, ShouldEqual, config.ModePull)
					So(snap.Items, ShouldNotContainKey, "shared-own/SKILL.md")
				})
			})
		}
	})
}

func TestPiMCPRoundTrip(t *testing.T) {
	Convey("Given a Pi mcp.json with foreign fields", t, func() {
		const fixture = `{
  "mcpServers": {
    "kite": {
      "command": "npx",
      "args": ["-y", "kite"],
      "env": {"K": "V"},
      "cwd": "~/work",
      "lifecycle": "lazy",
      "directTools": ["read"],
      "idleTimeout": 30,
      "auth": {"type": "bearer"}
    },
    "kite-remote": {
      "url": "https://example.com/mcp",
      "headers": {"Authorization": "Bearer y"},
      "transport": "streamable-http",
      "excludeTools": ["danger"]
    }
  }
}
`

		home := t.TempDir()
		path := filepath.Join(home, ".pi", "agent", "mcp.json")
		writeFile(t, path, fixture)

		a := agent.Pi(home, t.TempDir())

		snap := snapshot(t, a, kind.MCP)

		Convey("When servers are read and rewritten", func() {
			So(server(t, snap.Items["kite"]).Command, ShouldResemble, []string{"npx", "-y", "kite"})
			So(server(t, snap.Items["kite-remote"]).Transport, ShouldEqual, mcp.TransportHTTP)

			desired := kind.Items{
				"kite":        mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"kite", "--serve"}, Env: map[string]string{"K": "V2"}}),
				"kite-remote": mcp.Encode(mcp.Server{Transport: mcp.TransportHTTP, URL: "https://example.com/v2", Headers: map[string]string{"Authorization": "Bearer z"}}),
			}

			So(surfaceOf(t, a, kind.MCP).Write(t.Context(), desired), ShouldBeNil)

			out := readFile(t, path)

			Convey("Then foreign fields survive and the union round-trips", func() {
				So(out, ShouldContainSubstring, `"lifecycle":"lazy"`)
				So(out, ShouldContainSubstring, `"directTools":["read"]`)
				So(out, ShouldContainSubstring, `"idleTimeout":30`)
				So(out, ShouldContainSubstring, `"auth":{"type":"bearer"}`)
				So(out, ShouldContainSubstring, `"excludeTools":["danger"]`)
				So(out, ShouldContainSubstring, `"cwd":"~/work"`)
				So(out, ShouldContainSubstring, `"transport":"streamable-http"`)

				back := snapshot(t, a, kind.MCP)
				So(desired.Equal(back.Items), ShouldBeTrue)
			})
		})
	})
}

func TestKiloMCPAndPermissions(t *testing.T) {
	Convey("Given a Kilo config with mcp and permission", t, func() {
		const fixture = `{
  // kilo config, keep this comment
  "$schema": "https://kilo.ai/config.json",
  "mcp": {
    "local-srv": {"type": "local", "command": ["npx", "-y", "pkg"], "environment": {"K": "V"}, "enabled": true},
    "remote-srv": {"type": "remote", "url": "https://example.com/mcp", "headers": {"Authorization": "Bearer y"}}
  },
  "permission": {"bash": {"*": "allow"}, "edit": "deny"},
  "instructions": ["AGENTS.md"]
}
`

		home := t.TempDir()
		path := filepath.Join(home, ".config", "kilo", "kilo.jsonc")
		writeFile(t, path, fixture)

		a := agent.Kilo(home, t.TempDir())

		snap := snapshot(t, a, kind.MCP)

		Convey("When mcp and permissions are read and rewritten", func() {
			So(snap.Items, ShouldHaveLength, 2)
			So(server(t, snap.Items["local-srv"]).Command, ShouldResemble, []string{"npx", "-y", "pkg"})

			perms := snapshot(t, a, kind.Permissions)
			So(perms.Items["tool:edit"], ShouldResemble, []byte("deny"))

			So(surfaceOf(t, a, kind.MCP).Write(t.Context(), kind.Items{
				"local-srv": stdioServer("changed"),
				"added":     stdioServer("added"),
			}), ShouldBeNil)

			So(surfaceOf(t, a, kind.Permissions).Write(t.Context(), kind.Items{"tool:edit": []byte("ask")}), ShouldBeNil)

			out := readFile(t, path)

			Convey("Then the comment and foreign keys survive", func() {
				So(out, ShouldContainSubstring, "// kilo config, keep this comment")
				So(out, ShouldContainSubstring, `"instructions": ["AGENTS.md"]`)
				So(out, ShouldContainSubstring, `"bash": {"*": "allow"}`)
				So(out, ShouldContainSubstring, `"edit": "ask"`)
				So(out, ShouldNotContainSubstring, "remote-srv")

				backMCP := snapshot(t, a, kind.MCP)
				So(server(t, backMCP.Items["local-srv"]).Command, ShouldResemble, []string{"changed"})
				So(backMCP.Items, ShouldHaveLength, 2)

				backPerms := snapshot(t, a, kind.Permissions)
				So(backPerms.Items["tool:edit"], ShouldResemble, []byte("ask"))
			})
		})
	})
}

func TestInboxPaths(t *testing.T) {
	Convey("Given a home directory", t, func() {
		home := t.TempDir()

		Convey("When inbox paths are resolved", func() {
			Convey("Then the new hosts get their own inboxes", func() {
				So(agent.InboxPath(home, agent.CodexID), ShouldEqual, filepath.Join(home, ".codex", "inbox.md"))
				So(agent.InboxPath(home, agent.PiID), ShouldEqual, filepath.Join(home, ".pi", "agent", "inbox.md"))
				So(agent.InboxPath(home, agent.KiloID), ShouldEqual, filepath.Join(home, ".config", "kilo", "inbox.md"))
				So(agent.InboxPath(home, agent.ClaudeCodeID), ShouldBeEmpty)
			})
		})
	})
}
