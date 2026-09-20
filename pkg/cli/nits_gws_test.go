package cli

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

const nitsSecret = "ghp_abcdefghijklmnopqrstuvwxyz012345"

func captureLogs(t *testing.T, fn func() (string, error)) (string, error) {
	t.Helper()

	old := os.Stdout

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	os.Stdout = writer

	done := make(chan string, 1)

	go func() {
		data, _ := io.ReadAll(reader)
		done <- string(data)
	}()

	_, runErr := fn()

	_ = writer.Close()

	os.Stdout = old

	captured := <-done

	_ = reader.Close()

	return ansiPattern.ReplaceAllString(captured, ""), runErr
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestDoctorDoesNotClaimSecretMove(t *testing.T) {
	Convey("Given a memory note with a secret-like line", t, func() {
		home := gwsHome(t)

		gwsWrite(t, filepath.Join(home, ".claude", "projects", "-Users-test", "memory", "note.md"), "# note\ntoken: "+nitsSecret+"\n")

		if _, err := gwsRun(t, "init"); err != nil {
			t.Fatal(err)
		}

		Convey("When doctor runs", func() {
			logs, err := captureLogs(t, func() (string, error) { return gwsRun(t, "doctor") })
			So(err, ShouldBeNil)

			Convey("Then it never claims a secret move", func() {
				So(logs, ShouldNotContainSubstring, "moved to the vault store")
			})
		})

		Convey("When sync runs", func() {
			logs, err := captureLogs(t, func() (string, error) { return gwsRun(t, "sync") })
			So(err, ShouldBeNil)

			Convey("Then it reports the move once with a count", func() {
				So(strings.Count(logs, "moved to the vault store"), ShouldEqual, 1)
				So(logs, ShouldContainSubstring, "count=1")
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
