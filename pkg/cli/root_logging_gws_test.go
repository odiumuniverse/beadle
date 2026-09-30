package cli

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// A --json command's stdout is a document. One log line ahead of it makes
// `beadle doctor --json | jq` fail at character 0, so nothing but the document
// may reach that channel.
func TestTheRootLoggerKeepsStdoutParseable(t *testing.T) {
	Convey("Given an initialized vault", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		isolateTestRoots(t)

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		cases := [][]string{
			{"doctor", "--json"},
			{"plugins", "list", "--json"},
		}

		for _, args := range cases {
			Convey("When "+strings.Join(args, " ")+" runs", func() {
				stdout, _, err := runCLISplit(t, args...)
				So(err, ShouldBeNil)

				Convey("Then stdout is the document and nothing else", func() {
					var doc any

					So(json.Unmarshal([]byte(stdout), &doc), ShouldBeNil)
				})
			})
		}

		Convey("When a --json command has run", func() {
			// The decision this pins, stated as the property a caller can see:
			// a run that will print a document emits no info record, because
			// info is the level that would land on stdout in front of it.
			a := &app{errOut: io.Discard}
			root := newRootCmdWithApp(a, Options{Version: "test"})

			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs([]string{"doctor", "--json"})
			So(root.ExecuteContext(t.Context()), ShouldBeNil)

			So(a.logger.Log().Enabled(t.Context(), slog.LevelInfo), ShouldBeFalse)

			Convey("Then --verbose still asks for it on purpose", func() {
				verbose := &app{errOut: io.Discard}
				vroot := newRootCmdWithApp(verbose, Options{Version: "test"})

				vroot.SetOut(io.Discard)
				vroot.SetErr(io.Discard)
				vroot.SetArgs([]string{"doctor", "--json", "--verbose"})
				So(vroot.ExecuteContext(t.Context()), ShouldBeNil)

				So(verbose.logger.Log().Enabled(t.Context(), slog.LevelInfo), ShouldBeTrue)
			})
		})

		Convey("When a human command has run", func() {
			// stdout is a data channel for a human run too - a table, a pipe -
			// so no run emits info unless -v asks for it. A notice that must
			// always be visible is written to errOut as a plain line instead.
			a := &app{errOut: io.Discard}
			root := newRootCmdWithApp(a, Options{Version: "test"})

			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs([]string{"status"})
			So(root.ExecuteContext(t.Context()), ShouldBeNil)

			So(a.logger.Log().Enabled(t.Context(), slog.LevelInfo), ShouldBeFalse)
		})

		Convey("When the engine logs while a --json command runs", func() {
			// The fan-in note is engine-level and fires before the command
			// prints, which is how it reached stdout in the first place. It
			// fires exactly when the working directory is not a project, so
			// the test stands in one.
			t.Chdir(t.TempDir())

			stdout, _, err := runCLISplit(t, "doctor", "--json")
			So(err, ShouldBeNil)

			So(stdout, ShouldNotContainSubstring, "inbox fan-in")

			var doc any

			So(json.Unmarshal([]byte(stdout), &doc), ShouldBeNil)
		})
	})
}
