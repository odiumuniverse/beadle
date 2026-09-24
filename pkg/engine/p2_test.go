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

func pinSkillPath(f *fixture, version, skill string) string {
	return filepath.Join(f.vault.PluginsDir(), "acme", "tool", "at-"+version, "skills", skill)
}

func currentSkillPath(f *fixture, skill string) string {
	return filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current", "skills", skill)
}

func pinPath(f *fixture, version string) string {
	return filepath.Join(f.vault.PluginsDir(), "acme", "tool", "at-"+version)
}

func pinVersion(t *testing.T, f *fixture, agentID, version string) {
	t.Helper()

	if err := f.config.SetPluginPin(agentID, "acme/tool", version); err != nil {
		t.Fatalf("set pin: %v", err)
	}

	if err := f.config.Save(f.vault.ConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

func readLink(t *testing.T, path string) string {
	t.Helper()

	link, err := os.Readlink(path)
	if err != nil {
		t.Fatalf("readlink %s: %v", path, err)
	}

	return link
}

func TestPluginPinFarmUsesPinnedPivot(t *testing.T) {
	Convey("Given a pinned plugin with two cached versions", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		v1 := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, v1, "alpha", "# v1\n")

		v2 := pluginTree(t, f.home, "acme", "tool", "2.0.0")
		writeSkill(t, v2, "alpha", "# v2\n")

		pinVersion(t, f, agent.OpenCodeID, "1.0.0")

		f.sync(t)

		claudeDir := claudeSkillsDir(f.home)
		openCodeDir := openCodeSkillsDir(f.home)

		Convey("When a newer version appears", func() {
			So(farmLink(t, openCodeDir, "alpha"), ShouldEqual, pinSkillPath(f, "1.0.0", "alpha"))
			So(farmLink(t, claudeDir, "alpha"), ShouldEqual, currentSkillPath(f, "alpha"))
			So(read(t, filepath.Join(openCodeDir, "alpha", "SKILL.md")), ShouldEqual, "# v1\n")
			So(read(t, filepath.Join(claudeDir, "alpha", "SKILL.md")), ShouldEqual, "# v2\n")
			So(readLink(t, pinPath(f, "1.0.0")), ShouldEqual, v1)

			v3 := pluginTree(t, f.home, "acme", "tool", "3.0.0")
			writeSkill(t, v3, "alpha", "# v3\n")

			f.sync(t)

			Convey("Then the pin holds and the unpinned agent follows current", func() {
				So(farmLink(t, openCodeDir, "alpha"), ShouldEqual, pinSkillPath(f, "1.0.0", "alpha"))
				So(farmLink(t, claudeDir, "alpha"), ShouldEqual, currentSkillPath(f, "alpha"))
				So(read(t, filepath.Join(openCodeDir, "alpha", "SKILL.md")), ShouldEqual, "# v1\n")
				So(read(t, filepath.Join(claudeDir, "alpha", "SKILL.md")), ShouldEqual, "# v3\n")
				So(readLink(t, pinPath(f, "1.0.0")), ShouldEqual, v1)
				So(readLink(t, filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current")), ShouldEqual, v3)
			})
		})
	})
}

func TestPluginPinMissingVersionIsSkipped(t *testing.T) {
	Convey("Given a pin to a version missing from the cache", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# v1\n")

		pinVersion(t, f, agent.OpenCodeID, "9.9.9")

		report := f.sync(t)

		entries, err := os.ReadDir(openCodeSkillsDir(f.home))
		So(err, ShouldBeNil)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When sync runs", func() {
			Convey("Then the pin is kept, the version is not substituted and doctor errors", func() {
				So(strings.Join(report.Warnings, " "), ShouldContainSubstring,
					"pinned version 9.9.9 of acme/tool is not in the plugin cache; keeping the pin (no silent upgrade)")
				So(farmLink(t, claudeSkillsDir(f.home), "alpha"), ShouldEqual, currentSkillPath(f, "alpha"))
				So(entries, ShouldBeEmpty)

				So(hasIssue(issues, engine.SeverityError, "9.9.9"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityError, agent.OpenCodeID), ShouldBeTrue)
			})
		})
	})
}

func TestPluginPinUnknownPluginWarns(t *testing.T) {
	Convey("Given a pin to a plugin that is not installed", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# v1\n")

		So(f.config.SetPluginPin(agent.OpenCodeID, "ghost/tool", "1.0.0"), ShouldBeNil)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When doctor runs", func() {
			Convey("Then it warns about the unknown plugin", func() {
				So(hasIssue(issues, engine.SeverityWarn, "ghost/tool is not installed"), ShouldBeTrue)
			})
		})
	})
}

func TestPluginPinRepointOnChange(t *testing.T) {
	Convey("Given a pin that changes version", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		v1 := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, v1, "alpha", "# v1\n")

		v2 := pluginTree(t, f.home, "acme", "tool", "2.0.0")
		writeSkill(t, v2, "alpha", "# v2\n")

		pinVersion(t, f, agent.OpenCodeID, "1.0.0")
		f.sync(t)

		openCodeDir := openCodeSkillsDir(f.home)
		So(farmLink(t, openCodeDir, "alpha"), ShouldEqual, pinSkillPath(f, "1.0.0", "alpha"))

		pinVersion(t, f, agent.OpenCodeID, "2.0.0")

		Convey("When sync runs after the pin moves", func() {
			f.sync(t)

			_, err := os.Lstat(pinPath(f, "1.0.0"))

			Convey("Then the link repoints and the old pivot is pruned", func() {
				So(farmLink(t, openCodeDir, "alpha"), ShouldEqual, pinSkillPath(f, "2.0.0", "alpha"))
				So(read(t, filepath.Join(openCodeDir, "alpha", "SKILL.md")), ShouldEqual, "# v2\n")
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestPluginPinUnpinReturnsToCurrent(t *testing.T) {
	Convey("Given a pin that is removed", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# v1\n")

		pinVersion(t, f, agent.OpenCodeID, "1.0.0")
		f.sync(t)

		openCodeDir := openCodeSkillsDir(f.home)
		So(farmLink(t, openCodeDir, "alpha"), ShouldEqual, pinSkillPath(f, "1.0.0", "alpha"))

		f.config.UnsetPluginPin(agent.OpenCodeID, "acme/tool")
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		Convey("When sync runs after unpinning", func() {
			f.sync(t)

			_, err := os.Lstat(pinPath(f, "1.0.0"))

			Convey("Then the link returns to current and the pivot is pruned", func() {
				So(farmLink(t, openCodeDir, "alpha"), ShouldEqual, currentSkillPath(f, "alpha"))
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestPluginPinMCPRoot(t *testing.T) {
	Convey("Given a pinned plugin exposing an MCP server", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		v1 := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeMCPServers(t, v1, `{"mcpServers": {"plug": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug", "args": ["serve"]}}}`)

		v2 := pluginTree(t, f.home, "acme", "tool", "2.0.0")
		writeMCPServers(t, v2, `{"mcpServers": {"plug": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug"}}}`)

		pinVersion(t, f, agent.OpenCodeID, "1.0.0")

		f.sync(t)

		openCode := hostMCPServers(t, f.openCodeConfig(), "mcp")

		Convey("When the MCP root is rendered per agent", func() {
			Convey("Then the pin resolves against its own pivot and the source host reads natively", func() {
				So(openCode["plug"]["command"], ShouldResemble, []any{filepath.Join(f.vault.PluginsDir(), "acme", "tool", "at-1.0.0", "bin", "plug"), "serve"})
				So(hostMCPServers(t, f.claudeConfig(), "mcpServers"), ShouldNotContainKey, "plug")
			})
		})
	})
}

func TestPluginPinDifferentSkillSet(t *testing.T) {
	Convey("Given a pin to a version with fewer skills", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		v1 := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, v1, "alpha", "# v1 alpha\n")

		v2 := pluginTree(t, f.home, "acme", "tool", "2.0.0")
		writeSkill(t, v2, "alpha", "# v2 alpha\n")
		writeSkill(t, v2, "beta", "# v2 beta\n")

		pinVersion(t, f, agent.OpenCodeID, "1.0.0")

		report := f.sync(t)

		claudeDir := claudeSkillsDir(f.home)
		openCodeDir := openCodeSkillsDir(f.home)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When sync runs", func() {
			Convey("Then a skill absent from the pin is skipped and never linked", func() {
				So(farmLink(t, openCodeDir, "alpha"), ShouldEqual, pinSkillPath(f, "1.0.0", "alpha"))
				So(farmLink(t, claudeDir, "alpha"), ShouldEqual, currentSkillPath(f, "alpha"))
				So(farmLink(t, claudeDir, "beta"), ShouldEqual, currentSkillPath(f, "beta"))

				_, err := os.Lstat(filepath.Join(openCodeDir, "beta"))
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)
				So(strings.Join(report.Warnings, " "), ShouldContainSubstring, "skill beta is missing in acme/tool@1.0.0; skipped")

				for _, issue := range issues {
					So(issue.Message, ShouldNotContainSubstring, "beta")
				}
			})
		})
	})
}

func TestPluginPinDropsSkillMissingInPinnedVersion(t *testing.T) {
	Convey("Given a link to a skill absent from the pinned version", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		v1 := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, v1, "alpha", "# v1 alpha\n")

		v2 := pluginTree(t, f.home, "acme", "tool", "2.0.0")
		writeSkill(t, v2, "alpha", "# v2 alpha\n")
		writeSkill(t, v2, "beta", "# v2 beta\n")

		f.sync(t)

		openCodeDir := openCodeSkillsDir(f.home)
		So(farmLink(t, openCodeDir, "beta"), ShouldEqual, currentSkillPath(f, "beta"))

		pinVersion(t, f, agent.OpenCodeID, "1.0.0")

		Convey("When the pin drops the skill", func() {
			f.sync(t)

			_, err := os.Lstat(filepath.Join(openCodeDir, "beta"))

			Convey("Then the stale link is pruned and the other agent keeps it", func() {
				So(farmLink(t, openCodeDir, "alpha"), ShouldEqual, pinSkillPath(f, "1.0.0", "alpha"))
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)
				So(farmLink(t, claudeSkillsDir(f.home), "beta"), ShouldEqual, currentSkillPath(f, "beta"))
			})
		})
	})
}

func TestPluginPinMissingVersionDropsStaleLinks(t *testing.T) {
	Convey("Given an unresolvable pin after a working sync", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# v1\n")
		writeMCPServers(t, plugin, `{"mcpServers": {"plug": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug"}}}`)

		f.sync(t)

		openCodeDir := openCodeSkillsDir(f.home)
		So(farmLink(t, openCodeDir, "alpha"), ShouldEqual, currentSkillPath(f, "alpha"))
		So(hostMCPServers(t, f.openCodeConfig(), "mcp"), ShouldContainKey, "plug")

		pinVersion(t, f, agent.OpenCodeID, "9.9.9")

		Convey("When the pin cannot be resolved", func() {
			f.sync(t)

			entries, err := os.ReadDir(openCodeDir)
			So(err, ShouldBeNil)

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then stale links and servers do not survive as silent upgrades", func() {
				So(entries, ShouldBeEmpty)
				So(hostMCPServers(t, f.openCodeConfig(), "mcp"), ShouldNotContainKey, "plug")
				So(hasIssue(issues, engine.SeverityError, "9.9.9"), ShouldBeTrue)
			})
		})
	})
}

func TestPluginPinHealRepoints(t *testing.T) {
	Convey("Given a foreign link where a pinned skill belongs", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		v1 := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, v1, "alpha", "# v1\n")

		pinVersion(t, f, agent.OpenCodeID, "1.0.0")
		f.sync(t)

		openCodeDir := openCodeSkillsDir(f.home)
		So(os.Remove(filepath.Join(openCodeDir, "alpha")), ShouldBeNil)

		versionedLink(t, openCodeDir, "alpha", filepath.Join(v1, "skills", "alpha"))

		Convey("When heal runs", func() {
			results, err := f.engine.Heal(t.Context(), false)
			So(err, ShouldBeNil)

			Convey("Then the link is repointed at the pinned pivot", func() {
				So(results, ShouldNotBeEmpty)
				So(farmLink(t, openCodeDir, "alpha"), ShouldEqual, pinSkillPath(f, "1.0.0", "alpha"))
			})
		})
	})
}
