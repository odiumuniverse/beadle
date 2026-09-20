package fsutil_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
)

func TestWriteFileAtomic(t *testing.T) {
	Convey("Given an atomic writer", t, func() {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.json")

		So(fsutil.WriteFileAtomic(path, []byte("v1"), 0o600), ShouldBeNil)

		got, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
		So(err, ShouldBeNil)

		info, err := os.Stat(path)
		So(err, ShouldBeNil)

		Convey("When the file is rewritten with a new mode", func() {
			So(fsutil.WriteFileAtomic(path, []byte("v2"), 0o644), ShouldBeNil)

			got, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
			So(err, ShouldBeNil)

			info, err := os.Stat(path)

			entries, readErr := os.ReadDir(dir)

			Convey("Then content and mode are updated with no temp files left", func() {
				So(err, ShouldBeNil)
				So(string(got), ShouldEqual, "v2")
				So(info.Mode().Perm(), ShouldEqual, os.FileMode(0o644))
				So(readErr, ShouldBeNil)
				So(entries, ShouldHaveLength, 1)
			})
		})

		Convey("Then the first write had the requested mode", func() {
			So(string(got), ShouldEqual, "v1")
			So(info.Mode().Perm(), ShouldEqual, os.FileMode(0o600))
		})
	})
}

func TestWriteFileAtomicChecked(t *testing.T) {
	Convey("Given a nil check", t, func() {
		path := filepath.Join(t.TempDir(), "config.json")

		Convey("When the file is written", func() {
			So(fsutil.WriteFileAtomicChecked(path, []byte("v1"), 0o600, nil), ShouldBeNil)

			got, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file

			Convey("Then it keeps the old behavior", func() {
				So(err, ShouldBeNil)
				So(string(got), ShouldEqual, "v1")
			})
		})
	})

	Convey("Given a passing check", t, func() {
		path := filepath.Join(t.TempDir(), "config.json")
		So(os.WriteFile(path, []byte("original"), 0o600), ShouldBeNil)

		Convey("When the file is written", func() {
			err := fsutil.WriteFileAtomicChecked(path, []byte("replacement"), 0o600, func() error { return nil })
			So(err, ShouldBeNil)

			got, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file

			Convey("Then it is replaced", func() {
				So(err, ShouldBeNil)
				So(string(got), ShouldEqual, "replacement")
			})
		})
	})

	Convey("Given a failing check on an existing file", t, func() {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.json")
		So(os.WriteFile(path, []byte("original"), 0o600), ShouldBeNil)

		sentinel := errors.New("changed concurrently")

		Convey("When the file is written", func() {
			err := fsutil.WriteFileAtomicChecked(path, []byte("replacement"), 0o600, func() error { return sentinel })
			So(errors.Is(err, sentinel), ShouldBeTrue)

			got, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
			So(err, ShouldBeNil)

			entries, err := os.ReadDir(dir)

			Convey("Then the file is kept and no temp files remain", func() {
				So(err, ShouldBeNil)
				So(string(got), ShouldEqual, "original")
				So(entries, ShouldHaveLength, 1)
			})
		})
	})

	Convey("Given a failing check on a new file", t, func() {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.json")

		sentinel := errors.New("appeared concurrently")

		Convey("When the file is written", func() {
			err := fsutil.WriteFileAtomicChecked(path, []byte("v1"), 0o600, func() error { return sentinel })
			So(errors.Is(err, sentinel), ShouldBeTrue)

			_, err = os.Stat(path)
			So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)

			entries, err := os.ReadDir(dir)

			Convey("Then the file was not created and no temp files remain", func() {
				So(err, ShouldBeNil)
				So(entries, ShouldBeEmpty)
			})
		})
	})
}

func TestReplaceSymlink(t *testing.T) {
	Convey("Given a symlink that is replaced repeatedly", t, func() {
		dir := t.TempDir()
		path := filepath.Join(dir, "current")

		So(fsutil.ReplaceSymlink(path, "/targets/v1"), ShouldBeNil)

		link, err := os.Readlink(path)
		So(err, ShouldBeNil)

		Convey("When the target changes and then stays the same", func() {
			So(fsutil.ReplaceSymlink(path, "/targets/v2"), ShouldBeNil)

			link, err = os.Readlink(path)
			So(err, ShouldBeNil)

			So(fsutil.ReplaceSymlink(path, "/targets/v2"), ShouldBeNil)

			link, err = os.Readlink(path)
			So(err, ShouldBeNil)

			entries, err := os.ReadDir(dir)

			Convey("Then the link points at v2 with no temp files left", func() {
				So(err, ShouldBeNil)
				So(link, ShouldEqual, "/targets/v2")
				So(entries, ShouldHaveLength, 1)
				So(entries[0].Name(), ShouldEqual, "current")
			})
		})

		Convey("Then the first target was applied", func() {
			So(link, ShouldEqual, "/targets/v1")
		})
	})
}

func TestReplaceSymlinkReplacesFile(t *testing.T) {
	Convey("Given a plain file where a symlink belongs", t, func() {
		dir := t.TempDir()
		path := filepath.Join(dir, "current")
		So(os.WriteFile(path, []byte("plain file"), 0o600), ShouldBeNil)

		Convey("When a symlink is put in its place", func() {
			So(fsutil.ReplaceSymlink(path, "/targets/v1"), ShouldBeNil)

			link, err := os.Readlink(path)
			So(err, ShouldBeNil)

			entries, err := os.ReadDir(dir)

			Convey("Then the file is replaced by the link", func() {
				So(err, ShouldBeNil)
				So(link, ShouldEqual, "/targets/v1")
				So(entries, ShouldHaveLength, 1)
			})
		})
	})
}
