package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func ompNotices(a *agent.Agent, key string, value []byte) string {
	var out strings.Builder

	for _, notice := range a.Notices(kind.Commands, key, value) {
		out.WriteString(notice.Message)
		out.WriteString("\n")
	}

	return out.String()
}

func TestOmpCommands(t *testing.T) {
	Convey("Given an omp command file", t, func() {
		home := t.TempDir()

		withoutOmpEnv(t)

		a := agent.Omp(home, t.TempDir())
		dir := filepath.Join(home, ".omp", "agent", "commands")

		Convey("The canonical placeholders travel byte-for-byte", func() {
			writeFile(t, filepath.Join(dir, "greet.md"), "---\ndescription: Greet\n---\nSay $1 and $ARGUMENTS.\n")

			snap := snapshot(t, a, kind.Commands)
			So(string(snap.Items["greet.md"]), ShouldContainSubstring, "Say $1 and $ARGUMENTS.")

			canon := []byte("---\ndescription: greet\n---\nSay $1 and $ARGUMENTS.\n")

			So(surfaceOf(t, a, kind.Commands).Write(t.Context(), kind.Items{"greet.md": canon}), ShouldBeNil)
			So(readFile(t, filepath.Join(dir, "greet.md")), ShouldContainSubstring, "Say $1 and $ARGUMENTS.")

			// The transformed form is what lands in the file: a canon
			// command that carries the same body writes it unchanged.
			So(ompNotices(a, "greet.md", canon), ShouldBeEmpty)
		})

		Convey("When an omp-only construct is read back", func() {
			writeFile(t, filepath.Join(dir, "extra.md"), "---\ndescription: extra\n---\n"+
				"Use $@[1:2] and !`git status` and @src/file.go.\n")

			Convey("Then the constructs the canon cannot express are reported", func() {
				snap := snapshot(t, a, kind.Commands)
				So(snap.Warnings, ShouldNotBeEmpty)

				warnings := strings.Join(snap.Warnings, "\n")
				So(warnings, ShouldContainSubstring, "omp does not expand $@")
				So(warnings, ShouldContainSubstring, "omp does not expand shell blocks")
				So(warnings, ShouldContainSubstring, "omp does not expand file references")
			})
		})

		Convey("When the canon uses a placeholder omp cannot express", func() {
			value := []byte("---\ndescription: greet\n---\nUse ${1:-world} and $FILE.\n")

			Convey("Then no file is created and the doctor reports it", func() {
				So(surfaceOf(t, a, kind.Commands).Write(t.Context(), kind.Items{"greet.md": value}), ShouldBeNil)

				_, statErr := os.Stat(filepath.Join(dir, "greet.md"))
				So(os.IsNotExist(statErr), ShouldBeTrue)

				notice := ompNotices(a, "greet.md", value)
				So(notice, ShouldContainSubstring, "the template uses a placeholder omp cannot express")
			})
		})

		Convey("When a command is written twice", func() {
			canon := []byte("---\ndescription: greet\n---\nSay $1.\n")

			So(surfaceOf(t, a, kind.Commands).Write(t.Context(), kind.Items{"greet.md": canon}), ShouldBeNil)
			before := readFile(t, filepath.Join(dir, "greet.md"))

			Convey("Then the file is byte-stable", func() {
				So(surfaceOf(t, a, kind.Commands).Write(t.Context(), kind.Items{"greet.md": canon}), ShouldBeNil)
				So(readFile(t, filepath.Join(dir, "greet.md")), ShouldEqual, before)
			})
		})
	})
}
