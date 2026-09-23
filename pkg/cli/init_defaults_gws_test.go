package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// projectStatusLine returns the project status line of one file, so a test
// can assert what the vault-side policy says about it.
func projectStatusLine(t *testing.T, out, rel string) string {
	t.Helper()

	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, rel) {
			return line
		}
	}

	t.Fatalf("no %s line in:\n%s", rel, out)

	return ""
}

func TestInitEnablesProjectFilesInGitRepo(t *testing.T) {
	Convey("Given a git checkout with project files", t, func() {
		home := gwsHome(t)

		repo := t.TempDir()
		t.Chdir(repo)

		gwsWrite(t, filepath.Join(repo, "AGENTS.md"), "# repo rules\n")
		gwsWrite(t, filepath.Join(repo, ".mcp.json"), `{"mcpServers": {}}`)

		if out, err := exec.CommandContext(t.Context(), "git", "-C", repo, "init").CombinedOutput(); err != nil { //nolint:gosec // G204: the test runs git in its own temp repo
			t.Fatalf("git init: %v: %s", err, out)
		}

		projectAutoEnable = true

		t.Cleanup(func() { projectAutoEnable = false })

		Convey("When init runs", func() {
			out, err := gwsRun(t, "init")
			So(err, ShouldBeNil)

			Convey("Then the present files are enabled with secrets kept out", func() {
				So(out, ShouldContainSubstring, "project: enabled")
				So(out, ShouldContainSubstring, "AGENTS.md")
				So(out, ShouldContainSubstring, "secrets stay out")

				status, err := gwsRun(t, "project", "status")
				So(err, ShouldBeNil)
				So(projectStatusLine(t, status, "AGENTS.md"), ShouldContainSubstring, "yes")
				So(projectStatusLine(t, status, ".mcp.json"), ShouldContainSubstring, "yes")

				id := strings.TrimPrefix(projectStatusLine(t, status, "project:"), "project: ")

				policy, err := os.ReadFile(filepath.Join(home, ".beadle", "projects", id, "policy.json")) //nolint:gosec // G304: test reads its own temp file
				So(err, ShouldBeNil)
				So(string(policy), ShouldNotContainSubstring, `"allowSecrets": true`)
			})

			Convey("And a second init does not enable anything again", func() {
				again, err := gwsRun(t, "init")
				So(err, ShouldBeNil)
				So(again, ShouldNotContainSubstring, "project: enabled")
			})
		})
	})
}

func TestInitLeavesPlainDirectoryAlone(t *testing.T) {
	Convey("Given a plain directory with project files", t, func() {
		gwsHome(t)

		dir := t.TempDir()
		t.Chdir(dir)

		gwsWrite(t, filepath.Join(dir, "AGENTS.md"), "# not a repo\n")

		projectAutoEnable = true

		t.Cleanup(func() { projectAutoEnable = false })

		Convey("When init runs", func() {
			out, err := gwsRun(t, "init")
			So(err, ShouldBeNil)

			Convey("Then no project file is enabled", func() {
				So(out, ShouldNotContainSubstring, "project: enabled")
			})
		})
	})
}
