package engine_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/skill"
)

func claudeSkillsDir(home string) string {
	return filepath.Join(home, ".claude", "skills")
}

func openCodeSkillsDir(home string) string {
	return filepath.Join(home, ".config", "opencode", "skills")
}

func sharedSkillsDir(home string) string {
	return filepath.Join(home, ".agents", "skills")
}

func writeSkill(t *testing.T, pluginDir, name, content string) {
	t.Helper()

	write(t, filepath.Join(pluginDir, "skills", name, "SKILL.md"), content)
}

func farmLink(t *testing.T, dir, name string) string {
	t.Helper()

	link, err := os.Readlink(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("readlink %s/%s: %v", dir, name, err)
	}

	return link
}

func inode(t *testing.T, path string) uint64 {
	t.Helper()

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("not a Stat_t")
	}

	return stat.Ino
}

func isStub(t *testing.T, path string) bool {
	t.Helper()

	_, ok := skill.IsStubDir(path)

	return ok
}

func containsWarning(warnings []string, substr string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, substr) {
			return true
		}
	}

	return false
}

func pluginCurrentSkill(f *fixture, name string) string {
	return filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current", "skills", name)
}

func TestPluginFarmPresentsSkills(t *testing.T) {
	Convey("Given a plugin with two skills", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")
		writeSkill(t, plugin, "beta", "# beta\n")

		report := f.sync(t)

		wantAlpha := pluginCurrentSkill(f, "alpha")
		wantBeta := pluginCurrentSkill(f, "beta")

		entries, err := os.ReadDir(f.vault.SkillsDir())
		So(err, ShouldBeNil)

		Convey("When the farm runs", func() {
			Convey("Then each host links the skills and the canon is untouched", func() {
				for _, dir := range []string{claudeSkillsDir(f.home), openCodeSkillsDir(f.home)} {
					So(farmLink(t, dir, "alpha"), ShouldEqual, wantAlpha)
					So(farmLink(t, dir, "beta"), ShouldEqual, wantBeta)
					So(read(t, filepath.Join(dir, "alpha", "SKILL.md")), ShouldEqual, "# alpha\n")
				}

				So(report.Farm, ShouldHaveLength, 2)

				for _, result := range report.Farm {
					So(result.Action, ShouldEqual, engine.FarmLinked)
					So(result.Plugin, ShouldEqual, "acme/tool")
					So(result.Count, ShouldEqual, 2)
				}

				So(entries, ShouldBeEmpty)
			})
		})
	})
}

func TestPluginFarmInvisibleToSync(t *testing.T) {
	Convey("Given a farmed plugin", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		f.sync(t)

		ledger := read(t, f.vault.PluginsLedgerPath())

		report := f.sync(t)

		Convey("When sync runs again", func() {
			_, canonErr := os.Stat(filepath.Join(f.vault.SkillsDir(), "alpha"))

			Convey("Then it is a noop and never reaches the canon", func() {
				So(report.Farm, ShouldBeEmpty)
				So(report.Action(kind.Skills, agent.ClaudeCodeID), ShouldEqual, engine.ActionNoop)
				So(read(t, f.vault.PluginsLedgerPath()), ShouldEqual, ledger)
				So(errors.Is(canonErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestPluginFarmSurvivesUpgrade(t *testing.T) {
	Convey("Given a plugin that is upgraded", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha v1\n")

		f.sync(t)

		linkBefore := farmLink(t, claudeSkillsDir(f.home), "alpha")

		upgraded := pluginTree(t, f.home, "acme", "tool", "2.0.0")
		writeSkill(t, upgraded, "alpha", "# alpha v2\n")

		report := f.sync(t)

		Convey("When it upgrades", func() {
			Convey("Then the link is stable and content updates", func() {
				So(report.Farm, ShouldBeEmpty)
				So(farmLink(t, claudeSkillsDir(f.home), "alpha"), ShouldEqual, linkBefore)
				So(farmLink(t, openCodeSkillsDir(f.home), "alpha"), ShouldEqual, linkBefore)
				So(read(t, filepath.Join(claudeSkillsDir(f.home), "alpha", "SKILL.md")), ShouldEqual, "# alpha v2\n")
			})
		})
	})
}

func TestPluginFarmPrunesDroppedSkill(t *testing.T) {
	Convey("Given a skill dropped by an upgrade", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")
		writeSkill(t, plugin, "beta", "# beta\n")

		f.sync(t)

		upgraded := pluginTree(t, f.home, "acme", "tool", "2.0.0")
		writeSkill(t, upgraded, "beta", "# beta\n")

		report := f.sync(t)

		_, alphaClaude := os.Stat(filepath.Join(claudeSkillsDir(f.home), "alpha"))
		_, alphaOpen := os.Stat(filepath.Join(openCodeSkillsDir(f.home), "alpha"))

		Convey("When the farm reconciles", func() {
			Convey("Then the dropped skill is pruned on both hosts", func() {
				So(errors.Is(alphaClaude, fs.ErrNotExist), ShouldBeTrue)
				So(errors.Is(alphaOpen, fs.ErrNotExist), ShouldBeTrue)
				So(farmLink(t, claudeSkillsDir(f.home), "beta"), ShouldEqual, pluginCurrentSkill(f, "beta"))

				So(report.Farm, ShouldHaveLength, 2)

				for _, result := range report.Farm {
					So(result.Action, ShouldEqual, engine.FarmPruned)
					So(result.Count, ShouldEqual, 1)
				}
			})
		})
	})
}

func TestPluginFarmCollisions(t *testing.T) {
	Convey("Given colliding farmed skills", t, func() {
		Convey("When two plugins provide the same skill, the first by key wins", func() {
			t.Setenv("XDG_CONFIG_HOME", "")

			f := newFixture(t)
			f.emptyConfigs(t)

			first := pluginTree(t, f.home, "acme", "tool", "1.0.0")
			writeSkill(t, first, "alpha", "# first\n")

			second := pluginTree(t, f.home, "beta", "other", "1.0.0")
			writeSkill(t, second, "alpha", "# second\n")

			report := f.sync(t)

			So(farmLink(t, claudeSkillsDir(f.home), "alpha"), ShouldEqual, pluginCurrentSkill(f, "alpha"))
			So(containsWarning(report.Warnings, "already provided by acme/tool"), ShouldBeTrue)
		})

		Convey("When the canon also owns the name, the canon wins in the same sync", func() {
			t.Setenv("XDG_CONFIG_HOME", "")

			f := newFixture(t)
			f.emptyConfigs(t)

			plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
			writeSkill(t, plugin, "alpha", "# plugin\n")

			f.sync(t)
			So(farmLink(t, claudeSkillsDir(f.home), "alpha"), ShouldEqual, pluginCurrentSkill(f, "alpha"))

			write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# canon\n")

			report := f.sync(t)

			info, err := os.Lstat(filepath.Join(claudeSkillsDir(f.home), "alpha"))
			So(err, ShouldBeNil)

			var pruned []engine.FarmResult

			for _, result := range report.Farm {
				if result.Action == engine.FarmPruned {
					pruned = append(pruned, result)
				}
			}

			So(info.Mode()&fs.ModeSymlink, ShouldEqual, fs.FileMode(0))
			So(read(t, filepath.Join(claudeSkillsDir(f.home), "alpha", "SKILL.md")), ShouldEqual, "# canon\n")
			So(pruned, ShouldHaveLength, 2)
		})

		Convey("When the canon blocks the farm before the first sync", func() {
			t.Setenv("XDG_CONFIG_HOME", "")

			f := newFixture(t)
			f.emptyConfigs(t)

			plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
			writeSkill(t, plugin, "alpha", "# plugin\n")

			write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# canon\n")

			report := f.sync(t)

			info, err := os.Lstat(filepath.Join(claudeSkillsDir(f.home), "alpha"))
			So(err, ShouldBeNil)

			So(info.Mode()&fs.ModeSymlink, ShouldEqual, fs.FileMode(0))
			So(read(t, filepath.Join(claudeSkillsDir(f.home), "alpha", "SKILL.md")), ShouldEqual, "# canon\n")
			So(report.Farm, ShouldBeEmpty)
			So(containsWarning(report.Warnings, "shadowed by the vault canon"), ShouldBeTrue)
		})

		Convey("When foreign entries exist in the target dir, they are never touched", func() {
			t.Setenv("XDG_CONFIG_HOME", "")

			f := newFixture(t)
			f.emptyConfigs(t)

			plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
			writeSkill(t, plugin, "alpha", "# plugin alpha\n")
			writeSkill(t, plugin, "beta", "# plugin beta\n")

			claudeDir := claudeSkillsDir(f.home)
			So(os.MkdirAll(claudeDir, 0o700), ShouldBeNil)

			foreignTarget := filepath.Join(f.home, "elsewhere", "alpha")
			write(t, filepath.Join(foreignTarget, "SKILL.md"), "# foreign\n")
			So(os.Symlink(foreignTarget, filepath.Join(claudeDir, "alpha")), ShouldBeNil)

			realDir := filepath.Join(claudeDir, "beta")
			write(t, filepath.Join(realDir, "SKILL.md"), "# real\n")

			realInode := inode(t, realDir)

			report := f.sync(t)

			link, err := os.Readlink(filepath.Join(claudeDir, "alpha"))
			So(err, ShouldBeNil)

			var skipped []engine.FarmResult

			for _, result := range report.Farm {
				if result.Action == engine.FarmSkipped {
					skipped = append(skipped, result)
				}
			}

			So(link, ShouldEqual, foreignTarget)
			So(inode(t, realDir), ShouldEqual, realInode)
			So(skipped, ShouldHaveLength, 1)
			So(skipped[0].Note, ShouldEqual, "alpha, beta")
		})
	})
}

func TestPluginFarmStubsOnQuarantine(t *testing.T) {
	Convey("Given a quarantined plugin alongside foreign entries", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		f.sync(t)

		foreignDir := filepath.Join(claudeSkillsDir(f.home), "Foreign")
		write(t, filepath.Join(foreignDir, "SKILL.md"), "# foreign\n")

		foreignTarget := filepath.Join(f.home, "elsewhere", "helper")
		write(t, filepath.Join(foreignTarget, "SKILL.md"), "# helper\n")
		So(os.Symlink(foreignTarget, filepath.Join(claudeSkillsDir(f.home), "Helper")), ShouldBeNil)

		So(os.RemoveAll(plugin), ShouldBeNil)

		report := f.sync(t)

		Convey("When sync stubs the hosts", func() {
			So(report.Farm, ShouldHaveLength, 2)

			for _, result := range report.Farm {
				So(result.Action, ShouldEqual, engine.FarmStubbed)
				So(result.Plugin, ShouldEqual, "acme/tool")
				So(result.Count, ShouldEqual, 1)
			}

			for _, dir := range []string{claudeSkillsDir(f.home), openCodeSkillsDir(f.home)} {
				key, ok := skill.IsStubDir(filepath.Join(dir, "alpha"))
				So(ok, ShouldBeTrue)
				So(key, ShouldEqual, "acme/tool")
			}

			So(read(t, filepath.Join(foreignDir, "SKILL.md")), ShouldEqual, "# foreign\n")

			link, err := os.Readlink(filepath.Join(claudeSkillsDir(f.home), "Helper"))
			So(err, ShouldBeNil)
			So(link, ShouldEqual, foreignTarget)

			entries, err := os.ReadDir(f.vault.SkillsDir())
			So(err, ShouldBeNil)

			before := read(t, f.vault.PluginsLedgerPath())

			report = f.sync(t)

			Convey("Then a repeated sync is quiet and leaves the ledger alone", func() {
				So(report.Farm, ShouldBeEmpty)
				So(read(t, f.vault.PluginsLedgerPath()), ShouldEqual, before)
				So(entries, ShouldBeEmpty)
			})
		})
	})
}

func TestPluginFarmReplacesStubOnReinstall(t *testing.T) {
	Convey("Given a stub left by a quarantine", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		f.sync(t)

		So(os.RemoveAll(plugin), ShouldBeNil)

		f.sync(t)
		So(isStub(t, filepath.Join(claudeSkillsDir(f.home), "alpha")), ShouldBeTrue)

		reinstalled := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, reinstalled, "alpha", "# alpha\n")

		report := f.sync(t)

		entries, err := os.ReadDir(f.vault.SkillsDir())
		So(err, ShouldBeNil)

		Convey("When it is reinstalled", func() {
			Convey("Then the stub is replaced by a fresh link", func() {
				for _, result := range report.Farm {
					So(result.Action, ShouldNotEqual, engine.FarmStubbed)
				}

				for _, dir := range []string{claudeSkillsDir(f.home), openCodeSkillsDir(f.home)} {
					So(isStub(t, filepath.Join(dir, "alpha")), ShouldBeFalse)
					So(farmLink(t, dir, "alpha"), ShouldEqual, pluginCurrentSkill(f, "alpha"))
				}

				So(entries, ShouldBeEmpty)
			})
		})
	})
}

func TestPluginFarmPrunesOrphanStub(t *testing.T) {
	Convey("Given an orphan stub after an upgrade drops the skill", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")
		writeSkill(t, plugin, "beta", "# beta\n")

		f.sync(t)

		So(os.RemoveAll(plugin), ShouldBeNil)

		f.sync(t)
		So(isStub(t, filepath.Join(claudeSkillsDir(f.home), "alpha")), ShouldBeTrue)

		upgraded := pluginTree(t, f.home, "acme", "tool", "2.0.0")
		writeSkill(t, upgraded, "beta", "# beta\n")

		report := f.sync(t)

		_, alphaClaude := os.Stat(filepath.Join(claudeSkillsDir(f.home), "alpha"))
		_, alphaOpen := os.Stat(filepath.Join(openCodeSkillsDir(f.home), "alpha"))

		var pruned int

		for _, result := range report.Farm {
			if result.Action == engine.FarmPruned {
				pruned++
			}
		}

		Convey("When the farm reconciles", func() {
			Convey("Then the orphan stub is pruned on both hosts", func() {
				So(errors.Is(alphaClaude, fs.ErrNotExist), ShouldBeTrue)
				So(errors.Is(alphaOpen, fs.ErrNotExist), ShouldBeTrue)
				So(farmLink(t, claudeSkillsDir(f.home), "beta"), ShouldEqual, pluginCurrentSkill(f, "beta"))
				So(farmLink(t, openCodeSkillsDir(f.home), "beta"), ShouldEqual, pluginCurrentSkill(f, "beta"))
				So(pruned, ShouldEqual, 2)
			})
		})
	})
}

func TestPluginFarmStubSkipsShadowedName(t *testing.T) {
	Convey("Given a name shadowed at stub time", t, func() {
		Convey("When another parked plugin owns the name, the winner takes it instead of a stub", func() {
			t.Setenv("XDG_CONFIG_HOME", "")

			f := newFixture(t)
			f.emptyConfigs(t)

			loser := pluginTree(t, f.home, "aaa", "loser", "1.0.0")
			writeSkill(t, loser, "shared", "# loser\n")

			winner := pluginTree(t, f.home, "bbb", "winner", "1.0.0")
			writeSkill(t, winner, "shared", "# winner\n")

			f.sync(t)

			So(farmLink(t, claudeSkillsDir(f.home), "shared"), ShouldEqual, filepath.Join(f.vault.PluginsDir(), "aaa", "loser", "current", "skills", "shared"))

			So(os.RemoveAll(loser), ShouldBeNil)

			report := f.sync(t)

			So(isStub(t, filepath.Join(claudeSkillsDir(f.home), "shared")), ShouldBeFalse)
			So(read(t, filepath.Join(claudeSkillsDir(f.home), "shared", "SKILL.md")), ShouldEqual, "# winner\n")

			for _, result := range report.Farm {
				So(result.Action, ShouldNotEqual, engine.FarmStubbed)
			}
		})

		Convey("When the canon owns the name, the canon lands instead of a stub", func() {
			t.Setenv("XDG_CONFIG_HOME", "")

			f := newFixture(t)
			f.emptyConfigs(t)

			plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
			writeSkill(t, plugin, "alpha", "# plugin\n")

			f.sync(t)

			write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# canon\n")

			So(os.RemoveAll(plugin), ShouldBeNil)

			report := f.sync(t)

			So(isStub(t, filepath.Join(claudeSkillsDir(f.home), "alpha")), ShouldBeFalse)
			So(read(t, filepath.Join(claudeSkillsDir(f.home), "alpha", "SKILL.md")), ShouldEqual, "# canon\n")

			for _, result := range report.Farm {
				So(result.Action, ShouldNotEqual, engine.FarmStubbed)
			}
		})
	})
}

func TestPluginFarmStubSymlinkNotReadAsSkill(t *testing.T) {
	Convey("Given a symlink pointing at a stub", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		f.sync(t)

		So(os.RemoveAll(plugin), ShouldBeNil)

		f.sync(t)

		stub := filepath.Join(claudeSkillsDir(f.home), "alpha")
		So(isStub(t, stub), ShouldBeTrue)

		So(os.Symlink(stub, filepath.Join(claudeSkillsDir(f.home), "helper")), ShouldBeNil)

		f.sync(t)

		entries, err := os.ReadDir(f.vault.SkillsDir())
		So(err, ShouldBeNil)

		Convey("When sync runs", func() {
			_, helperErr := os.Stat(filepath.Join(f.vault.SkillsDir(), "helper", "SKILL.md"))

			Convey("Then it never leaks into the canon", func() {
				So(errors.Is(helperErr, fs.ErrNotExist), ShouldBeTrue)
				So(entries, ShouldBeEmpty)
			})
		})
	})
}

func TestPluginFarmRemoval(t *testing.T) {
	Convey("Given a plugin removed from the registry", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		f.sync(t)

		removeFromRegistry(t, f.home, "acme", "tool")

		report := f.sync(t)

		Convey("When the registry drops it while the cache stays", func() {
			Convey("Then the farm links are pruned without a stub", func() {
				for _, result := range report.Farm {
					So(result.Action, ShouldEqual, engine.FarmPruned)
					So(result.Plugin, ShouldEqual, "acme/tool")
					So(result.Count, ShouldEqual, 1)
				}

				for _, dir := range []string{claudeSkillsDir(f.home), openCodeSkillsDir(f.home)} {
					_, linkErr := os.Lstat(filepath.Join(dir, "alpha"))
					So(errors.Is(linkErr, fs.ErrNotExist), ShouldBeTrue)
				}

				Convey("And removing the cache keeps it retired, never stubbed", func() {
					So(os.RemoveAll(plugin), ShouldBeNil)

					report = f.sync(t)

					issues, err := f.engine.Doctor(t.Context())
					So(err, ShouldBeNil)

					So(report.Farm, ShouldBeEmpty)

					for _, dir := range []string{claudeSkillsDir(f.home), openCodeSkillsDir(f.home)} {
						_, linkErr := os.Lstat(filepath.Join(dir, "alpha"))
						So(errors.Is(linkErr, fs.ErrNotExist), ShouldBeTrue)
					}

					So(hasIssue(issues, engine.SeverityError, "is quarantined"), ShouldBeFalse)
					So(hasIssue(issues, engine.SeverityError, "broken symlink"), ShouldBeFalse)
					So(hasIssue(issues, engine.SeverityInfo, "was removed from its host registry"), ShouldBeTrue)
				})
			})
		})
	})
}

func TestPluginFarmDryRun(t *testing.T) {
	Convey("Given a dry run", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		report := f.run(t, engine.SyncOptions{DryRun: true})

		_, claudeErr := os.Stat(claudeSkillsDir(f.home))
		_, openErr := os.Stat(openCodeSkillsDir(f.home))

		Convey("When it previews", func() {
			Convey("Then no host dir is created", func() {
				So(report.Farm, ShouldBeEmpty)
				So(errors.Is(claudeErr, fs.ErrNotExist), ShouldBeTrue)
				So(errors.Is(openErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestPluginFarmSkipsOffAndDisabled(t *testing.T) {
	Convey("Given farm gating", t, func() {
		Convey("When a host has skills off, it is not farmed", func() {
			t.Setenv("XDG_CONFIG_HOME", "")

			f := newFixture(t)
			f.emptyConfigs(t)

			plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
			writeSkill(t, plugin, "alpha", "# alpha\n")

			f.config.SetMode(agent.OpenCodeID, kind.Skills, config.ModeOff)

			f.sync(t)

			So(farmLink(t, claudeSkillsDir(f.home), "alpha"), ShouldEqual, pluginCurrentSkill(f, "alpha"))

			_, openErr := os.Stat(openCodeSkillsDir(f.home))
			So(errors.Is(openErr, fs.ErrNotExist), ShouldBeTrue)
		})

		Convey("When the skills kind is disabled, nothing is farmed", func() {
			t.Setenv("XDG_CONFIG_HOME", "")

			f := newFixture(t)
			f.emptyConfigs(t)

			plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
			writeSkill(t, plugin, "alpha", "# alpha\n")

			f.config.SetKind(kind.Skills, config.ModeOff)

			report := f.sync(t)

			_, claudeErr := os.Stat(claudeSkillsDir(f.home))
			_, openErr := os.Stat(openCodeSkillsDir(f.home))

			So(report.Farm, ShouldBeEmpty)
			So(errors.Is(claudeErr, fs.ErrNotExist), ShouldBeTrue)
			So(errors.Is(openErr, fs.ErrNotExist), ShouldBeTrue)
		})

		Convey("When an opt-in host is not enabled, it is not farmed", func() {
			t.Setenv("XDG_CONFIG_HOME", "")

			f := newFixture(t)
			f.emptyConfigs(t)

			plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
			writeSkill(t, plugin, "alpha", "# alpha\n")

			f.sync(t)

			_, sharedErr := os.Stat(sharedSkillsDir(f.home))
			So(errors.Is(sharedErr, fs.ErrNotExist), ShouldBeTrue)
		})
	})
}

func TestPluginFarmNoPruneOnLedgerError(t *testing.T) {
	Convey("Given a broken ledger", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		f.sync(t)

		linkBefore := farmLink(t, claudeSkillsDir(f.home), "alpha")

		So(os.Remove(f.vault.PluginsLedgerPath()), ShouldBeNil)
		So(os.MkdirAll(f.vault.PluginsLedgerPath(), 0o700), ShouldBeNil)

		report := f.sync(t)

		Convey("When sync runs", func() {
			Convey("Then the farm does not prune and warns about the ledger", func() {
				So(farmLink(t, claudeSkillsDir(f.home), "alpha"), ShouldEqual, linkBefore)
				So(farmLink(t, openCodeSkillsDir(f.home), "alpha"), ShouldEqual, linkBefore)
				So(report.Farm, ShouldBeEmpty)
				So(containsWarning(report.Warnings, "ledger"), ShouldBeTrue)
			})
		})
	})
}

func TestPluginFarmSkipsTargetOutsideCache(t *testing.T) {
	Convey("Given a plugin installed outside the cache", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		outside := filepath.Join(f.home, "elsewhere", "tool")
		write(t, filepath.Join(outside, "skills", "alpha", "SKILL.md"), "# outside\n")
		write(t, filepath.Join(outside, ".claude-plugin", "plugin.json"), `{"name": "tool", "version": "1.0.0"}`)

		writeRegistry(t, f.home, map[string][]map[string]any{"tool@acme": {{
			"scope": "user", "installPath": outside, "version": "1.0.0",
		}}})

		report := f.sync(t)

		_, claudeErr := os.Stat(claudeSkillsDir(f.home))
		_, openErr := os.Stat(openCodeSkillsDir(f.home))

		entries, err := os.ReadDir(f.vault.SkillsDir())
		So(err, ShouldBeNil)

		Convey("When sync runs", func() {
			Convey("Then the farm refuses outside-cache targets and leaks nothing", func() {
				So(errors.Is(claudeErr, fs.ErrNotExist), ShouldBeTrue)
				So(errors.Is(openErr, fs.ErrNotExist), ShouldBeTrue)
				So(containsWarning(report.Warnings, "outside the plugin cache"), ShouldBeTrue)
				So(entries, ShouldBeEmpty)
			})
		})
	})
}

func TestPluginFarmSkipsSkillSymlinkedOutsideCache(t *testing.T) {
	Convey("Given a plugin skill symlinked outside the cache", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		outside := filepath.Join(f.home, "notes", "helper")
		write(t, filepath.Join(outside, "SKILL.md"), "# outside\n")

		So(os.MkdirAll(filepath.Join(plugin, "skills"), 0o700), ShouldBeNil)
		So(os.Symlink(outside, filepath.Join(plugin, "skills", "helper")), ShouldBeNil)

		report := f.sync(t)

		entries, err := os.ReadDir(f.vault.SkillsDir())
		So(err, ShouldBeNil)

		_, helperErr := os.Stat(filepath.Join(claudeSkillsDir(f.home), "helper"))

		Convey("When sync runs", func() {
			Convey("Then only the regular skill is farmed", func() {
				So(report.Farm, ShouldHaveLength, 2)

				for _, result := range report.Farm {
					So(result.Action, ShouldEqual, engine.FarmLinked)
					So(result.Count, ShouldEqual, 1)
				}

				So(errors.Is(helperErr, fs.ErrNotExist), ShouldBeTrue)
				So(containsWarning(report.Warnings, "resolves outside the plugin cache"), ShouldBeTrue)
				So(entries, ShouldBeEmpty)
			})
		})
	})
}

func TestPluginFarmNoPruneOnUnreadableSkills(t *testing.T) {
	Convey("Given unreadable plugin skills", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		f.sync(t)

		linkBefore := farmLink(t, claudeSkillsDir(f.home), "alpha")

		skillsPath := filepath.Join(plugin, "skills")
		So(os.Chmod(skillsPath, 0o000), ShouldBeNil)

		t.Cleanup(func() {
			_ = os.Chmod(skillsPath, 0o700) //nolint:gosec // G302: restoring the directory mode needs the execute bit
		})

		report := f.sync(t)

		Convey("When sync runs", func() {
			Convey("Then it does not prune and warns", func() {
				So(report.Farm, ShouldBeEmpty)
				So(farmLink(t, claudeSkillsDir(f.home), "alpha"), ShouldEqual, linkBefore)
				So(farmLink(t, openCodeSkillsDir(f.home), "alpha"), ShouldEqual, linkBefore)
				So(containsWarning(report.Warnings, "skills cannot be read"), ShouldBeTrue)
			})
		})
	})
}

func TestPluginFarmGatesDirectionAndKinds(t *testing.T) {
	Convey("Given a farmed plugin and a newer cache", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		f.sync(t)

		linkBefore := farmLink(t, claudeSkillsDir(f.home), "alpha")

		pluginTree(t, f.home, "acme", "tool", "2.0.0")

		_, err := f.engine.Sync(t.Context(), engine.SyncOptions{Direction: config.ModePull})
		So(err, ShouldBeNil)

		report := f.run(t, engine.SyncOptions{Kinds: []kind.ID{kind.MCP}})

		Convey("When pull and a narrowed kind set run", func() {
			Convey("Then neither touches the farm", func() {
				So(farmLink(t, claudeSkillsDir(f.home), "alpha"), ShouldEqual, linkBefore)
				So(report.Farm, ShouldBeEmpty)
				So(farmLink(t, claudeSkillsDir(f.home), "alpha"), ShouldEqual, linkBefore)
			})
		})
	})
}
