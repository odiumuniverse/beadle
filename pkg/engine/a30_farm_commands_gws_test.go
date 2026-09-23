package engine_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
)

func writeCommand(t *testing.T, pluginDir, name, body string) {
	t.Helper()

	write(t, filepath.Join(pluginDir, "commands", name+".md"), body)
}

func pluginCurrentCommand(f *fixture, marketplace, name string) string {
	return filepath.Join(f.vault.PluginsDir(), marketplace, "tool", "current", "commands", name+".md")
}

func openCodeCommandsDir(home string) string {
	return filepath.Join(home, ".config", "opencode", "commands")
}

const farmCommandDoc = `---
description: Test command
---

Do the thing.
`

func TestPluginFarmPresentsCommands(t *testing.T) {
	Convey("Given a plugin with a command and an enabled Codex", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		f.config.Enable(agent.CodexID)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		write(t, filepath.Join(f.home, ".codex", "config.toml"), "")

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeCommand(t, plugin, "deploy", farmCommandDoc)

		dir := openCodeCommandsDir(f.home)
		write(t, filepath.Join(dir, "custom.md"), "# mine\n")

		report := f.sync(t)

		Convey("When the farm runs", func() {
			Convey("Then the command is presented to OpenCode, Claude and Codex are skipped", func() {
				So(farmLink(t, dir, "tool--deploy.md"), ShouldEqual, pluginCurrentCommand(f, "acme", "deploy"))

				_, claudeErr := os.Stat(filepath.Join(f.home, ".claude", "commands", "tool--deploy.md"))
				So(errors.Is(claudeErr, fs.ErrNotExist), ShouldBeTrue)

				_, codexErr := os.Stat(filepath.Join(f.home, ".codex", "prompts", "tool--deploy.md"))
				So(errors.Is(codexErr, fs.ErrNotExist), ShouldBeTrue)

				Convey("And the foreign command is untouched", func() {
					So(read(t, filepath.Join(dir, "custom.md")), ShouldEqual, "# mine\n")
				})

				linked := 0

				for _, result := range report.Farm {
					if result.Action == engine.FarmLinked && result.Plugin == "acme/tool" {
						linked += result.Count
					}
				}

				So(linked, ShouldEqual, 1)
			})
		})
	})
}

func TestPluginFarmCommandGeminiRendered(t *testing.T) {
	Convey("Given a Gemini host and a plugin command", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.GeminiCLIID)

		So(os.MkdirAll(filepath.Join(f.home, ".gemini"), 0o750), ShouldBeNil)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeCommand(t, plugin, "deploy", farmCommandDoc)

		f.sync(t)

		link := filepath.Join(f.home, ".gemini", "commands", "tool--deploy.toml")

		Convey("When the farm runs", func() {
			Convey("Then the command is rendered as TOML and linked from the vault", func() {
				So(farmLink(t, filepath.Dir(link), "tool--deploy.toml"), ShouldContainSubstring, "farm")

				So(read(t, link), ShouldContainSubstring, "Do the thing.")

				artifact := filepath.Join(f.vault.PluginsDir(), "farm", "acme", "tool", "commands", "tool--deploy.toml")

				_, err := os.Stat(artifact)
				So(err, ShouldBeNil)

				_, canonErr := os.Stat(filepath.Join(f.vault.CommandsDir(), "tool--deploy.md"))
				So(errors.Is(canonErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})

		Convey("When sync runs again", func() {
			before := read(t, link)

			report2 := f.sync(t)

			Convey("Then the second sync is a byte-for-byte no-op and relinks nothing", func() {
				So(read(t, link), ShouldEqual, before)
				So(report2.Farm, ShouldBeEmpty)
			})
		})
	})
}

func TestPluginFarmCommandCanonWins(t *testing.T) {
	Convey("Given a plugin command and a canon command with the same name", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeCommand(t, plugin, "deploy", farmCommandDoc)

		write(t, filepath.Join(f.vault.CommandsDir(), "tool--deploy.md"),
			"---\ndescription: Canon\n---\nCanon body.\n")

		report := f.sync(t)

		Convey("When the farm runs", func() {
			Convey("Then the canon file is delivered and the plugin copy is shadowed", func() {
				path := filepath.Join(openCodeCommandsDir(f.home), "tool--deploy.md")

				info, err := os.Lstat(path)
				So(err, ShouldBeNil)
				So(info.Mode()&fs.ModeSymlink == 0, ShouldBeTrue)
				So(read(t, path), ShouldContainSubstring, "Canon body.")

				So(containsWarning(report.Warnings, "command tool--deploy is shadowed by the vault canon"), ShouldBeTrue)
			})
		})
	})
}

//nolint:dupl // the agent and command variants intentionally mirror each other
func TestPluginFarmCommandNestedSkipped(t *testing.T) {
	Convey("Given a plugin with a nested command", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		write(t, filepath.Join(plugin, "commands", "team", "review.md"), farmCommandDoc)

		report := f.sync(t)

		Convey("When the farm runs", func() {
			Convey("Then the nested definition is skipped with a warning", func() {
				So(containsWarning(report.Warnings, "command directory team"), ShouldBeTrue)
				So(containsWarning(report.Warnings, "nested ids are skipped"), ShouldBeTrue)

				entries, err := os.ReadDir(openCodeCommandsDir(f.home))
				So(errors.Is(err, fs.ErrNotExist) || len(entries) == 0, ShouldBeTrue)
			})
		})
	})
}

func TestPluginFarmCommandNamespaceCollision(t *testing.T) {
	Convey("Given two plugins providing the same command name", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		first := pluginTree(t, f.home, "aaa", "tool", "1.0.0")
		writeCommand(t, first, "deploy", farmCommandDoc)

		second := pluginTree(t, f.home, "bbb", "tool", "1.0.0")
		writeCommand(t, second, "deploy", farmCommandDoc)

		report := f.sync(t)

		Convey("When the farm runs", func() {
			Convey("Then the first owner wins and the other plugin warns", func() {
				dir := openCodeCommandsDir(f.home)

				So(farmLink(t, dir, "tool--deploy.md"), ShouldEqual, pluginCurrentCommand(f, "aaa", "deploy"))

				So(containsWarning(report.Warnings, "command tool--deploy of bbb/tool is already provided by aaa/tool"), ShouldBeTrue)
			})
		})
	})
}

func TestPluginFarmCommandPrunedOnQuarantine(t *testing.T) {
	Convey("Given a farmed plugin command", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeCommand(t, plugin, "deploy", farmCommandDoc)

		f.sync(t)

		dir := openCodeCommandsDir(f.home)
		link := filepath.Join(dir, "tool--deploy.md")

		So(farmLink(t, dir, "tool--deploy.md"), ShouldEqual, pluginCurrentCommand(f, "acme", "deploy"))

		Convey("When the plugin cache disappears", func() {
			So(os.RemoveAll(plugin), ShouldBeNil)

			pruned := f.sync(t)

			Convey("Then the presented copy is pruned with a quarantine warning", func() {
				_, err := os.Stat(link)
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)

				So(containsWarning(pruned.Warnings, "is quarantined"), ShouldBeTrue)

				prunedCount := 0

				for _, result := range pruned.Farm {
					if result.Action == engine.FarmPruned && result.Plugin == "acme/tool" {
						prunedCount += result.Count
					}
				}

				So(prunedCount, ShouldEqual, 1)
			})
		})
	})
}

func TestPluginFarmCommandRenderedReservedMarketplace(t *testing.T) {
	Convey("Given a rendered command in a marketplace named like the artifact root", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.GeminiCLIID)

		So(os.MkdirAll(filepath.Join(f.home, ".gemini"), 0o750), ShouldBeNil)

		plugin := pluginTree(t, f.home, "farm", "current", "1.0.0")
		writeCommand(t, plugin, "deploy", farmCommandDoc)

		f.sync(t)

		link := filepath.Join(f.home, ".gemini", "commands", "current--deploy.toml")

		So(farmLink(t, filepath.Dir(link), "current--deploy.toml"), ShouldContainSubstring, "farm")

		Convey("When the plugin cache disappears", func() {
			So(os.RemoveAll(plugin), ShouldBeNil)

			pruned := f.sync(t)

			Convey("Then the rendered presentation is pruned like any other", func() {
				_, err := os.Stat(link)
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)

				So(containsWarning(pruned.Warnings, "is quarantined"), ShouldBeTrue)

				prunedCount := 0

				for _, result := range pruned.Farm {
					if result.Action == engine.FarmPruned && result.Plugin == "farm/current" {
						prunedCount += result.Count
					}
				}

				So(prunedCount, ShouldEqual, 2)
			})
		})
	})
}

func TestPluginFarmCommandInvalidNameSkipped(t *testing.T) {
	Convey("Given a plugin with names hosts cannot express", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeCommand(t, plugin, "-leading", farmCommandDoc)
		writeCommand(t, plugin, strings.Repeat("a", 200), farmCommandDoc)
		writeCommand(t, plugin, "deploy", farmCommandDoc)

		report := f.sync(t)

		Convey("When the farm runs", func() {
			Convey("Then the invalid names are skipped once with a warning", func() {
				So(containsWarning(report.Warnings, "is not a valid name"), ShouldBeTrue)

				dir := openCodeCommandsDir(f.home)

				_, err := os.Stat(filepath.Join(dir, "tool---leading.md"))
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)

				So(farmLink(t, dir, "tool--deploy.md"), ShouldEqual, pluginCurrentCommand(f, "acme", "deploy"))
			})
		})
	})
}

//nolint:dupl // the agent and command variants intentionally mirror each other
func TestFarmCommandDoctorIssues(t *testing.T) {
	Convey("Given a plugin with a nested command", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		write(t, filepath.Join(plugin, "commands", "team", "review.md"), farmCommandDoc)

		f.sync(t)

		Convey("When the doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the farm findings are visible without a sync", func() {
				So(hasIssue(issues, engine.SeverityWarn, "nested ids are skipped"), ShouldBeTrue)
			})
		})
	})
}
