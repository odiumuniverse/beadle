package fsutil_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
)

// A root that arrived from outside the program is not necessarily spelled the
// way beadle spells the paths it builds from it.
//
// macOS hands TMPDIR over with a trailing separator and a mktemp template
// doubles it, so this is the ordinary spelling, not a contrived one:
//
//	TMPDIR = /var/folders/s8/…/T/
//	mktemp -d "${TMPDIR}/t-XXXXXX"  ->  /var/folders/s8/…/T//t-oRHUcf
//	t.TempDir()                     ->  …/T//t-XXXX/TestZZ…/001
//
// Nothing cleans that on the way in. So the root keeps its doubled separator
// while every path derived from it through filepath.Join does not, and the two
// halves of a containment test end up two different strings.
func TestARootIsCleanedWhereItIsRead(t *testing.T) {
	Convey("Given roots spelled with doubled and trailing separators", t, func() {
		Convey("Then they canonicalise to one spelling", func() {
			So(fsutil.Root("/x//y/"), ShouldEqual, "/x/y")
			So(fsutil.Root("/x/y/"), ShouldEqual, "/x/y")
			So(fsutil.Root("/x/./y"), ShouldEqual, "/x/y")
			So(fsutil.Root("/x/y//"), ShouldEqual, "/x/y")
		})

		Convey("And HOME spelled '/x//y/' is the same root as '/x/y'", func() {
			t.Setenv("HOME", "/x//y/")

			So(fsutil.RootEnv("HOME"), ShouldEqual, "/x/y")
			So(fsutil.RootEnv("HOME"), ShouldEqual, fsutil.Root("/x/y/"))
		})

		Convey("And os.UserHomeDir's spelling is cleaned too", func() {
			t.Setenv("HOME", "/x//y/")

			home, err := fsutil.UserHome()
			So(err, ShouldBeNil)
			So(home, ShouldEqual, "/x/y")
		})

		Convey("And a path derived from the cleaned root compares equal to it", func() {
			So(fsutil.Under(fsutil.Root("/x//y/"), filepath.Join("/x", "y", "f")), ShouldBeTrue)
		})

		Convey("And an unset variable is not a root", func() {
			// filepath.Clean("") is ".", a real and walkable directory. A blank
			// root must stay blank or every later path is silently relative to
			// the working directory.
			t.Setenv("BEADLE_PROBE_UNSET", "")

			So(fsutil.RootEnv("BEADLE_PROBE_UNSET"), ShouldEqual, "")
			So(fsutil.RootEnv("BEADLE_PROBE_ABSENT"), ShouldEqual, "")
			So(fsutil.Root(""), ShouldEqual, "")
		})

		Convey("And the user home is not invented when the OS cannot say", func() {
			// Sanity check on the wrapper itself: with HOME set it returns it,
			// cleaned, and the error is nil.
			t.Setenv("HOME", "/x//y/")

			home, err := fsutil.UserHome()
			So(err, ShouldBeNil)
			So(home, ShouldEqual, filepath.Clean(os.Getenv("HOME")))
		})
	})
}
