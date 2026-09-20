package fsutil_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
)

func TestExpandHome(t *testing.T) {
	Convey("Given a table of paths", t, func() {
		home, err := os.UserHomeDir()
		So(err, ShouldBeNil)

		tests := map[string]string{
			"~":         home,
			"~/x":       filepath.Join(home, "x"),
			"/abs/path": "/abs/path",
			"relative":  "relative",
		}

		for in, want := range tests {
			Convey("When expanding "+in, func() {
				got, err := fsutil.ExpandHome(in)

				Convey("Then the path matches", func() {
					So(err, ShouldBeNil)
					So(got, ShouldEqual, want)
				})
			})
		}
	})
}

func TestExists(t *testing.T) {
	Convey("Given an existing and a missing path", t, func() {
		dir := t.TempDir()

		Convey("When existence is checked", func() {
			Convey("Then it is reported", func() {
				So(fsutil.Exists(dir), ShouldBeTrue)
				So(fsutil.Exists(filepath.Join(dir, "missing")), ShouldBeFalse)
			})
		})
	})
}
