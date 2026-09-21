package cli

import (
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestExplainCommand(t *testing.T) {
	Convey("Given an initialized vault with a canon skill", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))

		writeFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {}}`)
		writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "# rules\n")

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		writeFile(t, filepath.Join(home, ".beadle", "skills", "alpha", "SKILL.md"), "# alpha\n")

		Convey("When explain runs", func() {
			out, err := runCLI(t, "explain", "alpha")
			So(err, ShouldBeNil)
			So(out, ShouldContainSubstring, "canon")
			So(out, ShouldContainSubstring, "alpha")
			So(out, ShouldContainSubstring, "role")

			Convey("And --json renders the rows", func() {
				out, err := runCLI(t, "explain", "alpha", "--json")
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, `"skill": "alpha"`)
				So(out, ShouldContainSubstring, `"host": "canon"`)
			})

			Convey("And an unknown skill is refused", func() {
				_, err := runCLI(t, "explain", "missing")
				So(err, ShouldNotBeNil)
			})
		})
	})
}
