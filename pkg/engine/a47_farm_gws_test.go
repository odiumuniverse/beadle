package engine_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/plugin"
)

func enableAgents(t *testing.T, f *fixture, ids ...string) {
	t.Helper()

	for _, id := range ids {
		f.config.Enable(id)
	}

	So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)
}

func geminiHome(t *testing.T, f *fixture) {
	t.Helper()

	write(t, filepath.Join(f.home, ".gemini", "settings.json"), "{}")
}

func a47CursorHome(t *testing.T, f *fixture) {
	t.Helper()

	write(t, filepath.Join(f.home, ".cursor", "mcp.json"), `{"mcpServers":{}}`)
}

func writingAgent(t *testing.T, pluginDir, name, body string) {
	t.Helper()

	write(t, filepath.Join(pluginDir, "agents", name+".md"), body)
}

func pluginFarmLink(f *fixture, origin, name, kindDir, file string) string {
	return filepath.Join(f.vault.PluginsDir(), origin, name, "current", kindDir, file)
}

func TestA47CodexPluginFarmsToEveryHost(t *testing.T) {
	Convey("Given a Codex plugin and every file host", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		geminiHome(t, f)
		a47CursorHome(t, f)
		enableAgents(t, f, agent.GeminiCLIID, agent.CursorID)

		dir := codexPluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, dir, "alpha", "# alpha\n")
		writingAgent(t, dir, "helper", "---\nname: helper\ndescription: helps\n---\n\nHelp.\n")
		write(t, filepath.Join(dir, "commands", "deploy.md"), farmCommandDoc)
		write(t, filepath.Join(dir, "hooks", "hooks.json"), `{"hooks":{"PreToolUse":[{"matcher":"","hooks":[{"type":"command","command":"run"}]}]}}`)
		write(t, filepath.Join(dir, "mcp.json"), `{"mcpServers":{"fetch":{"command":"/bin/fetch"}}}`)

		f.sync(t)

		Convey("When the farm runs", func() {
			Convey("Then skills, agents and commands reach every other host", func() {
				So(farmLink(t, claudeSkillsDir(f.home), "alpha"), ShouldEqual, pluginFarmLink(f, "acme", "tool", "skills", "alpha"))
				So(farmLink(t, filepath.Join(f.home, ".gemini", "skills"), "alpha"), ShouldEqual, pluginFarmLink(f, "acme", "tool", "skills", "alpha"))
				So(farmLink(t, filepath.Join(f.home, ".cursor", "skills"), "alpha"), ShouldEqual, pluginFarmLink(f, "acme", "tool", "skills", "alpha"))

				So(farmLink(t, openCodeSkillsDir(f.home), "alpha"), ShouldEqual, pluginFarmLink(f, "acme", "tool", "skills", "alpha"))

				agentLink := pluginFarmLink(f, "acme", "tool", "agents", "helper.md")
				So(farmLink(t, filepath.Join(f.home, ".config", "opencode", "agents"), "tool--helper.md"), ShouldEqual, agentLink)
				So(farmLink(t, filepath.Join(f.home, ".gemini", "agents"), "tool--helper.md"), ShouldEqual, agentLink)
				So(farmLink(t, filepath.Join(f.home, ".cursor", "agents"), "tool--helper.md"), ShouldEqual, agentLink)

				So(farmLink(t, openCodeCommandsDir(f.home), "tool--deploy.md"), ShouldEqual, pluginFarmLink(f, "acme", "tool", "commands", "deploy.md"))

				geminiCommand := filepath.Join(f.home, ".gemini", "commands", "tool--deploy.toml")

				link, err := os.Readlink(geminiCommand)
				So(err, ShouldBeNil)
				So(link, ShouldContainSubstring, filepath.Join("farm", "acme", "tool", "commands"))
				So(read(t, geminiCommand), ShouldContainSubstring, "prompt = ")

				_, claudeAgentErr := os.Stat(filepath.Join(f.home, ".claude", "agents", "tool--helper.md"))
				So(errors.Is(claudeAgentErr, fs.ErrNotExist), ShouldBeTrue)
			})

			Convey("Then the plugin MCP server reaches every host", func() {
				claude := hostMCPServers(t, f.claudeConfig(), "mcpServers")
				So(claude, ShouldContainKey, "fetch")

				openCode := hostMCPServers(t, f.openCodeConfig(), "mcp")
				So(openCode, ShouldContainKey, "fetch")

				gemini := hostMCPServers(t, filepath.Join(f.home, ".gemini", "settings.json"), "mcpServers")
				So(gemini, ShouldContainKey, "fetch")

				cursor := hostMCPServers(t, filepath.Join(f.home, ".cursor", "mcp.json"), "mcpServers")
				So(cursor, ShouldContainKey, "fetch")
			})

			Convey("Then the plugin hooks wait for an explicit approval", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityInfo, "approve with `beadle hooks approve --plugin acme/tool`"), ShouldBeTrue)
			})
		})
	})
}

func TestA47FarmIsIdempotentForForeignSources(t *testing.T) {
	Convey("Given a farmed Codex plugin", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		dir := codexPluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, dir, "alpha", "# alpha\n")
		writingAgent(t, dir, "helper", "---\nname: helper\ndescription: helps\n---\n\nHelp.\n")
		write(t, filepath.Join(dir, "commands", "deploy.md"), farmCommandDoc)

		f.sync(t)

		before := read(t, filepath.Join(claudeSkillsDir(f.home), "alpha", "SKILL.md"))

		report := f.sync(t)

		Convey("When the farm runs again", func() {
			Convey("Then it links nothing and the payload is untouched", func() {
				So(report.Farm, ShouldBeEmpty)
				So(report.Errors(), ShouldBeEmpty)
				So(read(t, filepath.Join(claudeSkillsDir(f.home), "alpha", "SKILL.md")), ShouldEqual, before)
				So(read(t, farmLink(t, openCodeCommandsDir(f.home), "tool--deploy.md")), ShouldEqual, farmCommandDoc)
			})
		})
	})
}

func TestA47GeminiCommandsReachMarkdownHosts(t *testing.T) {
	Convey("Given a Gemini extension with a TOML command", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		geminiHome(t, f)
		enableAgents(t, f, agent.GeminiCLIID)

		dir := geminiExtensionTree(t, f.home, "ext")
		write(t, filepath.Join(dir, "commands", "deploy.toml"), "description = \"Deploy\"\nprompt = \"Deploy {{args}}\"\n")
		writeSkill(t, dir, "alpha", "# alpha\n")

		f.sync(t)

		Convey("When the farm runs", func() {
			Convey("Then the TOML command is lifted for markdown hosts", func() {
				link := farmLink(t, openCodeCommandsDir(f.home), "ext--deploy.md")
				So(link, ShouldContainSubstring, filepath.Join("farm", "gemini-cli", "ext", "commands"))

				lifted := read(t, link)
				So(lifted, ShouldContainSubstring, "description: Deploy")
				So(lifted, ShouldContainSubstring, "Deploy $ARGUMENTS")
				So(lifted, ShouldNotContainSubstring, "prompt = ")

				Convey("And the extension host gets its own format back", func() {
					gemini := read(t, filepath.Join(f.home, ".gemini", "commands", "ext--deploy.toml"))
					So(gemini, ShouldContainSubstring, "prompt = ")
					So(gemini, ShouldContainSubstring, "{{args}}")
				})

				Convey("And the extension skills reach Claude", func() {
					So(farmLink(t, claudeSkillsDir(f.home), "alpha"), ShouldEqual, pluginFarmLink(f, "gemini-cli", "ext", "skills", "alpha"))
				})
			})
		})

		Convey("When the sync runs again", func() {
			report := f.sync(t)

			Convey("Then the lifted and rendered commands are stable", func() {
				So(report.Farm, ShouldBeEmpty)
				So(report.Errors(), ShouldBeEmpty)
				So(read(t, farmLink(t, openCodeCommandsDir(f.home), "ext--deploy.md")), ShouldContainSubstring, "Deploy $ARGUMENTS")
				So(read(t, filepath.Join(f.home, ".gemini", "commands", "ext--deploy.toml")), ShouldContainSubstring, "prompt = ")
			})
		})
	})
}

func TestA47DuplicatePluginPresentedOnce(t *testing.T) {
	Convey("Given the same plugin from Claude and Gemini", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		geminiHome(t, f)
		enableAgents(t, f, agent.GeminiCLIID)

		claude := pluginTree(t, f.home, "caveman", "caveman", "1.0.0")
		writeSkill(t, claude, "alpha", "# alpha\n")
		writingAgent(t, claude, "helper", "---\nname: helper\ndescription: helps\n---\n\nHelp.\n")

		gemini := geminiExtensionTree(t, f.home, "caveman")
		writeSkill(t, gemini, "alpha", "# alpha\n")
		writingAgent(t, gemini, "helper", "---\nname: helper\ndescription: helps\n---\n\nHelp.\n")

		report := f.sync(t)

		Convey("When the sync runs", func() {
			Convey("Then only one copy is presented with a note", func() {
				So(containsWarning(report.Notes, "presenting the caveman/caveman copy"), ShouldBeTrue)

				want := pluginFarmLink(f, "caveman", "caveman", "skills", "alpha")
				So(farmLink(t, claudeSkillsDir(f.home), "alpha"), ShouldEqual, want)
				So(farmLink(t, filepath.Join(f.home, ".gemini", "skills"), "alpha"), ShouldEqual, want)

				So(containsWarning(report.Warnings, "different content"), ShouldBeFalse)
				So(containsWarning(report.Warnings, "already provided"), ShouldBeFalse)
			})

			Convey("When the winning copy is uninstalled", func() {
				removeFromRegistry(t, f.home, "caveman", "caveman")
				So(os.RemoveAll(claude), ShouldBeNil)

				f.sync(t)

				Convey("Then the surviving copy takes over", func() {
					want := pluginFarmLink(f, "gemini-cli", "caveman", "skills", "alpha")
					So(farmLink(t, claudeSkillsDir(f.home), "alpha"), ShouldEqual, want)
					So(farmLink(t, filepath.Join(f.home, ".gemini", "skills"), "alpha"), ShouldEqual, want)
					So(farmLink(t, a47OpenCodeAgentsDir(f.home), "caveman--helper.md"), ShouldEqual, pluginFarmLink(f, "gemini-cli", "caveman", "agents", "helper.md"))
				})
			})

			Convey("When the copies diverge", func() {
				write(t, filepath.Join(gemini, "skills", "alpha", "SKILL.md"), "# different\n")

				report := f.sync(t)
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)

				Convey("Then both stay visible and the divergence is warned", func() {
					So(containsWarning(report.Warnings, "different content"), ShouldBeTrue)
					So(hasIssue(issues, engine.SeverityWarn, "different content"), ShouldBeTrue)
					So(farmLink(t, claudeSkillsDir(f.home), "alpha"), ShouldEqual, pluginFarmLink(f, "caveman", "caveman", "skills", "alpha"))
				})
			})
		})
	})
}

func TestA47DuplicatePluginSuppressesMCP(t *testing.T) {
	Convey("Given the same plugin with an MCP server in Codex and Cursor", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		a47CursorHome(t, f)
		enableAgents(t, f, agent.CursorID)

		mcpDoc := `{"mcpServers":{"shared":{"command":"/bin/shared"}}}`

		codex := codexPluginTree(t, f.home, "acme", "tool", "1.0.0")
		write(t, filepath.Join(codex, "mcp.json"), mcpDoc)

		cursor := filepath.Join(f.home, ".cursor", "plugins", "local", "tool")
		write(t, filepath.Join(cursor, "plugin.json"), `{"name":"tool","version":"1.0.0"}`)
		write(t, filepath.Join(cursor, "mcp.json"), mcpDoc)

		report := f.sync(t)

		Convey("When the sync runs", func() {
			Convey("Then the server is presented once from the winning host", func() {
				claude := hostMCPServers(t, f.claudeConfig(), "mcpServers")
				So(claude, ShouldContainKey, "shared")

				So(containsWarning(report.Notes, "presenting the cursor/tool copy"), ShouldBeTrue)
				So(containsWarning(report.Warnings, "already provided by"), ShouldBeFalse)
			})
		})
	})
}

func TestA47DottedPluginNameArtifactsAreStable(t *testing.T) {
	Convey("Given a Gemini extension whose name carries a dot", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		geminiHome(t, f)
		enableAgents(t, f, agent.GeminiCLIID)

		dir := filepath.Join(f.home, ".gemini", "extensions", "project.ext")
		write(t, filepath.Join(dir, "gemini-extension.json"), `{"name":"project.ext","version":"1.0.0"}`)
		write(t, filepath.Join(dir, "commands", "deploy.toml"), "description = \"Deploy\"\nprompt = \"Deploy {{args}}\"\n")

		f.sync(t)

		artifact := filepath.Join(f.vault.PluginsDir(), "farm", "gemini-cli", "project.ext", "commands", "project.ext--deploy.md")

		before, err := os.Stat(artifact)
		So(err, ShouldBeNil)

		report := f.sync(t)

		Convey("When the sync runs again", func() {
			Convey("Then the lifted artifact is not churned", func() {
				after, err := os.Stat(artifact)
				So(err, ShouldBeNil)
				So(after.ModTime(), ShouldEqual, before.ModTime())

				Convey("And the TOML host reports the inexpressible namespace as skipped", func() {
					So(report.Farm, ShouldHaveLength, 1)
					So(report.Farm[0].Action, ShouldEqual, engine.FarmSkipped)
					So(report.Farm[0].Agent, ShouldEqual, agent.GeminiCLIID)
				})
			})
		})
	})
}

func TestA47ClaudeTOMLCommandsReachOtherHosts(t *testing.T) {
	Convey("Given a Claude plugin packaging TOML commands for Gemini", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		geminiHome(t, f)
		enableAgents(t, f, agent.GeminiCLIID)

		dir := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		write(t, filepath.Join(dir, "commands", "deploy.toml"), "description = \"Deploy\"\nprompt = \"Deploy {{args}}\"\n")
		write(t, filepath.Join(dir, "commands", "bogus.toml"), "this is not = = toml\n")

		report := f.sync(t)

		Convey("When the farm runs", func() {
			Convey("Then the TOML command is lifted for markdown hosts", func() {
				lifted := read(t, farmLink(t, openCodeCommandsDir(f.home), "tool--deploy.md"))
				So(lifted, ShouldContainSubstring, "description: Deploy")
				So(lifted, ShouldContainSubstring, "Deploy $ARGUMENTS")
				So(lifted, ShouldNotContainSubstring, "prompt = ")

				Convey("And rendered back for the extension host", func() {
					gemini := read(t, filepath.Join(f.home, ".gemini", "commands", "tool--deploy.toml"))
					So(gemini, ShouldContainSubstring, "prompt = ")
					So(gemini, ShouldContainSubstring, "{{args}}")
				})

				Convey("And an unparsable TOML command is reported, not dropped silently", func() {
					So(containsWarning(report.Warnings, "lift bogus"), ShouldBeTrue)
				})
			})
		})
	})
}

func TestA47CodexMultipleVersionsParkTheNewest(t *testing.T) {
	Convey("Given two cached versions of one Codex plugin", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		older := codexPluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, older, "alpha", "# alpha\n")

		newer := codexPluginTree(t, f.home, "acme", "tool", "2.0.0")
		writeSkill(t, newer, "beta", "# beta\n")

		base := time.Now().Add(-time.Hour)

		So(os.Chtimes(older, base, base), ShouldBeNil)
		So(os.Chtimes(newer, base.Add(time.Minute), base.Add(time.Minute)), ShouldBeNil)

		report := f.sync(t)

		Convey("When the sync runs", func() {
			Convey("Then one key is parked at the newest version", func() {
				So(report.Plugins, ShouldHaveLength, 1)
				So(report.Plugins[0].Key, ShouldEqual, "acme/tool")
				So(report.Plugins[0].Version, ShouldEqual, "2.0.0")
				So(pivotLink(t, f, "acme", "tool"), ShouldEqual, newer)

				So(farmLink(t, claudeSkillsDir(f.home), "beta"), ShouldEqual, filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current", "skills", "beta"))
				So(containsWarning(report.Warnings, "has 2 cached versions"), ShouldBeTrue)
			})
		})
	})
}

func TestA47PluginMCPSecretsAreExtracted(t *testing.T) {
	Convey("Given a plugin .mcp.json with a literal secret", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		dir := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		write(t, filepath.Join(dir, ".mcp.json"),
			`{"mcpServers":{"leaky":{"command":"/bin/leaky","env":{"API_TOKEN":"ghp_abcdefghijklmnopqrstuvwxyz0123456789"}}}}`)

		report := f.sync(t)

		Convey("When the sync runs", func() {
			Convey("Then the value moves to the secret store and never enters the vault files", func() {
				secrets, err := os.ReadFile(f.vault.SecretsPath())
				So(err, ShouldBeNil)
				So(string(secrets), ShouldContainSubstring, "ghp_")

				info, err := os.Stat(f.vault.SecretsPath())
				So(err, ShouldBeNil)
				So(info.Mode().Perm(), ShouldEqual, fs.FileMode(0o600))

				So(warnedAbout(report, "literal secret"), ShouldBeTrue)

				secretPath := f.vault.SecretsPath()
				vaultRoot := filepath.Dir(filepath.Dir(secretPath))

				var leaked []string

				err = filepath.WalkDir(vaultRoot, func(path string, entry fs.DirEntry, err error) error {
					if err != nil || entry.IsDir() || path == secretPath {
						//nolint:nilerr // an unreadable entry cannot hold the value the walk looks for
						return nil
					}

					data, err := os.ReadFile(path) //nolint:gosec // the test reads its own temp vault
					if err == nil && strings.Contains(string(data), "ghp_") {
						leaked = append(leaked, path)
					}

					return nil
				})
				So(err, ShouldBeNil)
				So(leaked, ShouldBeEmpty)
			})
		})
	})
}

func TestA47BeadleBundlePluginIsSelfSkipped(t *testing.T) {
	Convey("Given the beadle bundle plugin in the Claude cache", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		dir := pluginTree(t, f.home, "beadle", "beadle-canon", "0.0.0-test")
		writeSkill(t, dir, "adhd", "# adhd\n")
		write(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers":{"bundle-only":{"command":"/bin/bundle"}}}`)

		neighbour := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, neighbour, "alpha", "# alpha\n")

		report := f.sync(t)

		Convey("When the sync runs", func() {
			Convey("Then the bundle plugin is neither parked nor presented", func() {
				manifest, err := plugin.ReadAll(f.home)
				So(err, ShouldBeNil)

				seen := false

				for _, p := range manifest.Plugins {
					if p.Origin == "beadle" && p.Name == "beadle-canon" {
						seen = true
					}
				}

				So(seen, ShouldBeTrue)

				So(pluginResult(t, report, "acme/tool").Action, ShouldEqual, engine.PluginCreated)
				So(farmLink(t, claudeSkillsDir(f.home), "alpha"), ShouldEqual, filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current", "skills", "alpha"))

				for _, result := range report.Plugins {
					So(result.Key, ShouldNotEqual, "beadle/beadle-canon")
				}

				claude := hostMCPServers(t, f.claudeConfig(), "mcpServers")
				So(claude, ShouldNotContainKey, "bundle-only")

				openCode := hostMCPServers(t, f.openCodeConfig(), "mcp")
				So(openCode, ShouldNotContainKey, "bundle-only")

				if data, err := os.ReadFile(f.vault.ServersPath()); err == nil {
					So(string(data), ShouldNotContainSubstring, "bundle-only")
				}

				_, skillErr := os.Stat(filepath.Join(claudeSkillsDir(f.home), "adhd"))
				So(errors.Is(skillErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestA47RemovedForeignPluginIsPruned(t *testing.T) {
	Convey("Given a Codex plugin removed from the cache", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		dir := codexPluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, dir, "alpha", "# alpha\n")

		f.sync(t)

		So(os.RemoveAll(filepath.Dir(dir)), ShouldBeNil)

		report := f.sync(t)

		Convey("When the sync runs", func() {
			Convey("Then the plugin is quarantined and its presentation goes", func() {
				result := pluginResult(t, report, "acme/tool")
				So(result.Action, ShouldEqual, engine.PluginQuarantined)

				entries, err := os.ReadDir(claudeSkillsDir(f.home))
				So(err, ShouldBeNil)

				for _, entry := range entries {
					if entry.Name() != "alpha" {
						continue
					}

					link, err := os.Readlink(filepath.Join(claudeSkillsDir(f.home), entry.Name()))
					if err == nil {
						So(strings.Contains(link, "acme/tool"), ShouldBeFalse)
					}
				}
			})
		})
	})
}

func a47OpenCodeAgentsDir(home string) string {
	return filepath.Join(home, ".config", "opencode", "agents")
}
