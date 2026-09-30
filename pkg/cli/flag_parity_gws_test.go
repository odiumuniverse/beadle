package cli

import (
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// TestStatusOutdatedOnlyFlag covers the rename decided in
// verger/docs/reviews/FLAG-PARITY-1.md §Decisions 3: the flag the user reads
// is --outdated-only, and --check stays as a hidden alias so a script written
// against the old name keeps working without the surface carrying two names.
func TestStatusOutdatedOnlyFlag(t *testing.T) {
	Convey("Given the status command", t, func() {
		home := gwsHome(t)
		_ = home

		if _, err := gwsRun(t, "init"); err != nil {
			t.Fatal(err)
		}

		Convey("Then --outdated-only is the flag it advertises", func() {
			help, err := gwsRun(t, "status", "--help")
			So(err, ShouldBeNil)
			So(help, ShouldContainSubstring, "--outdated-only")
		})

		Convey("And --check is still accepted, as the old name", func() {
			_, err := gwsRun(t, "status", "--check")
			So(err, ShouldBeNil)
		})

		Convey("And --check is not advertised twice, under its old name", func() {
			help, err := gwsRun(t, "status", "--help")
			So(err, ShouldBeNil)
			So(strings.Count(help, "--outdated-only"), ShouldEqual, 1)
			So(help, ShouldNotContainSubstring, "--check")
		})

		Convey("And the two spellings do the same work", func() {
			newOut, err := gwsRun(t, "status", "--outdated-only")
			So(err, ShouldBeNil)

			oldOut, err := gwsRun(t, "status", "--check")
			So(err, ShouldBeNil)

			So(oldOut, ShouldEqual, newOut)
		})
	})
}

// TestVersionSubcommandAndFlag covers FLAG-PARITY-1 §Decisions 8: beadle takes
// both a `version` subcommand and -v/--version, so it answers to the same two
// shapes verger does.
func TestVersionSubcommandAndFlag(t *testing.T) {
	Convey("Given the root command", t, func() {
		Convey("Then `beadle version` prints the version", func() {
			out, err := gwsRun(t, "version")
			So(err, ShouldBeNil)
			So(out, ShouldContainSubstring, "test")
		})

		Convey("And --version prints the same thing", func() {
			flagOut, err := gwsRun(t, "--version")
			So(err, ShouldBeNil)

			subOut, err := gwsRun(t, "version")
			So(err, ShouldBeNil)

			So(flagOut, ShouldContainSubstring, "test")
			So(flagOut, ShouldEqual, subOut)
		})

		Convey("And -v prints the same thing too", func() {
			short, err := gwsRun(t, "-v")
			So(err, ShouldBeNil)
			So(short, ShouldContainSubstring, "test")
		})
	})
}
