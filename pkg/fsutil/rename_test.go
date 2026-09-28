package fsutil_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
)

func TestRenamePath(t *testing.T) {
	Convey("Given a file", t, func() {
		dir := t.TempDir()
		from := filepath.Join(dir, "old")
		to := filepath.Join(dir, "new")

		So(os.WriteFile(from, []byte("payload"), 0o600), ShouldBeNil)

		Convey("When it is renamed", func() {
			So(fsutil.RenamePath(from, to), ShouldBeNil)

			Convey("Then only the new name carries the content", func() {
				data, err := os.ReadFile(to) //nolint:gosec // G304: the test reads its own temp file
				So(err, ShouldBeNil)
				So(string(data), ShouldEqual, "payload")

				_, err = os.Stat(from)
				So(os.IsNotExist(err), ShouldBeTrue)
			})
		})
	})

	Convey("Given a directory tree", t, func() {
		dir := t.TempDir()
		from := filepath.Join(dir, "old")
		to := filepath.Join(dir, "new")

		So(os.MkdirAll(filepath.Join(from, "child"), 0o700), ShouldBeNil)
		So(os.WriteFile(filepath.Join(from, "child", "SKILL.md"), []byte("x"), 0o600), ShouldBeNil)

		Convey("When it is renamed", func() {
			So(fsutil.RenamePath(from, to), ShouldBeNil)

			Convey("Then the whole tree moved and the old name is free", func() {
				So(fsutil.Exists(filepath.Join(to, "child", "SKILL.md")), ShouldBeTrue)
				So(fsutil.Exists(from), ShouldBeFalse)
			})
		})
	})

	Convey("Given a source that does not exist", t, func() {
		dir := t.TempDir()

		Convey("When the rename runs", func() {
			So(fsutil.RenamePath(filepath.Join(dir, "missing"), filepath.Join(dir, "new")), ShouldNotBeNil)
		})
	})

	Convey("Given a destination that already exists", t, func() {
		dir := t.TempDir()
		from := filepath.Join(dir, "old")
		to := filepath.Join(dir, "new")

		// A non-empty destination makes rename(2) refuse on both platforms;
		// an empty one would be silently replaced.
		So(os.MkdirAll(from, 0o700), ShouldBeNil)
		So(os.MkdirAll(to, 0o700), ShouldBeNil)
		So(os.WriteFile(filepath.Join(to, "keep"), []byte("keep"), 0o600), ShouldBeNil)

		Convey("When the rename runs", func() {
			So(fsutil.RenamePath(from, to), ShouldNotBeNil)

			Convey("Then the existing tree is left intact", func() {
				So(fsutil.Exists(to), ShouldBeTrue)
				So(fsutil.Exists(from), ShouldBeTrue)
			})
		})
	})
}
