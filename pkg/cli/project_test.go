package cli

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func newProjectCLIRepo(t *testing.T) string {
	t.Helper()

	home := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))
	t.Setenv("XDG_CONFIG_HOME", "")

	writeFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {}}`)

	repo := t.TempDir()

	cmd := exec.CommandContext(t.Context(), "git", "-C", repo, "init", "-q") //nolint:gosec // G204: fixed git subcommand in a test
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	t.Chdir(repo)

	if _, err := runCLI(t, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}

	return repo
}

func TestProjectCLIStatusEnableForget(t *testing.T) {
	Convey("Given a git repo with an initialized vault", t, func() {
		repo := newProjectCLIRepo(t)

		out, err := runCLI(t, "project", "status")
		So(err, ShouldBeNil)

		Convey("When a project file is enabled", func() {
			So(out, ShouldContainSubstring, "project: ")
			So(out, ShouldContainSubstring, "FILE")
			So(out, ShouldContainSubstring, "ENABLED")
			So(out, ShouldContainSubstring, "PUBLISHABLE")
			So(out, ShouldContainSubstring, "AGENTS.md")
			So(out, ShouldContainSubstring, ".mcp.json")

			out, err := runCLI(t, "project", "enable", ".mcp.json")

			Convey("Then it materializes the skeleton and shows up as enabled", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "enabled .mcp.json")

				out, err := runCLI(t, "project", "status")
				So(err, ShouldBeNil)

				matched, matchErr := regexp.MatchString(`(?m)^\.mcp\.json\s+yes`, out)
				So(matchErr, ShouldBeNil)
				So(matched, ShouldBeTrue)

				_, statErr := os.Stat(filepath.Join(repo, ".mcp.json"))
				So(statErr, ShouldBeNil)

				_, err = runCLI(t, "sync")
				So(err, ShouldBeNil)

				out, err = runCLI(t, "project", "disable", ".mcp.json")
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "disabled .mcp.json")

				out, err = runCLI(t, "project", "status")
				So(err, ShouldBeNil)

				matched, matchErr = regexp.MatchString(`(?m)^\.mcp\.json\s+no`, out)
				So(matchErr, ShouldBeNil)
				So(matched, ShouldBeTrue)

				_, err = runCLI(t, "project", "forget", ".mcp.json")
				So(err, ShouldBeNil)

				_, statErr = os.Stat(filepath.Join(repo, ".mcp.json"))
				So(errors.Is(statErr, fs.ErrNotExist), ShouldBeTrue)

				_, err = runCLI(t, "project", "enable", "nope.md")
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "unknown project file")
			})
		})
	})
}

func TestProjectCLIStatusPathSlug(t *testing.T) {
	Convey("Given a directory that is not a git checkout", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))
		t.Setenv("XDG_CONFIG_HOME", "")

		dir := t.TempDir()
		t.Chdir(dir)

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		Convey("When the project status is requested", func() {
			out, err := runCLI(t, "project", "status")

			Convey("Then it reports a path slug", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "not a git checkout")
			})
		})
	})
}
