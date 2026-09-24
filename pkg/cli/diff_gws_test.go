package cli

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestDiffMarksUncreatableSurface(t *testing.T) {
	Convey("Given a canon rule and a Codex home without AGENTS.md", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))

		So(os.MkdirAll(filepath.Join(home, ".codex"), 0o750), ShouldBeNil)

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		writeFile(t, filepath.Join(home, ".beadle", "rules", "base.md"), "# canon rules\n")

		stdout, _, err := gwsRunSplit(t, "diff")

		Convey("When diff runs", func() {
			Convey("Then the surface a sync will not write is marked, not shown as a pending change", func() {
				So(err, ShouldBeNil)
				So(stdout, ShouldContainSubstring, "rules: codex skipped: no config file to write into")
				So(stdout, ShouldNotContainSubstring, "rules → codex")
			})
		})

		Convey("When diff is asked for an agent with no changes", func() {
			stdout, _, err := gwsRunSplit(t, "diff", "--agent", "claude-code")

			Convey("Then it says there are no differences", func() {
				So(err, ShouldBeNil)
				So(stdout, ShouldContainSubstring, "no differences")
			})
		})
	})
}
