package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

const nitsSecret = "ghp_abcdefghijklmnopqrstuvwxyz012345"

func TestDoctorDoesNotClaimSecretMove(t *testing.T) {
	Convey("Given a memory note with a secret-like line", t, func() {
		home := gwsHome(t)

		gwsWrite(t, filepath.Join(home, ".claude", "projects", "-Users-test", "memory", "note.md"), "# note\ntoken: "+nitsSecret+"\n")

		if _, err := gwsRun(t, "init"); err != nil {
			t.Fatal(err)
		}

		// A memory is delivered to an agent that is enabled, so with no
		// agent enabled the sync has nowhere to put the note: nothing is
		// moved and the count below would be 0 for a reason that has
		// nothing to do with secrets. Enabling one makes the assertion
		// about the secret actually able to fail.
		if _, err := gwsRun(t, "agents", "enable", "claude"); err != nil {
			t.Fatal(err)
		}

		// beadle's notices travel on the command's own stderr, not on the
		// process os.Stderr, so they are read through the command's
		// streams. Piping os.Stderr captured nothing and made "never claims"
		// pass against an empty string.
		Convey("When doctor runs", func() {
			stdout, stderr, err := gwsRunSplit(t, "doctor")
			So(err, ShouldBeNil)

			Convey("Then it never claims a secret move", func() {
				So(stdout+stderr, ShouldNotContainSubstring, "moved to the vault store")
			})
		})

		// The move reaches the user as a note on the report, and the report
		// is what sync prints, so this is stdout. The logger's own copy of
		// the sentence is not what the user reads, and asserting on it
		// would test the log line rather than the promise.
		Convey("When sync runs", func() {
			stdout, _, err := gwsRunSplit(t, "sync")
			So(err, ShouldBeNil)

			Convey("Then it reports the move once with a count", func() {
				So(strings.Count(stdout, "moved to the vault store"), ShouldEqual, 1)
				So(stdout, ShouldContainSubstring, "count=1")
			})
		})
	})
}

func TestSeedSkillPermissions(t *testing.T) {
	Convey("Given a fresh vault", t, func() {
		home := gwsHome(t)

		if _, err := gwsRun(t, "init"); err != nil {
			t.Fatal(err)
		}

		dir := filepath.Join(home, ".beadle", "skills", "beadle-conflicts")
		file := filepath.Join(dir, "SKILL.md")

		Convey("When init seeds the conflicts skill", func() {
			dirInfo, err := os.Stat(dir)
			So(err, ShouldBeNil)

			fileInfo, err := os.Stat(file)
			So(err, ShouldBeNil)

			Convey("Then the directory and the file are owner-only", func() {
				So(dirInfo.Mode().Perm(), ShouldEqual, os.FileMode(0o700))
				So(fileInfo.Mode().Perm(), ShouldEqual, os.FileMode(0o600))
			})

			Convey("And a forced reseed keeps them", func() {
				_, err := gwsRun(t, "skills", "seed", "--force")
				So(err, ShouldBeNil)

				fileInfo, err := os.Stat(file)
				So(err, ShouldBeNil)
				So(fileInfo.Mode().Perm(), ShouldEqual, os.FileMode(0o600))
			})
		})
	})
}

func cursorStatusLine(t *testing.T, out string) string {
	t.Helper()

	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "cursor") {
			return line
		}
	}

	t.Fatalf("no cursor line in status output:\n%s", out)

	return ""
}

func TestStatusDedupesKindsForCursor(t *testing.T) {
	Convey("Given a Cursor install", t, func() {
		home := gwsHome(t)
		gwsWrite(t, filepath.Join(home, ".cursor", "mcp.json"), `{"mcpServers":{}}`)

		if _, err := gwsRun(t, "init"); err != nil {
			t.Fatal(err)
		}

		Convey("When status runs", func() {
			out, err := gwsRun(t, "status")
			So(err, ShouldBeNil)

			Convey("Then every kind is listed once", func() {
				line := cursorStatusLine(t, out)
				So(strings.Count(line, "projects:"), ShouldEqual, 1)
				So(strings.Count(line, "mcp:"), ShouldEqual, 1)
			})
		})
	})
}
