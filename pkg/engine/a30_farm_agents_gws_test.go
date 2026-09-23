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
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/subagent"
)

func writeAgent(t *testing.T, pluginDir, name, body string) {
	t.Helper()

	write(t, filepath.Join(pluginDir, "agents", name+".md"), body)
}

func pluginCurrentAgent(f *fixture, name string) string {
	return filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current", "agents", name+".md")
}

func openCodeAgentsDir(home string) string {
	return filepath.Join(home, ".config", "opencode", "agents")
}

const farmAgentDoc = `---
name: alpha
description: Test agent
---

# alpha
`

func TestPluginFarmPresentsAgents(t *testing.T) {
	Convey("Given a plugin with two agents", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeAgent(t, plugin, "alpha", farmAgentDoc)
		writeAgent(t, plugin, "beta", farmAgentDoc)

		report := f.sync(t)

		Convey("When the farm runs", func() {
			Convey("Then the agents are presented to OpenCode and Claude is skipped", func() {
				dir := openCodeAgentsDir(f.home)

				So(farmLink(t, dir, "tool--alpha.md"), ShouldEqual, pluginCurrentAgent(f, "alpha"))
				So(farmLink(t, dir, "tool--beta.md"), ShouldEqual, pluginCurrentAgent(f, "beta"))

				_, claudeErr := os.Stat(filepath.Join(f.home, ".claude", "agents"))
				So(errors.Is(claudeErr, fs.ErrNotExist), ShouldBeTrue)

				linked := 0

				for _, result := range report.Farm {
					if result.Action == engine.FarmLinked && result.Plugin == "acme/tool" {
						linked += result.Count
					}
				}

				So(linked, ShouldEqual, 2)
			})
		})
	})
}

func TestPluginFarmAgentsInvisibleToCanon(t *testing.T) {
	Convey("Given a farmed plugin agent", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeAgent(t, plugin, "alpha", farmAgentDoc)

		f.sync(t)

		dir := openCodeAgentsDir(f.home)

		report := f.sync(t)

		Convey("When sync runs again", func() {
			Convey("Then it is a noop and the agent never reaches the canon", func() {
				entries, err := os.ReadDir(f.vault.SubagentsDir())
				So(err, ShouldBeNil)

				So(report.Farm, ShouldBeEmpty)
				So(entries, ShouldBeEmpty)
				So(farmLink(t, dir, "tool--alpha.md"), ShouldEqual, pluginCurrentAgent(f, "alpha"))
			})
		})
	})
}

func TestPluginFarmAgentCanonWins(t *testing.T) {
	Convey("Given a canon agent with the namespaced plugin name", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, filepath.Join(f.vault.SubagentsDir(), "tool--alpha.md"), string(subagent.Render(subagent.Document{
			Name:        "tool--alpha",
			Description: "Canon agent",
		})))

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeAgent(t, plugin, "alpha", farmAgentDoc)

		report := f.sync(t)

		Convey("When the farm runs", func() {
			Convey("Then the canon copy wins and the plugin entry is skipped", func() {
				So(containsWarning(report.Warnings, "shadowed by the vault canon"), ShouldBeTrue)

				for _, result := range report.Farm {
					So(result.Action, ShouldNotEqual, engine.FarmLinked)
				}

				// The canon agent itself is delivered by the kind sync; the
				// farm must not have replaced it with the plugin copy.
				So(read(t, filepath.Join(openCodeAgentsDir(f.home), "tool--alpha.md")), ShouldContainSubstring, "Canon agent")
			})
		})
	})
}

//nolint:dupl // the agent and command variants intentionally mirror each other
func TestPluginFarmAgentNestedSkipped(t *testing.T) {
	Convey("Given a plugin with a nested agent", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		write(t, filepath.Join(plugin, "agents", "team", "review.md"), farmAgentDoc)

		report := f.sync(t)

		Convey("When the farm runs", func() {
			Convey("Then the nested definition is skipped with a warning", func() {
				So(containsWarning(report.Warnings, "agent directory team"), ShouldBeTrue)
				So(containsWarning(report.Warnings, "nested ids are skipped"), ShouldBeTrue)

				entries, err := os.ReadDir(openCodeAgentsDir(f.home))
				So(errors.Is(err, fs.ErrNotExist) || len(entries) == 0, ShouldBeTrue)
			})
		})
	})
}

func TestPluginFarmAgentPrunedOnQuarantine(t *testing.T) {
	Convey("Given a farmed plugin agent", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeAgent(t, plugin, "alpha", farmAgentDoc)

		f.sync(t)

		dir := openCodeAgentsDir(f.home)
		link := filepath.Join(dir, "tool--alpha.md")

		So(farmLink(t, dir, "tool--alpha.md"), ShouldEqual, pluginCurrentAgent(f, "alpha"))

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

func TestPluginFarmKeepsForeignAgent(t *testing.T) {
	Convey("Given a user agent in the host directory", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		dir := openCodeAgentsDir(f.home)
		write(t, filepath.Join(dir, "custom.md"), "# mine\n")

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeAgent(t, plugin, "alpha", farmAgentDoc)

		f.sync(t)

		Convey("When the farm runs", func() {
			Convey("Then the foreign agent is untouched", func() {
				So(read(t, filepath.Join(dir, "custom.md")), ShouldEqual, "# mine\n")
				So(farmLink(t, dir, "tool--alpha.md"), ShouldEqual, pluginCurrentAgent(f, "alpha"))
			})
		})
	})
}

func TestPluginFarmAgentNamespaceCollision(t *testing.T) {
	Convey("Given two plugins providing the same agent name", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		first := pluginTree(t, f.home, "aaa", "tool", "1.0.0")
		writeAgent(t, first, "alpha", farmAgentDoc)

		second := pluginTree(t, f.home, "zzz", "tool", "1.0.0")
		writeAgent(t, second, "alpha", farmAgentDoc)

		report := f.sync(t)

		Convey("When the farm runs", func() {
			Convey("Then the first key wins and the loser is reported", func() {
				want := filepath.Join(f.vault.PluginsDir(), "aaa", "tool", "current", "agents", "alpha.md")
				So(farmLink(t, openCodeAgentsDir(f.home), "tool--alpha.md"), ShouldEqual, want)

				So(containsWarning(report.Warnings, "is already provided by aaa/tool"), ShouldBeTrue)
			})
		})
	})
}

func TestPluginFarmAgentArtifactsPruned(t *testing.T) {
	Convey("Given a Codex-rendered plugin agent", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		f.config.Enable(agent.CodexID)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		write(t, filepath.Join(f.home, ".codex", "config.toml"), "")

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeAgent(t, plugin, "alpha", farmAgentDoc)

		f.sync(t)

		link := filepath.Join(f.home, ".codex", "agents", "tool--alpha.toml")
		artifactDir := filepath.Join(f.vault.PluginsDir(), "farm", "acme", "tool")

		So(farmLink(t, filepath.Dir(link), "tool--alpha.toml"), ShouldContainSubstring, "farm")

		Convey("When the plugin disappears", func() {
			So(os.RemoveAll(plugin), ShouldBeNil)

			plugins := readRegistry(t, f.home)
			delete(plugins, "tool@acme")
			writeRegistry(t, f.home, plugins)

			report := f.sync(t)

			Convey("Then the presentation and the vault artifacts are gone", func() {
				_, linkErr := os.Stat(link)
				So(errors.Is(linkErr, fs.ErrNotExist), ShouldBeTrue)

				_, dirErr := os.Stat(artifactDir)
				So(errors.Is(dirErr, fs.ErrNotExist), ShouldBeTrue)

				So(containsWarning(report.Warnings, "pivot is not retired"), ShouldBeFalse)
			})
		})
	})
}

//nolint:dupl // the agent and command variants intentionally mirror each other
func TestFarmAgentDoctorIssues(t *testing.T) {
	Convey("Given a plugin with a nested agent", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		write(t, filepath.Join(plugin, "agents", "team", "review.md"), farmAgentDoc)

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

func TestPluginFarmAgentPinnedMissingPrunes(t *testing.T) {
	Convey("Given a farmed plugin agent", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeAgent(t, plugin, "alpha", farmAgentDoc)

		f.sync(t)

		dir := openCodeAgentsDir(f.home)
		link := filepath.Join(dir, "tool--alpha.md")

		So(farmLink(t, dir, "tool--alpha.md"), ShouldContainSubstring, "current")

		Convey("When the plugin is pinned to a version the cache does not hold", func() {
			So(f.config.SetPluginPin(agent.OpenCodeID, "acme/tool", "9.9.9"), ShouldBeNil)
			So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

			report := f.sync(t)

			Convey("Then the dangling presentation is pruned, not reported forever", func() {
				_, err := os.Stat(link)
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)

				kr := report.Kind(kind.Subagents)
				So(kr, ShouldNotBeNil)
				So(strings.Join(kr.Warnings, "\n"), ShouldNotContainSubstring, "broken symlink")
			})
		})
	})
}

func TestPluginFarmAgentCanonWinsWithBrokenPin(t *testing.T) {
	Convey("Given a farmed plugin agent", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeAgent(t, plugin, "alpha", farmAgentDoc)

		f.sync(t)

		dir := openCodeAgentsDir(f.home)
		link := filepath.Join(dir, "tool--alpha.md")

		So(farmLink(t, dir, "tool--alpha.md"), ShouldContainSubstring, "current")

		Convey("When the canon takes the name and the pin cannot resolve", func() {
			write(t, filepath.Join(f.vault.SubagentsDir(), "tool--alpha.md"), string(subagent.Render(subagent.Document{
				Name:        "tool--alpha",
				Description: "Canon agent",
			})))

			So(f.config.SetPluginPin(agent.OpenCodeID, "acme/tool", "9.9.9"), ShouldBeNil)
			So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

			f.sync(t)

			Convey("Then the canon file is delivered instead of the held link", func() {
				info, err := os.Lstat(link)
				So(err, ShouldBeNil)
				So(info.Mode()&fs.ModeSymlink == 0, ShouldBeTrue)
				So(read(t, link), ShouldContainSubstring, "Canon agent")
			})
		})
	})
}

func TestPluginFarmAgentReservedArtifactRoot(t *testing.T) {
	Convey("Given a plugin named like a pivot entry and an enabled Codex", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		f.config.Enable(agent.CodexID)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		write(t, filepath.Join(f.home, ".codex", "config.toml"), "")

		plugin := pluginTree(t, f.home, "acme", "current", "1.0.0")
		writeAgent(t, plugin, "alpha", farmAgentDoc)

		f.sync(t)

		artifactDir := filepath.Join(f.vault.PluginsDir(), "farm", "acme", "current")

		info, err := os.Stat(artifactDir)
		So(err, ShouldBeNil)
		So(info.IsDir(), ShouldBeTrue)

		Convey("When the plugin disappears without a trace", func() {
			So(os.RemoveAll(plugin), ShouldBeNil)
			So(os.Remove(f.vault.PluginsLedgerPath()), ShouldBeNil)

			plugins := readRegistry(t, f.home)
			delete(plugins, "current@acme")
			writeRegistry(t, f.home, plugins)

			report := f.sync(t)

			Convey("Then the artifact root is not mistaken for an orphan pivot", func() {
				warnings := strings.Join(report.Warnings, "\n")
				So(warnings, ShouldNotContainSubstring, "holds regular files")
				So(warnings, ShouldNotContainSubstring, "pivot is not retired")

				_, err := os.Stat(artifactDir)
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestPluginFarmAgentCodexRendered(t *testing.T) {
	Convey("Given a plugin agent and an enabled Codex", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		f.config.Enable(agent.CodexID)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		write(t, filepath.Join(f.home, ".codex", "config.toml"), "")

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeAgent(t, plugin, "alpha", farmAgentDoc)

		f.sync(t)

		Convey("When the farm runs", func() {
			Convey("Then Codex gets a rendered TOML copy with the ownership marker", func() {
				path := filepath.Join(f.home, ".codex", "agents", "tool--alpha.toml")

				target := farmLink(t, filepath.Dir(path), "tool--alpha.toml")
				So(target, ShouldContainSubstring, filepath.Join("plugins", "farm", "acme", "tool"))

				info, err := os.Lstat(filepath.Join(f.home, ".codex", "agents", "tool--alpha.toml"))
				So(err, ShouldBeNil)
				So(info.Mode()&fs.ModeSymlink != 0, ShouldBeTrue)

				data, err := os.ReadFile(target) //nolint:gosec // the test reads its own temp file
				So(err, ShouldBeNil)
				So(string(data), ShouldContainSubstring, "developer_instructions")
				So(string(data), ShouldContainSubstring, "tool--alpha")

				Convey("And a second sync keeps the file byte for byte", func() {
					before, err := os.ReadFile(target) //nolint:gosec // the test reads its own temp file
					So(err, ShouldBeNil)

					report := f.sync(t)

					after, err := os.ReadFile(target) //nolint:gosec // the test reads its own temp file
					So(err, ShouldBeNil)
					So(strings.Join(report.Warnings, "\n"), ShouldNotContainSubstring, "plugin farm")
					So(string(after), ShouldEqual, string(before))
				})
			})
		})
	})
}
