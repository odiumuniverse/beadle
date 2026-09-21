package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
)

func TestSyncRetiresInvalidCanonSkill(t *testing.T) {
	Convey("Given a canon skill beadle delivered to the surfaces", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		for _, id := range []string{agent.SharedID, agent.GeminiCLIID} {
			f.config.Enable(id)
		}

		if err := f.config.Save(f.vault.ConfigPath()); err != nil {
			t.Fatalf("save config: %v", err)
		}

		write(t, filepath.Join(f.vault.SkillsDir(), "bucket", "SKILL.md"), "# bucket\n")
		write(t, filepath.Join(f.vault.SkillsDir(), "bucket", "data", "note.txt"), "n\n")
		write(t, f.geminiSettings(), "{}")

		write(t, filepath.Join(f.home, ".claude", "skills", "claude-bucket", "note.txt"), "x\n")
		write(t, filepath.Join(f.home, ".agents", "skills", "shared-bucket", "note.txt"), "x\n")

		f.sync(t)

		copies := []string{
			filepath.Join(f.vault.SkillsDir(), "bucket"),
			filepath.Join(f.home, ".claude", "skills", "bucket"),
			filepath.Join(f.home, ".agents", "skills", "bucket"),
			filepath.Join(f.home, ".gemini", "skills", "bucket"),
		}

		Convey("When the root SKILL.md disappears everywhere, as in an account sync", func() {
			for _, dir := range copies {
				So(fsutil.Exists(filepath.Join(dir, "SKILL.md")), ShouldBeTrue)
				So(os.Remove(filepath.Join(dir, "SKILL.md")), ShouldBeNil)
			}

			f.config.SetMode(agent.ClaudeCodeID, kind.Skills, config.ModeOff)

			if err := f.config.Save(f.vault.ConfigPath()); err != nil {
				t.Fatalf("save config: %v", err)
			}

			st := loadState(t, f)
			st.Conflicts = append(st.Conflicts, state.Conflict{
				Kind: kind.Skills, Agent: agent.ClaudeCodeID, Key: "bucket/data/note.txt",
				Reason: state.ReasonModified, Since: time.Now().UTC(),
			})

			if err := st.Save(f.vault.StatePath()); err != nil {
				t.Fatalf("save state: %v", err)
			}

			Convey("Then doctor warns before the cleanup", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityWarn, "canon skill bucket has no root SKILL.md"), ShouldBeTrue)
			})

			report := f.sync(t)

			Convey("Then the canon and the writable beadle-owned copies are retired", func() {
				So(fsutil.Exists(filepath.Join(f.vault.SkillsDir(), "bucket")), ShouldBeFalse)
				So(fsutil.Exists(filepath.Join(f.home, ".agents", "skills", "bucket")), ShouldBeFalse)
				So(fsutil.Exists(filepath.Join(f.home, ".gemini", "skills", "bucket")), ShouldBeFalse)

				kr := report.Kind(kind.Skills)
				So(kr, ShouldNotBeNil)
				So(strings.Join(kr.Warnings, " "), ShouldContainSubstring, "retired the invalid canon skill bucket")
			})

			Convey("Then the mode-off surface and the foreign directories survive", func() {
				So(fsutil.Exists(filepath.Join(f.home, ".claude", "skills", "bucket", "data", "note.txt")), ShouldBeTrue)
				So(fsutil.Exists(filepath.Join(f.home, ".claude", "skills", "claude-bucket", "note.txt")), ShouldBeTrue)
				So(fsutil.Exists(filepath.Join(f.home, ".agents", "skills", "shared-bucket", "note.txt")), ShouldBeTrue)
			})

			Convey("Then the conflict about the retired name is dropped", func() {
				So(f.conflicts(t, kind.Skills, agent.ClaudeCodeID), ShouldBeEmpty)
			})

			Convey("Then a second sync is idempotent", func() {
				second := f.sync(t)

				kr := second.Kind(kind.Skills)
				So(kr, ShouldNotBeNil)
				So(strings.Join(kr.Warnings, " "), ShouldNotContainSubstring, "retired the invalid canon skill bucket")
				So(fsutil.Exists(filepath.Join(f.home, ".agents", "skills", "bucket")), ShouldBeFalse)
			})
		})
	})
}

func TestSyncKeepsForeignInvalidCanonDirectory(t *testing.T) {
	Convey("Given a canon directory nobody adopted", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, filepath.Join(f.vault.SkillsDir(), "foreign-bucket", "note.txt"), "x\n")

		Convey("When sync runs", func() {
			report := f.sync(t)

			Convey("Then the directory stays and no retire is reported", func() {
				So(fsutil.Exists(filepath.Join(f.vault.SkillsDir(), "foreign-bucket", "note.txt")), ShouldBeTrue)

				kr := report.Kind(kind.Skills)
				So(kr, ShouldNotBeNil)
				So(strings.Join(kr.Warnings, " "), ShouldNotContainSubstring, "retired the invalid canon skill")
			})
		})
	})
}
