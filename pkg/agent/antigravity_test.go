package agent_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

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

	return surfaceOf(t, agent.AntigravityCLI(home, t.TempDir()), kind.MCP)
}

func antigravityDoc(t *testing.T, home string) map[string]any {
	t.Helper()

	var doc map[string]any

	if err := json.Unmarshal([]byte(readFile(t, antigravityConfig(home))), &doc); err != nil {
		t.Fatalf("unmarshal antigravity config: %v", err)
	}

	return doc
}

func antigravityEntry(t *testing.T, home, name string) map[string]any {
	t.Helper()

	servers, ok := antigravityDoc(t, home)["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("no mcpServers object")
	}

	entry, ok := servers[name].(map[string]any)
	if !ok {
		t.Fatalf("no %s entry", name)
	}

	return entry
}

func antigravityEnv(t *testing.T, home, name string) map[string]any {
	t.Helper()

	env, ok := antigravityEntry(t, home, name)["env"].(map[string]any)
	if !ok {
		t.Fatal("no env object")
	}

	return env
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	return data
}

func TestAntigravityMCPStdioRoundTrip(t *testing.T) {
	Convey("Given an empty Antigravity mcp config", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		writeFile(t, antigravityConfig(home), `{"mcpServers": {}}`)

		surface := antigravitySurface(t, home)

		Convey("When a stdio server is written", func() {
			So(surface.Write(t.Context(), kind.Items{
				"alpha": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"npx", "-y", "alpha"}}),
			}), ShouldBeNil)

			entry := antigravityEntry(t, home, "alpha")

			Convey("Then it uses command/args and no legacy url keys", func() {
				So(entry["command"], ShouldEqual, "npx")
				So(entry["args"], ShouldResemble, []any{"-y", "alpha"})

				for _, legacy := range []string{"url", "httpUrl", "serverUrl"} {
					So(entry, ShouldNotContainKey, legacy)
				}

				snap := snapshot(t, agent.AntigravityCLI(home, t.TempDir()), kind.MCP)
				So(server(t, snap.Items["alpha"]).Command, ShouldResemble, []string{"npx", "-y", "alpha"})
			})
		})
	})
}

func TestAntigravityMCPRemoteUsesServerURL(t *testing.T) {
	Convey("Given an empty Antigravity mcp config", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		writeFile(t, antigravityConfig(home), `{"mcpServers": {}}`)

		Convey("When a remote server is written", func() {
			So(antigravitySurface(t, home).Write(t.Context(), kind.Items{
				"gamma": mcp.Encode(mcp.Server{
					Transport: mcp.TransportHTTP,
					URL:       "https://gamma.example.com/mcp",
					Headers:   map[string]string{"Authorization": "Bearer token"},
				}),
			}), ShouldBeNil)

			entry := antigravityEntry(t, home, "gamma")

			Convey("Then it uses serverUrl and keeps headers", func() {
				So(entry["serverUrl"], ShouldEqual, "https://gamma.example.com/mcp")
				So(entry, ShouldNotContainKey, "url")
				So(entry, ShouldNotContainKey, "httpUrl")
				So(entry, ShouldContainKey, "headers")

				snap := snapshot(t, agent.AntigravityCLI(home, t.TempDir()), kind.MCP)
				got := server(t, snap.Items["gamma"])

				So(got.URL, ShouldEqual, "https://gamma.example.com/mcp")
				So(got.Headers["Authorization"], ShouldEqual, "Bearer token")
			})
		})
	})
}

func TestAntigravityStripsLegacyKeysKeepsAgentOnly(t *testing.T) {
	Convey("Given an entry with legacy url keys and agent-only fields", t, func() {
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

		Convey("When the server is rewritten", func() {
			So(antigravitySurface(t, home).Write(t.Context(), kind.Items{
				"alpha": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"new"}}),
			}), ShouldBeNil)

			entry := antigravityEntry(t, home, "alpha")

			Convey("Then legacy keys drop and agent-only fields survive", func() {
				So(entry["command"], ShouldEqual, "new")
				So(string(mustJSON(t, entry)), ShouldNotContainSubstring, "legacy")
				So(entry, ShouldNotContainKey, "url")
				So(entry, ShouldNotContainKey, "httpUrl")

				So(entry["cwd"], ShouldEqual, "/work")
				So(entry["disabled"], ShouldEqual, true)
				So(entry["disabledTools"], ShouldResemble, []any{"x"})
				So(entry["authProviderType"], ShouldEqual, "oauth")
			})
		})
	})
}

func TestAntigravityEnvRefsFollowGemini(t *testing.T) {
	Convey("Given an empty Antigravity mcp config", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		writeFile(t, antigravityConfig(home), `{"mcpServers": {}}`)

		Convey("When a server with a canonical env ref is written", func() {
			So(antigravitySurface(t, home).Write(t.Context(), kind.Items{
				"alpha": mcp.Encode(mcp.Server{
					Transport: mcp.TransportStdio,
					Command:   []string{"a"},
					Env:       map[string]string{"TOKEN": "{env:SECRET}"}, //nolint:gosec // G101: a canonical reference, not a credential
				}),
			}), ShouldBeNil)

			Convey("Then it renders the Gemini syntax and reads back canonically", func() {
				So(antigravityEnv(t, home, "alpha")["TOKEN"], ShouldEqual, "${SECRET}")

				snap := snapshot(t, agent.AntigravityCLI(home, t.TempDir()), kind.MCP)
				So(server(t, snap.Items["alpha"]).Env["TOKEN"], ShouldEqual, "{env:SECRET}")
			})
		})
	})
}

func TestAntigravityDetect(t *testing.T) {
	Convey("Given a pure Gemini install", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		pureGemini := t.TempDir()
		writeFile(t, filepath.Join(pureGemini, ".gemini", "settings.json"), `{"mcpServers": {}}`)
		writeFile(t, filepath.Join(pureGemini, ".gemini", "GEMINI.md"), "# rules\n")
		writeFile(t, filepath.Join(pureGemini, ".gemini", "skills", "alpha", "SKILL.md"), "# alpha\n")

		Convey("When detection runs", func() {
			detected, err := agent.AntigravityCLI(pureGemini, t.TempDir()).Detect()
			So(err, ShouldBeNil)

			_, err = agent.AntigravityCLI(t.TempDir(), t.TempDir()).Detect()
			So(err, ShouldBeNil)

			Convey("Then it stays inactive", func() {
				So(detected, ShouldBeFalse)
			})
		})
	})

	Convey("Given a home with the antigravity state directory", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		stateHome := t.TempDir()
		So(os.MkdirAll(filepath.Join(stateHome, ".gemini", "antigravity-cli"), 0o750), ShouldBeNil)

		Convey("When detection runs", func() {
			detected, err := agent.AntigravityCLI(stateHome, t.TempDir()).Detect()

			Convey("Then it is installed", func() {
				So(err, ShouldBeNil)
				So(detected, ShouldBeTrue)
			})
		})
	})

	Convey("Given a home with only the shared mcp config", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		configHome := t.TempDir()
		writeFile(t, antigravityConfig(configHome), `{"mcpServers": {}}`)

		Convey("When detection runs", func() {
			detected, err := agent.AntigravityCLI(configHome, t.TempDir()).Detect()

			Convey("Then it is detected even without the CLI", func() {
				So(err, ShouldBeNil)
				So(detected, ShouldBeTrue)
			})
		})
	})
}

func TestAntigravitySurfaceShape(t *testing.T) {
	Convey("Given an Antigravity agent", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		cwd := t.TempDir()
		a := agent.AntigravityCLI(home, cwd)

		Convey("When its surfaces are inspected", func() {
			projects := a.Surface(kind.Projects)

			Convey("Then MCP and project surfaces exist and the rest do not", func() {
				So(surfaceOf(t, a, kind.MCP).Traits().ReloadHint, ShouldEqual, antigravityReloadHint)
				So(surfaceOf(t, a, kind.MCP).Path(), ShouldEqual, antigravityConfig(home))

				So(projects, ShouldNotBeNil)
				So(projects.Path(), ShouldEqual, filepath.Join(cwd, ".agents", "mcp_config.json"))

				for _, k := range []kind.ID{kind.Rules, kind.Skills, kind.Permissions, kind.Memory} {
					So(a.Surface(k), ShouldBeNil)
				}
			})
		})
	})
}

func TestAntigravityLegacyURLIsNotCanonized(t *testing.T) {
	Convey("Given an entry with only legacy url keys", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		writeFile(t, antigravityConfig(home), `{"mcpServers": {"dead": {"url": "https://dead.example.com/mcp", "httpUrl": "https://dead2.example.com/mcp"}}}`)

		Convey("When it is read", func() {
			snap := snapshot(t, agent.AntigravityCLI(home, t.TempDir()), kind.MCP)

			Convey("Then it is not an item", func() {
				So(snap.Items, ShouldBeEmpty)
			})
		})
	})
}

func TestAntigravityKeepsUnmanagedEntries(t *testing.T) {
	Convey("Given an Antigravity config with an unrelated top-level key", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		writeFile(t, antigravityConfig(home), `{"mcpServers": {}, "unrelated": {"keep": true}}`)

		Convey("When a server is written", func() {
			So(antigravitySurface(t, home).Write(t.Context(), kind.Items{
				"alpha": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"a"}}),
			}), ShouldBeNil)

			var doc map[string]any

			Convey("Then the unrelated key survives", func() {
				So(json.Unmarshal([]byte(readFile(t, antigravityConfig(home))), &doc), ShouldBeNil)
				So(doc, ShouldContainKey, "unrelated")
			})
		})
	})
}
