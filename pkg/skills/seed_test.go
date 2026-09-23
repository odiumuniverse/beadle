package skills_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/skills"
)

func readSkill(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: the test reads its own temp file
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func TestSeedBuiltinSkills(t *testing.T) {
	Convey("Given an empty skills directory", t, func() {
		dir := t.TempDir()
		conflicts := filepath.Join(dir, skills.ConflictsName, "SKILL.md")
		beadle := filepath.Join(dir, skills.BeadleName, "SKILL.md")

		Convey("When the built-in skills are seeded", func() {
			written, err := skills.Seed(dir, false)

			Convey("Then both are written with frontmatter and owner-only permissions", func() {
				So(err, ShouldBeNil)
				So(written, ShouldResemble, []string{skills.ConflictsName, skills.BeadleName})

				for _, skill := range []string{skills.ConflictsName, skills.BeadleName} {
					dirInfo, statErr := os.Stat(filepath.Join(dir, skill))
					So(statErr, ShouldBeNil)
					So(dirInfo.Mode().Perm(), ShouldEqual, os.FileMode(0o700))

					fileInfo, statErr := os.Stat(filepath.Join(dir, skill, "SKILL.md"))
					So(statErr, ShouldBeNil)
					So(fileInfo.Mode().Perm(), ShouldEqual, os.FileMode(0o600))
				}

				text := readSkill(t, conflicts)
				So(text, ShouldStartWith, "---\nname: beadle-conflicts")
				So(text, ShouldContainSubstring, "beadle guide")

				for _, tag := range []string{"<rules>", "<workflow>", "<command>", "<never>", "<prevention>"} {
					So(text, ShouldContainSubstring, tag)
				}

				So(text, ShouldContainSubstring, "beadle conflicts --json")
				So(text, ShouldContainSubstring, "--expect-base")

				umbrella := readSkill(t, beadle)
				So(umbrella, ShouldStartWith, "---\nname: beadle")
				So(umbrella, ShouldContainSubstring, "hooks approve")
				So(umbrella, ShouldContainSubstring, "beadle guide")

				for _, tag := range []string{"<important>", "<workflow>", "<command>", "<never>"} {
					So(umbrella, ShouldContainSubstring, tag)
				}

				Convey("And a second seed leaves an edited skill alone", func() {
					So(os.WriteFile(conflicts, []byte("custom\n"), 0o600), ShouldBeNil)

					written, err := skills.Seed(dir, false)
					So(err, ShouldBeNil)
					So(written, ShouldBeEmpty)
					So(readSkill(t, conflicts), ShouldEqual, "custom\n")

					Convey("But --force overwrites both", func() {
						written, err := skills.Seed(dir, true)
						So(err, ShouldBeNil)
						So(slices.Contains(written, skills.ConflictsName), ShouldBeTrue)
						So(slices.Contains(written, skills.BeadleName), ShouldBeTrue)
						So(readSkill(t, conflicts), ShouldContainSubstring, "name: beadle-conflicts")
					})

					Convey("And a forced reseed tightens a loose directory mode", func() {
						skillDir := filepath.Join(dir, skills.ConflictsName)
						So(os.Chmod(skillDir, 0o750), ShouldBeNil) //nolint:gosec // G302: loosened on purpose to test the tightening

						written, err := skills.Seed(dir, true)
						So(err, ShouldBeNil)
						So(written, ShouldNotBeEmpty)

						info, statErr := os.Stat(skillDir)
						So(statErr, ShouldBeNil)
						So(info.Mode().Perm(), ShouldEqual, os.FileMode(0o700))
					})
				})
			})
		})
	})
}

func TestEmbeddedSkillsAreValid(t *testing.T) {
	Convey("Given the embedded skills", t, func() {
		Convey("When they are read", func() {
			Convey("Then both carry a valid frontmatter block", func() {
				kebab := regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

				for _, body := range [][]byte{skills.Conflicts(), skills.Beadle()} {
					text := string(body)
					So(text, ShouldStartWith, "---\nname: ")
					So(text, ShouldContainSubstring, "\ndescription: ")
					So(text, ShouldContainSubstring, "\n---\n")

					name := strings.TrimSpace(strings.Split(strings.Split(text, "\n")[1], ":")[1])
					So(kebab.MatchString(name), ShouldBeTrue)
				}
			})
		})
	})
}
