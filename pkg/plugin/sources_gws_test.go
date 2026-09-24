package plugin_test

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/plugin"
)

func codexCacheDir(home, marketplace, name, version string) string {
	return filepath.Join(home, ".codex", "plugins", "cache", marketplace, name, version)
}

func validPluginDir(t *testing.T, dir, manifest string) {
	t.Helper()

	writeFile(t, filepath.Join(dir, "plugin.json"), manifest)
}

func TestReadAllCodexCache(t *testing.T) {
	Convey("Given a Codex plugin cache", t, func() {
		home := t.TempDir()
		dir := codexCacheDir(home, "acme", "tool", "1.2.0")

		validPluginDir(t, dir, `{"name":"tool","version":"1.2.0","description":"portable plugin"}`)
		writeFile(t, filepath.Join(dir, "skills", "alpha", "SKILL.md"), "# alpha\n")
		writeFile(t, filepath.Join(dir, "agents", "helper.md"), "# helper\n")
		writeFile(t, filepath.Join(dir, "commands", "dev.md"), "# dev\n")
		writeFile(t, filepath.Join(dir, "hooks", "hooks.json"), `{"hooks":{"PreToolUse":[{"matcher":"","hooks":[{"type":"command","command":"x"}]}]}}`)
		writeFile(t, filepath.Join(dir, "mcp.json"), `{"mcpServers":{"fetch":{},"search":{}}}`)

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the plugin and its artifacts are reported", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldHaveLength, 1)

				p := manifest.Plugins[0]
				So(p.Source, ShouldEqual, plugin.SourceCodex)
				So(p.Origin, ShouldEqual, "acme")
				So(p.Name, ShouldEqual, "tool")
				So(p.Version, ShouldEqual, "1.2.0")
				So(p.Description, ShouldEqual, "portable plugin")
				So(p.Scope, ShouldEqual, "user")
				So(p.InstallPath, ShouldEqual, dir)
				So(p.Skills, ShouldResemble, []string{"alpha"})
				So(p.Agents, ShouldResemble, []string{"helper"})
				So(p.Commands, ShouldResemble, []string{"dev"})
				So(p.Hooks, ShouldResemble, []string{"PreToolUse"})
				So(p.MCPServers, ShouldResemble, []string{"fetch", "search"})
				So(manifest.Warnings, ShouldBeEmpty)
			})
		})
	})
}

func TestReadAllCodexLegacyManifest(t *testing.T) {
	Convey("Given a Codex plugin with a legacy manifest", t, func() {
		home := t.TempDir()
		dir := codexCacheDir(home, "acme", "legacy", "local")

		writeFile(t, filepath.Join(dir, ".codex-plugin", "plugin.json"),
			`{"name":"legacy","version":"0.1.0","skills":"./custom/"}`)
		writeFile(t, filepath.Join(dir, "custom", "alpha", "SKILL.md"), "# alpha\n")
		writeFile(t, filepath.Join(dir, "skills", "ignored", "SKILL.md"), "# ignored\n")
		writeFile(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers":{"search":{}}}`)

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the declared skill path replaces the default", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldHaveLength, 1)
				So(manifest.Plugins[0].Skills, ShouldResemble, []string{"alpha"})
				So(manifest.Plugins[0].MCPServers, ShouldResemble, []string{"search"})
				So(manifest.Plugins[0].Version, ShouldEqual, "0.1.0")
			})
		})
	})
}

func TestReadAllCodexPersonalMarketplace(t *testing.T) {
	Convey("Given a Codex personal marketplace", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".codex", "plugins", "my-plugin")

		writeFile(t, filepath.Join(home, ".agents", "plugins", "marketplace.json"),
			`{"name":"personal","plugins":[{"name":"my-plugin","source":{"source":"local","path":"./.codex/plugins/my-plugin"}}]}`)
		validPluginDir(t, dir, `{"name":"my-plugin","version":"0.3.0"}`)
		writeFile(t, filepath.Join(dir, "skills", "alpha", "SKILL.md"), "# alpha\n")

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the marketplace plugin is read from its source path", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldHaveLength, 1)
				So(manifest.Plugins[0].Origin, ShouldEqual, "personal")
				So(manifest.Plugins[0].Name, ShouldEqual, "my-plugin")
				So(manifest.Plugins[0].Version, ShouldEqual, "0.3.0")
				So(manifest.Plugins[0].InstallPath, ShouldEqual, dir)
			})
		})

		Convey("When the marketplace entry is a git source", func() {
			writeFile(t, filepath.Join(home, ".agents", "plugins", "marketplace.json"),
				`{"name":"personal","plugins":[{"name":"remote","source":{"source":"url","url":"https://example.com/plugins.git"}}]}`)

			manifest, err := plugin.ReadAll(home)

			Convey("Then the remote entry is ignored", func() {
				So(err, ShouldBeNil)

				for _, p := range manifest.Plugins {
					So(p.Name, ShouldNotEqual, "remote")
				}
			})
		})

		Convey("When the marketplace path escapes the home directory", func() {
			writeFile(t, filepath.Join(home, ".agents", "plugins", "marketplace.json"),
				`{"name":"personal","plugins":[{"name":"escape","source":"./../../outside"}]}`)

			manifest, err := plugin.ReadAll(home)

			Convey("Then the entry is refused with an honest boundary", func() {
				So(err, ShouldBeNil)

				for _, p := range manifest.Plugins {
					So(p.Name, ShouldNotEqual, "escape")
				}

				So(manifest.Warnings, ShouldHaveLength, 1)
				So(manifest.Warnings[0], ShouldContainSubstring, "escapes the home directory")
				So(manifest.Warnings[0], ShouldNotContainSubstring, "marketplace root")
			})
		})
	})
}

func TestReadAllCodexMarketplaceSkipsCached(t *testing.T) {
	Convey("Given a marketplace entry and its cache copy", t, func() {
		home := t.TempDir()
		source := filepath.Join(home, ".codex", "plugins", "my-plugin")
		cached := codexCacheDir(home, "personal", "my-plugin", "local")

		writeFile(t, filepath.Join(home, ".agents", "plugins", "marketplace.json"),
			`{"name":"personal","plugins":[{"name":"my-plugin","source":"./.codex/plugins/my-plugin"}]}`)
		validPluginDir(t, source, `{"name":"my-plugin"}`)
		validPluginDir(t, cached, `{"name":"my-plugin"}`)
		writeFile(t, filepath.Join(cached, "skills", "alpha", "SKILL.md"), "# alpha\n")

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then only the cached copy is reported", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldHaveLength, 1)
				So(manifest.Plugins[0].InstallPath, ShouldEqual, cached)
			})
		})
	})
}

func TestReadAllCodexBrokenManifest(t *testing.T) {
	Convey("Given a Codex cache plugin with a broken manifest", t, func() {
		home := t.TempDir()
		dir := codexCacheDir(home, "acme", "broken", "1.0.0")
		validPluginDir(t, dir, "{oops")

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the plugin is skipped with a warning", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldBeEmpty)
				So(manifest.Warnings, ShouldHaveLength, 1)
				So(manifest.Warnings[0], ShouldContainSubstring, "codex: cannot parse")
			})
		})
	})
}

func TestReadAllGeminiExtension(t *testing.T) {
	Convey("Given a Gemini CLI extension", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".gemini", "extensions", "ext-dir")

		writeFile(t, filepath.Join(dir, "gemini-extension.json"), `{
			"name":"ext-name","version":"2.0.0","description":"an extension",
			"mcpServers":{"docs":{"command":"docs-server"}}
		}`)
		writeFile(t, filepath.Join(dir, "skills", "alpha", "SKILL.md"), "# alpha\n")
		writeFile(t, filepath.Join(dir, "agents", "helper.md"), "# helper\n")
		writeFile(t, filepath.Join(dir, "commands", "deploy.toml"), "prompt = \"deploy\"\n")
		writeFile(t, filepath.Join(dir, "hooks", "hooks.json"), `{"SessionStart":[{"matcher":"*","hooks":[{"type":"command","command":"x"}]}]}`)

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the extension carries its artifacts", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldHaveLength, 1)

				p := manifest.Plugins[0]
				So(p.Source, ShouldEqual, plugin.SourceGeminiCLI)
				So(p.Origin, ShouldEqual, plugin.SourceGeminiCLI)
				So(p.Name, ShouldEqual, "ext-name")
				So(p.Version, ShouldEqual, "2.0.0")
				So(p.Skills, ShouldResemble, []string{"alpha"})
				So(p.Agents, ShouldResemble, []string{"helper"})
				So(p.Commands, ShouldResemble, []string{"deploy"})
				So(p.Hooks, ShouldResemble, []string{"SessionStart"})
				So(p.MCPServers, ShouldResemble, []string{"docs"})
			})
		})
	})
}

func TestReadAllAntigravityRoots(t *testing.T) {
	Convey("Given an Antigravity plugin installed in two roots", t, func() {
		home := t.TempDir()
		installed := filepath.Join(home, ".gemini", "config", "plugins", "agy-plugin")
		workspace := filepath.Join(home, ".agents", "plugins", "agy-plugin")

		validPluginDir(t, installed, `{"name":"agy-plugin","version":"1.0.0"}`)
		writeFile(t, filepath.Join(installed, "skills", "alpha", "SKILL.md"), "# alpha\n")
		writeFile(t, filepath.Join(installed, "hooks.json"),
			`{"owner":{"enabled":true,"PreToolUse":[{"type":"command","command":"x","matcher":"run_command"}],"Stop":[{"type":"command","command":"y"}]}}`)
		writeFile(t, filepath.Join(installed, "mcp_config.json"), `{"mcpServers":{"search":{"command":"s"}}}`)

		validPluginDir(t, workspace, `{"name":"agy-plugin","version":"1.0.0"}`)

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the first root wins with a duplicate warning", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldHaveLength, 1)
				So(manifest.Plugins[0].InstallPath, ShouldEqual, installed)
				So(manifest.Plugins[0].Hooks, ShouldResemble, []string{"PreToolUse", "Stop"})
				So(manifest.Plugins[0].MCPServers, ShouldResemble, []string{"search"})
				So(manifest.Warnings, ShouldHaveLength, 1)
				So(manifest.Warnings[0], ShouldContainSubstring, "installed in both")
			})
		})
	})
}

func TestReadAllAntigravityCLIRoot(t *testing.T) {
	Convey("Given an Antigravity plugin only in the CLI data root", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".gemini", "antigravity-cli", "plugins", "cli-plugin")

		validPluginDir(t, dir, `{"name":"cli-plugin","version":"1.0.0"}`)
		writeFile(t, filepath.Join(dir, "skills", "alpha", "SKILL.md"), "# alpha\n")

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the CLI root is read", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldHaveLength, 1)
				So(manifest.Plugins[0].Source, ShouldEqual, plugin.SourceAntigravityCLI)
				So(manifest.Plugins[0].InstallPath, ShouldEqual, dir)
			})
		})
	})
}

func TestReadAllCursorPlugin(t *testing.T) {
	Convey("Given a Cursor plugin with explicit component paths", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".cursor", "plugins", "local", "cursor-plugin")

		writeFile(t, filepath.Join(dir, ".cursor-plugin", "plugin.json"), `{
			"name":"cursor-plugin","version":"0.9.0",
			"skills":"./my-skills/","agents":["./agents/reviewer.md"],"commands":"./commands/"
		}`)
		writeFile(t, filepath.Join(dir, "my-skills", "alpha", "SKILL.md"), "# alpha\n")
		writeFile(t, filepath.Join(dir, "skills", "ignored", "SKILL.md"), "# ignored\n")
		writeFile(t, filepath.Join(dir, "agents", "reviewer.md"), "# reviewer\n")
		writeFile(t, filepath.Join(dir, "agents", "unlisted.md"), "# unlisted\n")
		writeFile(t, filepath.Join(dir, "commands", "deploy.mdc"), "# deploy\n")
		writeFile(t, filepath.Join(dir, "commands", "notes.txt"), "notes\n")
		writeFile(t, filepath.Join(dir, "hooks", "hooks.json"), `{"version":1,"hooks":{"preToolUse":[{"command":"x"}]}}`)
		writeFile(t, filepath.Join(dir, "mcp.json"), `{"mcpServers":{"search":{}}}`)

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the manifest paths replace folder discovery", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldHaveLength, 1)

				p := manifest.Plugins[0]
				So(p.Source, ShouldEqual, plugin.SourceCursor)
				So(p.Skills, ShouldResemble, []string{"alpha"})
				So(p.Agents, ShouldResemble, []string{"reviewer"})
				So(p.Commands, ShouldResemble, []string{"deploy", "notes"})
				So(p.Hooks, ShouldResemble, []string{"preToolUse"})
				So(p.MCPServers, ShouldResemble, []string{"search"})
			})
		})
	})
}

func TestReadAllCursorAgentPluginFormat(t *testing.T) {
	Convey("Given a Cursor Agent Plugins package", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".cursor", "plugins", "local", "agent-pkg")

		validPluginDir(t, dir, `{"name":"agent-pkg","version":"1.0.0"}`)
		writeFile(t, filepath.Join(dir, "skills", "alpha", "SKILL.md"), "# alpha\n")

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the portable manifest is recognized", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldHaveLength, 1)
				So(manifest.Plugins[0].Name, ShouldEqual, "agent-pkg")
				So(manifest.Plugins[0].Skills, ShouldResemble, []string{"alpha"})
			})
		})
	})
}

func TestReadAllSilentWithoutHosts(t *testing.T) {
	Convey("Given a home without any host", t, func() {
		home := t.TempDir()

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then nothing and no warnings are reported", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldBeEmpty)
				So(manifest.Warnings, ShouldBeEmpty)
			})
		})

		Convey("When the home is empty", func() {
			_, err := plugin.ReadAll("")

			Convey("Then it fails", func() {
				So(err, ShouldBeError)
			})
		})
	})
}

func TestArtifactDigestAcrossSources(t *testing.T) {
	Convey("Given the same plugin packaged for two hosts", t, func() {
		home := t.TempDir()
		codex := codexCacheDir(home, "acme", "caveman", "1.0.0")
		gemini := filepath.Join(home, ".gemini", "extensions", "caveman")

		validPluginDir(t, codex, `{"name":"caveman","version":"1.0.0"}`)
		writeFile(t, filepath.Join(codex, "skills", "alpha", "SKILL.md"), "# alpha\n")
		writeFile(t, filepath.Join(codex, "skills", "alpha", "notes.md"), "notes\n")

		writeFile(t, filepath.Join(gemini, "gemini-extension.json"), `{"name":"caveman","version":"1.0.0"}`)
		writeFile(t, filepath.Join(gemini, "skills", "alpha", "SKILL.md"), "# alpha\n")
		writeFile(t, filepath.Join(gemini, "skills", "alpha", "notes.md"), "notes\n")

		Convey("When the digests are computed", func() {
			codexDigest, err := plugin.ArtifactDigest(plugin.SourceCodex, codex)
			So(err, ShouldBeNil)

			geminiDigest, err := plugin.ArtifactDigest(plugin.SourceGeminiCLI, gemini)
			So(err, ShouldBeNil)

			Convey("Then they match", func() {
				So(codexDigest, ShouldEqual, geminiDigest)
			})
		})

		Convey("When one copy changes", func() {
			writeFile(t, filepath.Join(gemini, "skills", "alpha", "notes.md"), "different\n")

			codexDigest, err := plugin.ArtifactDigest(plugin.SourceCodex, codex)
			So(err, ShouldBeNil)

			geminiDigest, err := plugin.ArtifactDigest(plugin.SourceGeminiCLI, gemini)
			So(err, ShouldBeNil)

			Convey("Then the digests differ", func() {
				So(codexDigest, ShouldNotEqual, geminiDigest)
			})
		})

		Convey("When the install path is relative", func() {
			_, err := plugin.ArtifactDigest(plugin.SourceCodex, "relative/path")

			Convey("Then it fails", func() {
				So(err, ShouldBeError)
			})
		})
	})
}

func TestReadHooksForSources(t *testing.T) {
	Convey("Given host-specific hook documents", t, func() {
		home := t.TempDir()

		codex := codexCacheDir(home, "acme", "codex-hooks", "1.0.0")
		validPluginDir(t, codex, `{"name":"codex-hooks","extensions":{"com.openai":{"hooks":"./hooks/custom.json"}}}`)
		writeFile(t, filepath.Join(codex, "hooks", "custom.json"), `{"hooks":{"PreToolUse":[{"matcher":"","hooks":[{"type":"command","command":"codex-cmd"}]}]}}`)
		writeFile(t, filepath.Join(codex, "hooks", "hooks.json"), `{"hooks":{"SessionStart":[]}}`)

		cursor := filepath.Join(home, ".cursor", "plugins", "local", "cursor-hooks")
		writeFile(t, filepath.Join(cursor, ".cursor-plugin", "plugin.json"), `{"name":"cursor-hooks"}`)
		writeFile(t, filepath.Join(cursor, "hooks", "hooks.json"), `{"version":1,"hooks":{
			"preToolUse":[{"command":"cursor-cmd","timeout":5},{"command":"matched.sh","matcher":"Bash"},{"command":"nummatcher.sh","matcher":5}],
			"stop":[{"command":"guarded.sh","failClosed":true}]
		}}`)

		agy := filepath.Join(home, ".gemini", "config", "plugins", "agy-hooks")
		validPluginDir(t, agy, `{"name":"agy-hooks"}`)
		writeFile(t, filepath.Join(agy, "hooks.json"),
			`{"enabled-owner":{"enabled":true,"PreToolUse":[{"type":"command","command":"agy-cmd","matcher":"rm"}]},"off-owner":{"enabled":false,"Stop":[{"type":"command","command":"nope"}]}}`)

		Convey("When the hooks are read per source", func() {
			codexHooks, codexWarns, err := plugin.ReadHooksFor(plugin.SourceCodex, codex)
			So(err, ShouldBeNil)
			So(codexWarns, ShouldBeEmpty)
			So(slices.Sorted(maps.Keys(codexHooks)), ShouldResemble, []string{"PreToolUse"})

			cursorHooks, cursorWarns, err := plugin.ReadHooksFor(plugin.SourceCursor, cursor)
			So(err, ShouldBeNil)
			So(cursorWarns, ShouldResemble, []string{"hook matcher is not a string; ignored"})
			So(slices.Sorted(maps.Keys(cursorHooks)), ShouldResemble, []string{"preToolUse", "stop"})
			So(cursorHooks["preToolUse"], ShouldHaveLength, 3)
			So(cursorHooks["preToolUse"][0].Matcher, ShouldEqual, "")
			So(cursorHooks["preToolUse"][0].Hooks[0].Type, ShouldEqual, "command")
			So(cursorHooks["preToolUse"][0].Hooks[0].Command, ShouldEqual, "cursor-cmd")
			So(cursorHooks["preToolUse"][0].Hooks[0].Timeout, ShouldEqual, 5)
			So(cursorHooks["preToolUse"][1].Matcher, ShouldEqual, "Bash")
			So(cursorHooks["preToolUse"][1].Hooks[0].Command, ShouldEqual, "matched.sh")
			So(cursorHooks["preToolUse"][2].Matcher, ShouldEqual, "")
			So(cursorHooks["preToolUse"][2].Hooks[0].Command, ShouldEqual, "nummatcher.sh")
			So(cursorHooks["stop"][0].Hooks[0].Command, ShouldEqual, "guarded.sh")
			So(cursorHooks["stop"][0].Hooks[0].Unsupported, ShouldResemble, []string{"failClosed"})

			agyHooks, agyWarns, err := plugin.ReadHooksFor(plugin.SourceAntigravityCLI, agy)
			So(err, ShouldBeNil)
			So(agyWarns, ShouldBeEmpty)
			So(slices.Sorted(maps.Keys(agyHooks)), ShouldResemble, []string{"PreToolUse"})
			So(agyHooks["PreToolUse"][0].Matcher, ShouldEqual, "rm")
			So(agyHooks["PreToolUse"][0].Hooks[0].Command, ShouldEqual, "agy-cmd")

			Convey("Then the explicit Codex hooks replace the default file", func() {
				So(codexHooks["PreToolUse"][0].Hooks[0].Command, ShouldEqual, "codex-cmd")
			})
		})
	})
}

func TestReadHooksForMalformed(t *testing.T) {
	Convey("Given a broken hooks document", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".gemini", "extensions", "broken")
		writeFile(t, filepath.Join(dir, "gemini-extension.json"), `{"name":"broken"}`)
		writeFile(t, filepath.Join(dir, "hooks", "hooks.json"), "{oops")

		Convey("When the hooks are read", func() {
			_, warns, err := plugin.ReadHooksFor(plugin.SourceGeminiCLI, dir)

			Convey("Then it fails for the caller to report", func() {
				So(err, ShouldBeError)
				So(warns, ShouldBeNil)
			})
		})

		Convey("When the install path is not absolute", func() {
			_, warns, err := plugin.ReadHooksFor(plugin.SourceGeminiCLI, "relative")

			Convey("Then it is refused with a warning", func() {
				So(err, ShouldBeNil)
				So(warns, ShouldHaveLength, 1)
			})
		})
	})
}

func TestReadAllCodexMultipleVersions(t *testing.T) {
	Convey("Given two cached versions of one Codex plugin", t, func() {
		home := t.TempDir()
		older := codexCacheDir(home, "acme", "tool", "1.0.0")
		newer := codexCacheDir(home, "acme", "tool", "2.0.0")

		validPluginDir(t, newer, `{"name":"tool","version":"2.0.0"}`)
		writeFile(t, filepath.Join(newer, "skills", "beta", "SKILL.md"), "# beta\n")
		validPluginDir(t, older, `{"name":"tool","version":"1.0.0"}`)
		writeFile(t, filepath.Join(older, "skills", "alpha", "SKILL.md"), "# alpha\n")

		base := time.Now().Add(-time.Hour)

		So(os.Chtimes(older, base, base), ShouldBeNil)
		So(os.Chtimes(newer, base.Add(time.Minute), base.Add(time.Minute)), ShouldBeNil)

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the newest version wins with one warning", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldHaveLength, 1)
				So(manifest.Plugins[0].InstallPath, ShouldEqual, newer)
				So(manifest.Plugins[0].Version, ShouldEqual, "2.0.0")
				So(manifest.Plugins[0].Skills, ShouldResemble, []string{"beta"})
				So(manifest.Warnings, ShouldHaveLength, 1)
				So(manifest.Warnings[0], ShouldContainSubstring, "has 2 cached versions")
				So(manifest.Warnings[0], ShouldContainSubstring, "using 2.0.0")
			})
		})
	})
}

func TestReadAllCodexOrphanedVersionIsSkipped(t *testing.T) {
	Convey("Given an orphaned newest Codex version", t, func() {
		home := t.TempDir()
		older := codexCacheDir(home, "acme", "tool", "1.0.0")
		newer := codexCacheDir(home, "acme", "tool", "2.0.0")

		validPluginDir(t, older, `{"name":"tool","version":"1.0.0"}`)
		validPluginDir(t, newer, `{"name":"tool","version":"2.0.0"}`)
		writeFile(t, filepath.Join(newer, ".orphaned_at"), "1789083161350\n")

		base := time.Now().Add(-time.Hour)

		So(os.Chtimes(older, base, base), ShouldBeNil)
		So(os.Chtimes(newer, base.Add(time.Minute), base.Add(time.Minute)), ShouldBeNil)

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the active older version wins", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldHaveLength, 1)
				So(manifest.Plugins[0].Version, ShouldEqual, "1.0.0")
			})
		})
	})
}

func TestReadAllCodexAllVersionsOrphaned(t *testing.T) {
	Convey("Given only orphaned Codex versions", t, func() {
		home := t.TempDir()
		dir := codexCacheDir(home, "acme", "tool", "1.0.0")

		validPluginDir(t, dir, `{"name":"tool","version":"1.0.0"}`)
		writeFile(t, filepath.Join(dir, ".orphaned_at"), "1789083161350\n")

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the plugin is skipped with a warning", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldBeEmpty)
				So(manifest.Warnings, ShouldHaveLength, 1)
				So(manifest.Warnings[0], ShouldContainSubstring, "all 1 cached version(s) are orphaned")
			})
		})
	})
}

func TestReadAllCodexClaudeManifestFallback(t *testing.T) {
	Convey("Given a Codex plugin with a Claude-compatible manifest", t, func() {
		home := t.TempDir()
		dir := codexCacheDir(home, "acme", "compat", "1.0.0")

		writeFile(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{"name":"compat","version":"1.0.0","skills":"./my-skills/"}`)
		writeFile(t, filepath.Join(dir, "my-skills", "alpha", "SKILL.md"), "# alpha\n")

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the fallback manifest and its skill path are read", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldHaveLength, 1)
				So(manifest.Plugins[0].Name, ShouldEqual, "compat")
				So(manifest.Plugins[0].Skills, ShouldResemble, []string{"alpha"})
			})
		})
	})
}

func TestReadAllCursorRootSkillWarns(t *testing.T) {
	Convey("Given a Cursor plugin with a root SKILL.md only", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".cursor", "plugins", "local", "solo")

		validPluginDir(t, dir, `{"name":"solo"}`)
		writeFile(t, filepath.Join(dir, "SKILL.md"), "# solo\n")

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the single-skill layout is reported, not silently dropped", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldHaveLength, 1)
				So(manifest.Plugins[0].Skills, ShouldBeEmpty)
				So(manifest.Warnings, ShouldHaveLength, 1)
				So(manifest.Warnings[0], ShouldContainSubstring, "single-skill root SKILL.md")
			})
		})
	})
}

func TestReadAllGeminiVersionlessManifest(t *testing.T) {
	Convey("Given a versionless Gemini extension", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".gemini", "extensions", "plain")

		writeFile(t, filepath.Join(dir, "gemini-extension.json"), `{"name":"plain","description":"no version"}`)
		writeFile(t, filepath.Join(dir, "skills", "alpha", "SKILL.md"), "# alpha\n")

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the extension is read without a version", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldHaveLength, 1)
				So(manifest.Plugins[0].Version, ShouldBeEmpty)
				So(manifest.Plugins[0].Skills, ShouldResemble, []string{"alpha"})
			})
		})
	})
}

func TestReadAllSharedAgentsRoot(t *testing.T) {
	Convey("Given one .agents/plugins root with a marketplace file and an Antigravity plugin", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".agents", "plugins", "agy-plugin")

		writeFile(t, filepath.Join(home, ".agents", "plugins", "marketplace.json"), `{"name":"personal","plugins":[]}`)
		validPluginDir(t, dir, `{"name":"agy-plugin","version":"1.0.0"}`)
		writeFile(t, filepath.Join(dir, "skills", "alpha", "SKILL.md"), "# alpha\n")

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then Antigravity reads the directory and Codex ignores the file", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldHaveLength, 1)
				So(manifest.Plugins[0].Source, ShouldEqual, plugin.SourceAntigravityCLI)
				So(manifest.Plugins[0].Name, ShouldEqual, "agy-plugin")
			})
		})
	})
}

func TestReadAllClaudeNestedPayloadIsIgnored(t *testing.T) {
	Convey("Given a Claude plugin with nested and foreign payloads", t, func() {
		home, pluginDir, _, _ := pluginFixture(t)

		writeFile(t, filepath.Join(pluginDir, "plugins", "nested", "skills", "hidden", "SKILL.md"), "# hidden\n")
		writeFile(t, filepath.Join(pluginDir, "plugins", "nested", ".codex-plugin", "plugin.json"), `{"name":"nested"}`)
		writeFile(t, filepath.Join(pluginDir, ".codex-plugin", "plugin.json"), `{"name":"foreign-overlay"}`)

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the nested payload does not leak into the model", func() {
				So(err, ShouldBeNil)

				names := make([]string, 0, len(manifest.Plugins))

				for _, p := range manifest.Plugins {
					names = append(names, p.Name)
				}

				So(names, ShouldNotContain, "nested")

				for _, p := range manifest.Plugins {
					if p.Name == "vmkteam-developer" {
						So(p.Skills, ShouldNotContain, "hidden")
					}
				}
			})
		})
	})
}

func TestReadAllClaudeSourceField(t *testing.T) {
	Convey("Given a Claude plugin registry", t, func() {
		home, _, _, _ := pluginFixture(t)

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the Claude plugins carry their source", func() {
				So(err, ShouldBeNil)

				for _, p := range manifest.Plugins {
					So(p.Source, ShouldEqual, plugin.SourceClaudeCode)
				}
			})
		})
	})
}

func TestPluginExplicitPathEscapesRoot(t *testing.T) {
	Convey("Given a Cursor manifest with an escaping skill path", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".cursor", "plugins", "local", "escape")
		writeFile(t, filepath.Join(dir, ".cursor-plugin", "plugin.json"), `{"name":"escape","skills":"../outside"}`)

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the plugin is read without the escaping path", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldHaveLength, 1)
				So(manifest.Plugins[0].Skills, ShouldBeEmpty)
				So(manifest.Warnings, ShouldHaveLength, 1)
				So(manifest.Warnings[0], ShouldContainSubstring, "escapes the plugin root")
			})
		})
	})
}
