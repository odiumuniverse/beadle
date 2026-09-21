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
	"github.com/odiumuniverse/beadle/pkg/fsutil"
)

func TestAdoptMovesForeignCopyIntoStash(t *testing.T) {
	Convey("Given a claude host with a foreign symlink of a canon skill", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, f.vaultSkill("alpha"), "# alpha\n")

		src := foreignSkill(t, f, "alpha", "# alpha\n")
		link := filepath.Join(f.home, ".claude", "skills", "alpha")
		stash := filepath.Join(f.vault.AdoptionsDir(), agent.ClaudeCodeID, "alpha")

		Convey("When adopt runs", func() {
			report, err := f.engine.Adopt(t.Context(), "alpha", "claude", false)
			So(err, ShouldBeNil)

			Convey("Then the symlink is stashed and the ledger records it", func() {
				So(report.Adoptions, ShouldHaveLength, 1)
				So(report.Adoptions[0].Action, ShouldEqual, "adopted")
				So(report.Adoptions[0].Provider, ShouldEqual, link)

				_, statErr := os.Lstat(link)
				So(errors.Is(statErr, fs.ErrNotExist), ShouldBeTrue)

				info, statErr := os.Lstat(stash)
				So(statErr, ShouldBeNil)
				So(info.Mode()&fs.ModeSymlink, ShouldNotBeZeroValue)

				target, readErr := os.Readlink(stash)
				So(readErr, ShouldBeNil)
				So(target, ShouldEqual, src)

				record, ok := loadState(t, f).AdoptionFor(agent.ClaudeCodeID, "alpha")
				So(ok, ShouldBeTrue)
				So(record.Target, ShouldEqual, src)
				So(record.Provider, ShouldEqual, link)
				So(read(t, filepath.Join(src, "SKILL.md")), ShouldEqual, "# alpha\n")
				So(read(t, f.vaultSkill("alpha")), ShouldEqual, "# alpha\n")
			})

			Convey("Then a second adopt refuses", func() {
				_, err := f.engine.Adopt(t.Context(), "alpha", "claude", false)
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, "already adopted")
			})

			Convey("And doctor reports the adoption", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityInfo, "was adopted"), ShouldBeTrue)
			})

			Convey("When sync delivers the canon and unadopt follows", func() {
				f.sync(t)

				So(fsutil.Exists(f.claudeSkill("alpha")), ShouldBeTrue)

				report, err := f.engine.Unadopt(t.Context(), "alpha", "claude", false)
				So(err, ShouldBeNil)
				So(report.Adoptions, ShouldHaveLength, 1)
				So(report.Adoptions[0].Action, ShouldEqual, "restored")

				info, statErr := os.Lstat(link)
				So(statErr, ShouldBeNil)
				So(info.Mode()&fs.ModeSymlink, ShouldNotBeZeroValue)

				_, ok := loadState(t, f).AdoptionFor(agent.ClaudeCodeID, "alpha")
				So(ok, ShouldBeFalse)

				Convey("And the next sync leaves the foreign copy alone", func() {
					f.sync(t)

					info, statErr := os.Lstat(link)
					So(statErr, ShouldBeNil)
					So(info.Mode()&fs.ModeSymlink, ShouldNotBeZeroValue)
					So(read(t, f.claudeSkill("alpha")), ShouldEqual, "# alpha\n")
				})
			})
		})
	})
}

func TestAdoptRefusesForksAndMissingCopies(t *testing.T) {
	Convey("Given a forked foreign copy of a canon skill", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, f.vaultSkill("alpha"), "# alpha\n")

		src := foreignSkill(t, f, "alpha", "# alpha v2\n")
		link := filepath.Join(f.home, ".claude", "skills", "alpha")

		Convey("When adopt runs", func() {
			_, err := f.engine.Adopt(t.Context(), "alpha", "claude-code", false)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "differs between the canon")

			Convey("Then the forked copy stays untouched", func() {
				So(read(t, filepath.Join(src, "SKILL.md")), ShouldEqual, "# alpha v2\n")

				_, statErr := os.Lstat(link)
				So(statErr, ShouldBeNil)
			})
		})

		Convey("When the copy is gone", func() {
			So(os.Remove(link), ShouldBeNil)

			_, err := f.engine.Adopt(t.Context(), "alpha", "claude-code", false)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "no readable copy")
		})
	})

	Convey("Given a foreign copy of a skill outside the canon", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, f.vaultSkill("alpha"), "# alpha\n")
		foreignSkill(t, f, "beta", "# beta\n")

		Convey("When adopt runs", func() {
			_, err := f.engine.Adopt(t.Context(), "beta", "claude-code", false)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "not in the canon")
		})
	})

	Convey("Given a canon skill already delivered by a beadle channel", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, f.vaultSkill("alpha"), "# alpha\n")
		write(t, f.sharedSkill("alpha"), "# alpha\n")

		Convey("When adopt runs for opencode", func() {
			_, err := f.engine.Adopt(t.Context(), "alpha", "opencode", false)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "already delivers")
		})
	})

	Convey("Given a canon skill and unknown or inactive hosts", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, f.vaultSkill("alpha"), "# alpha\n")

		Convey("When the host is unknown", func() {
			_, err := f.engine.Adopt(t.Context(), "alpha", "does-not-exist", false)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "unknown host")
		})

		Convey("When the host is inactive", func() {
			_, err := f.engine.Adopt(t.Context(), "alpha", "gemini-cli", false)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "not active")
		})

		Convey("When no host holds a copy", func() {
			_, err := f.engine.Adopt(t.Context(), "alpha", "", false)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "no adoptable foreign copy")
		})
	})
}

func TestAdoptDryRunWritesNothing(t *testing.T) {
	Convey("Given a claude host with a foreign symlink", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, f.vaultSkill("alpha"), "# alpha\n")

		foreignSkill(t, f, "alpha", "# alpha\n")
		link := filepath.Join(f.home, ".claude", "skills", "alpha")
		stash := filepath.Join(f.vault.AdoptionsDir(), agent.ClaudeCodeID, "alpha")

		Convey("When adopt runs dry", func() {
			report, err := f.engine.Adopt(t.Context(), "alpha", "claude-code", true)
			So(err, ShouldBeNil)

			Convey("Then nothing moved", func() {
				So(report.Adoptions, ShouldHaveLength, 1)
				So(report.Adoptions[0].Action, ShouldEqual, "would-adopt")

				_, statErr := os.Lstat(link)
				So(statErr, ShouldBeNil)
				So(fsutil.Exists(stash), ShouldBeFalse)

				_, ok := loadState(t, f).AdoptionFor(agent.ClaudeCodeID, "alpha")
				So(ok, ShouldBeFalse)
			})

			Convey("When the adoption is real", func() {
				_, err := f.engine.Adopt(t.Context(), "alpha", "claude-code", false)
				So(err, ShouldBeNil)

				Convey("Then unadopt dry does not restore either", func() {
					report, err := f.engine.Unadopt(t.Context(), "alpha", "claude-code", true)
					So(err, ShouldBeNil)
					So(report.Adoptions, ShouldHaveLength, 1)
					So(report.Adoptions[0].Action, ShouldEqual, "would-restore")

					_, statErr := os.Lstat(link)
					So(errors.Is(statErr, fs.ErrNotExist), ShouldBeTrue)
					So(fsutil.Exists(stash), ShouldBeTrue)

					_, ok := loadState(t, f).AdoptionFor(agent.ClaudeCodeID, "alpha")
					So(ok, ShouldBeTrue)
				})
			})
		})
	})
}

func TestUnadoptFallbacks(t *testing.T) {
	adoptFixture := func(t *testing.T) (*fixture, string, string) {
		t.Helper()

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, f.vaultSkill("alpha"), "# alpha\n")

		src := foreignSkill(t, f, "alpha", "# alpha\n")
		link := filepath.Join(f.home, ".claude", "skills", "alpha")

		if _, err := f.engine.Adopt(t.Context(), "alpha", "claude-code", false); err != nil {
			t.Fatalf("adopt: %v", err)
		}

		return f, src, link
	}

	Convey("Given an adopted symlink whose stash is gone but the target lives", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f, src, link := adoptFixture(t)

		stash := filepath.Join(f.vault.AdoptionsDir(), agent.ClaudeCodeID, "alpha")
		So(os.Remove(stash), ShouldBeNil)

		Convey("When unadopt runs", func() {
			report, err := f.engine.Unadopt(t.Context(), "alpha", "claude-code", false)
			So(err, ShouldBeNil)

			Convey("Then the symlink is recreated from the recorded target", func() {
				So(report.Adoptions[0].Action, ShouldEqual, "restored")
				So(report.Adoptions[0].Note, ShouldContainSubstring, "stash is gone")

				target, readErr := os.Readlink(link)
				So(readErr, ShouldBeNil)
				So(target, ShouldEqual, src)
			})
		})
	})

	Convey("Given an adopted symlink whose stash and target are gone", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f, src, link := adoptFixture(t)

		stash := filepath.Join(f.vault.AdoptionsDir(), agent.ClaudeCodeID, "alpha")
		So(os.Remove(stash), ShouldBeNil)
		So(os.RemoveAll(src), ShouldBeNil)

		Convey("When unadopt runs", func() {
			report, err := f.engine.Unadopt(t.Context(), "alpha", "claude-code", false)
			So(err, ShouldBeNil)

			Convey("Then the record stays with a warning", func() {
				So(report.Adoptions[0].Action, ShouldEqual, "kept")
				So(report.Adoptions[0].Note, ShouldContainSubstring, "is gone")
				So(strings.Join(report.Warnings, " "), ShouldContainSubstring, "is gone")

				_, statErr := os.Lstat(link)
				So(errors.Is(statErr, fs.ErrNotExist), ShouldBeTrue)

				_, ok := loadState(t, f).AdoptionFor(agent.ClaudeCodeID, "alpha")
				So(ok, ShouldBeTrue)
			})

			Convey("And doctor warns about the lost original", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityWarn, "the adopted copy of alpha is gone"), ShouldBeTrue)
			})
		})
	})

	Convey("Given an adopted copy whose slot is occupied by an unmanaged tree", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f, _, link := adoptFixture(t)

		write(t, filepath.Join(link, "SKILL.md"), "# somebody else\n")

		Convey("When unadopt runs", func() {
			report, err := f.engine.Unadopt(t.Context(), "alpha", "claude-code", false)
			So(err, ShouldBeNil)

			Convey("Then the record stays and the foreign tree is untouched", func() {
				So(report.Adoptions[0].Action, ShouldEqual, "kept")
				So(report.Adoptions[0].Note, ShouldContainSubstring, "move it away manually")
				So(read(t, filepath.Join(link, "SKILL.md")), ShouldEqual, "# somebody else\n")

				_, ok := loadState(t, f).AdoptionFor(agent.ClaudeCodeID, "alpha")
				So(ok, ShouldBeTrue)
			})
		})
	})
}

func TestAdoptTreeCopyAndUnadopt(t *testing.T) {
	Convey("Given a claude host with an unmanaged tree copy of a canon skill", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, f.vaultSkill("alpha"), "# alpha\n")
		write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "notes.txt"), "keep me\n")

		link := filepath.Join(f.home, ".claude", "skills", "alpha")
		write(t, filepath.Join(link, "SKILL.md"), "# alpha\n")
		write(t, filepath.Join(link, "notes.txt"), "keep me\n")

		stash := filepath.Join(f.vault.AdoptionsDir(), agent.ClaudeCodeID, "alpha")

		Convey("When adopt runs", func() {
			report, err := f.engine.Adopt(t.Context(), "alpha", "claude-code", false)
			So(err, ShouldBeNil)

			Convey("Then the tree is stashed and the record has no symlink target", func() {
				So(report.Adoptions[0].Action, ShouldEqual, "adopted")

				record, ok := loadState(t, f).AdoptionFor(agent.ClaudeCodeID, "alpha")
				So(ok, ShouldBeTrue)
				So(record.Target, ShouldBeEmpty)

				_, statErr := os.Lstat(link)
				So(errors.Is(statErr, fs.ErrNotExist), ShouldBeTrue)
				So(read(t, filepath.Join(stash, "SKILL.md")), ShouldEqual, "# alpha\n")
				So(read(t, filepath.Join(stash, "notes.txt")), ShouldEqual, "keep me\n")
			})

			Convey("When unadopt runs", func() {
				report, err := f.engine.Unadopt(t.Context(), "alpha", "claude-code", false)
				So(err, ShouldBeNil)

				Convey("Then the whole tree comes back", func() {
					So(report.Adoptions[0].Action, ShouldEqual, "restored")
					So(read(t, filepath.Join(link, "SKILL.md")), ShouldEqual, "# alpha\n")
					So(read(t, filepath.Join(link, "notes.txt")), ShouldEqual, "keep me\n")

					_, ok := loadState(t, f).AdoptionFor(agent.ClaudeCodeID, "alpha")
					So(ok, ShouldBeFalse)
				})
			})
		})
	})
}

func TestAdoptWithoutHostAdoptsTheOwningSurfaceOnce(t *testing.T) {
	Convey("Given a foreign symlink both claude and opencode can read", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, f.vaultSkill("alpha"), "# alpha\n")
		foreignSkill(t, f, "alpha", "# alpha\n")

		Convey("When adopt runs without a host", func() {
			report, err := f.engine.Adopt(t.Context(), "alpha", "", false)
			So(err, ShouldBeNil)

			Convey("Then the copy is adopted once and the other host reports nothing to adopt", func() {
				actions := map[string]string{}
				for _, result := range report.Adoptions {
					actions[result.Agent] = result.Action
				}

				So(actions[agent.ClaudeCodeID], ShouldEqual, "adopted")
				So(actions[agent.OpenCodeID], ShouldEqual, "skipped")
				So(strings.Join(report.Warnings, " "), ShouldContainSubstring, agent.OpenCodeID)

				_, ok := loadState(t, f).AdoptionFor(agent.ClaudeCodeID, "alpha")
				So(ok, ShouldBeTrue)
			})
		})
	})
}
