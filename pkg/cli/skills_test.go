package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestSkillsAdoptCommands(t *testing.T) {
	Convey("Given an initialized vault with a canon skill and a foreign symlink", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))

		writeFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {}}`)
		writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "# rules\n")

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		writeFile(t, filepath.Join(home, ".beadle", "skills", "alpha", "SKILL.md"), "# alpha\n")

		src := filepath.Join(home, "skills-src", "alpha")
		writeFile(t, filepath.Join(src, "SKILL.md"), "# alpha\n")

		link := filepath.Join(home, ".claude", "skills", "alpha")
		So(os.MkdirAll(filepath.Dir(link), 0o750), ShouldBeNil)
		So(os.Symlink(src, link), ShouldBeNil)

		Convey("When adopt runs dry", func() {
			out, err := runCLI(t, "skills", "adopt", "alpha", "--host", "claude", "--dry-run")
			So(err, ShouldBeNil)
			So(out, ShouldContainSubstring, "would-adopt")
			So(out, ShouldContainSubstring, "re-run without --dry-run")

			_, statErr := os.Lstat(link)
			So(statErr, ShouldBeNil)

			Convey("When adopt runs for real", func() {
				out, err := runCLI(t, "skills", "adopt", "alpha", "--host", "claude")
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "adopted")
				So(out, ShouldContainSubstring, "run beadle sync")

				_, statErr := os.Lstat(link)
				So(errors.Is(statErr, fs.ErrNotExist), ShouldBeTrue)

				Convey("When unadopt runs", func() {
					out, err := runCLI(t, "skills", "unadopt", "alpha")
					So(err, ShouldBeNil)
					So(out, ShouldContainSubstring, "restored")

					_, statErr := os.Lstat(link)
					So(statErr, ShouldBeNil)
				})
			})
		})

		Convey("When the skill is unknown", func() {
			_, err := runCLI(t, "skills", "adopt", "missing", "--host", "claude")
			So(err, ShouldNotBeNil)
		})
	})
}
