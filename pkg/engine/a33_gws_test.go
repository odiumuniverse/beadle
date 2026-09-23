package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func TestFlatSkillFanIn(t *testing.T) {
	Convey("Given a flat skill in the OpenCode skills directory", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		dir := openCodeSkillsDir(f.home)
		flat := filepath.Join(dir, "alpha.md")

		write(t, flat, "# alpha\n")

		Convey("When the first sync runs", func() {
			f.sync(t)

			Convey("Then the canon holds the skill as a directory tree", func() {
				So(read(t, f.vaultSkill("alpha")), ShouldEqual, "# alpha\n")
			})

			Convey("And a second sync keeps the flat copy and writes no directory", func() {
				f.sync(t)

				So(read(t, flat), ShouldEqual, "# alpha\n")

				_, err := os.Stat(filepath.Join(dir, "alpha"))
				So(os.IsNotExist(err), ShouldBeTrue)
			})

			Convey("And the doctor reports the flat copy without complaints", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)

				So(hasIssue(issues, engine.SeverityInfo, "1 flat skill(s) in ~/.config/opencode/skills are read by the host: alpha"), ShouldBeTrue)

				for _, issue := range issues {
					if issue.Severity != engine.SeverityWarn && issue.Severity != engine.SeverityError {
						continue
					}

					So(strings.Contains(issue.Message, "alpha"), ShouldBeFalse)
				}
			})
		})
	})
}

func TestFlatSkillDelivery(t *testing.T) {
	Convey("Given a flat skill adopted into the canon", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		f.config.SetMode(agent.OpenCodeID, kind.Skills, config.ModeSync)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		dir := openCodeSkillsDir(f.home)
		flat := filepath.Join(dir, "alpha.md")

		write(t, flat, "# alpha\n")

		f.sync(t)

		Convey("When the canon changes", func() {
			write(t, f.vaultSkill("alpha"), "# canon\n")

			f.sync(t)

			Convey("Then the directory carries the canon and the flat file stays", func() {
				So(read(t, filepath.Join(dir, "alpha", "SKILL.md")), ShouldEqual, "# canon\n")
				So(read(t, flat), ShouldEqual, "# alpha\n")
			})
		})

		Convey("When the canon drops the skill", func() {
			So(os.RemoveAll(filepath.Dir(f.vaultSkill("alpha"))), ShouldBeNil)

			f.sync(t)

			Convey("Then the flat copy is removed with the canon", func() {
				_, err := os.Stat(flat)
				So(os.IsNotExist(err), ShouldBeTrue)
			})
		})
	})
}

func TestFlatSkillShadowedCollision(t *testing.T) {
	Convey("Given a flat copy shadowed by a same-name skill directory", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		f.config.SetMode(agent.OpenCodeID, kind.Skills, config.ModeSync)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		dir := openCodeSkillsDir(f.home)
		flat := filepath.Join(dir, "alpha.md")

		write(t, flat, "# flat\n")
		write(t, filepath.Join(dir, "alpha", "SKILL.md"), "# dir\n")

		Convey("When the sync runs", func() {
			report := f.sync(t)

			Convey("Then the directory is the canon copy and the file stays", func() {
				So(read(t, f.vaultSkill("alpha")), ShouldEqual, "# dir\n")
				So(read(t, flat), ShouldEqual, "# flat\n")

				kr := report.Kind(kind.Skills)
				So(kr, ShouldNotBeNil)
				So(strings.Join(kr.Warnings, "\n"), ShouldContainSubstring, "shadowed by alpha/SKILL.md")
			})

			Convey("And the doctor reports the collision", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)

				So(hasIssue(issues, engine.SeverityWarn, "flat skill ~/.config/opencode/skills/alpha.md is shadowed by alpha/SKILL.md"), ShouldBeTrue)
			})
		})

		Convey("When the canon drops the name", func() {
			f.sync(t)

			So(os.RemoveAll(filepath.Dir(f.vaultSkill("alpha"))), ShouldBeNil)

			report := f.sync(t)
			So(report.Errors(), ShouldBeEmpty)

			Convey("Then the directory and the shadowed flat file are both gone", func() {
				_, dirErr := os.Stat(filepath.Join(dir, "alpha"))
				_, flatErr := os.Stat(flat)

				So(os.IsNotExist(dirErr), ShouldBeTrue)
				So(os.IsNotExist(flatErr), ShouldBeTrue)
			})
		})
	})
}

func TestFlatSkillPiSharedIgnored(t *testing.T) {
	Convey("Given a flat copy in the shared directory", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		flat := filepath.Join(f.home, ".agents", "skills", "beta.md")
		write(t, flat, "# beta\n")

		Convey("When the sync runs", func() {
			f.sync(t)

			Convey("Then the shared flat copy is not adopted", func() {
				_, err := os.Stat(f.vaultSkill("beta"))
				So(os.IsNotExist(err), ShouldBeTrue)
			})
		})
	})
}
