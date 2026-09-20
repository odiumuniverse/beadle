package skills_test

import (
	"os"
	"path/filepath"
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

func TestSeedConflictsSkill(t *testing.T) {
	Convey("Given an empty skills directory", t, func() {
		dir := t.TempDir()
		file := filepath.Join(dir, skills.ConflictsName, "SKILL.md")

		Convey("When the built-in skill is seeded", func() {
			written, err := skills.Seed(dir, false)

			Convey("Then it is written with frontmatter and owner-only permissions", func() {
				So(err, ShouldBeNil)
				So(written, ShouldBeTrue)

				dirInfo, statErr := os.Stat(filepath.Join(dir, skills.ConflictsName))
				So(statErr, ShouldBeNil)
				So(dirInfo.Mode().Perm(), ShouldEqual, os.FileMode(0o700))

				fileInfo, statErr := os.Stat(file)
				So(statErr, ShouldBeNil)
				So(fileInfo.Mode().Perm(), ShouldEqual, os.FileMode(0o600))

				text := readSkill(t, file)
				So(text, ShouldStartWith, "---\nname: beadle-conflicts")

				for _, tag := range []string{"<rules>", "<workflow>", "<command>", "<never>", "<prevention>"} {
					So(text, ShouldContainSubstring, tag)
				}

				So(text, ShouldContainSubstring, "beadle conflicts --json")
				So(text, ShouldContainSubstring, "--expect-base")

				Convey("And a second seed leaves an edited skill alone", func() {
					So(os.WriteFile(file, []byte("custom\n"), 0o600), ShouldBeNil)

					written, err := skills.Seed(dir, false)
					So(err, ShouldBeNil)
					So(written, ShouldBeFalse)
					So(readSkill(t, file), ShouldEqual, "custom\n")

					Convey("But --force overwrites it", func() {
						written, err := skills.Seed(dir, true)
						So(err, ShouldBeNil)
						So(written, ShouldBeTrue)
						So(readSkill(t, file), ShouldContainSubstring, "name: beadle-conflicts")
					})

					Convey("And a forced reseed tightens a loose directory mode", func() {
						skillDir := filepath.Join(dir, skills.ConflictsName)
						So(os.Chmod(skillDir, 0o750), ShouldBeNil) //nolint:gosec // G302: loosened on purpose to test the tightening

						written, err := skills.Seed(dir, true)
						So(err, ShouldBeNil)
						So(written, ShouldBeTrue)

						info, statErr := os.Stat(skillDir)
						So(statErr, ShouldBeNil)
						So(info.Mode().Perm(), ShouldEqual, os.FileMode(0o700))
					})
				})
			})
		})
	})
}
