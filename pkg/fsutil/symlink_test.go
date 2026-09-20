package fsutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func stubSymlinkLinker(t *testing.T, link func(oldname, newname string) error) {
	t.Helper()

	previous := symlinkLinker
	symlinkLinker = link

	t.Cleanup(func() { symlinkLinker = previous })
}

func TestUnsupportedSymlinkFS(t *testing.T) {
	Convey("Given a table of filesystem types", t, func() {
		tests := map[string]bool{
			"exfat": true,
			"EXFAT": true,
			"msdos": true,
			"vfat":  true,
			"smb":   false,
			"cifs":  false,
			"apfs":  false,
			"ext4":  false,
		}

		for fsType, want := range tests {
			Convey("When checking "+fsType, func() {
				Convey("Then support matches", func() {
					So(UnsupportedSymlinkFS(fsType), ShouldEqual, want)
				})
			})
		}
	})
}

func TestClassifySymlinkErr(t *testing.T) {
	Convey("Given unsupported symlink errors", t, func() {
		unsupported := map[string]error{
			"EPERM":      syscall.EPERM,
			"EOPNOTSUPP": syscall.EOPNOTSUPP,
			"ENOTSUP":    syscall.ENOTSUP,
			"ENOSYS":     syscall.ENOSYS,
		}

		for name, err := range unsupported {
			Convey("When classifying "+name, func() {
				wrapped := fmt.Errorf("symlink: %w", &os.PathError{Op: "symlink", Path: "x", Err: err})

				Convey("Then it maps to ErrSymlinksUnsupported", func() {
					So(errors.Is(classifySymlinkErr(wrapped), ErrSymlinksUnsupported), ShouldBeTrue)
				})
			})
		}
	})

	Convey("Given unrelated symlink errors", t, func() {
		damage := map[string]error{
			"EACCES":  syscall.EACCES,
			"EEXIST":  syscall.EEXIST,
			"ENOENT":  syscall.ENOENT,
			"ENOTDIR": syscall.ENOTDIR,
		}

		for name, err := range damage {
			Convey("When classifying "+name, func() {
				classified := classifySymlinkErr(&os.PathError{Op: "symlink", Path: "x", Err: err})

				Convey("Then it stays unrelated to unsupported", func() {
					So(errors.Is(classified, ErrSymlinksUnsupported), ShouldBeFalse)
					So(errors.Is(classified, err), ShouldBeTrue)
				})
			})
		}
	})
}

func TestReplaceSymlinkClassifiesUnsupported(t *testing.T) {
	Convey("Given a linker that denies symlinks", t, func() {
		dir := t.TempDir()

		Convey("When the link error is unsupported", func() {
			for _, denied := range []error{syscall.EPERM, syscall.EOPNOTSUPP, syscall.ENOTSUP, syscall.ENOSYS} {
				Convey("With "+denied.Error(), func() {
					stubSymlinkLinker(t, func(_, _ string) error { return denied })

					Convey("Then it is classified as unsupported", func() {
						So(errors.Is(ReplaceSymlink(filepath.Join(dir, "alpha"), "target"), ErrSymlinksUnsupported), ShouldBeTrue)
					})
				})
			}
		})

		Convey("When the link error is unrelated", func() {
			stubSymlinkLinker(t, func(_, _ string) error { return syscall.EACCES })

			err := ReplaceSymlink(filepath.Join(dir, "alpha"), "target")

			entries, readErr := os.ReadDir(dir)

			Convey("Then the original error is kept and no temp artifacts remain", func() {
				So(errors.Is(err, ErrSymlinksUnsupported), ShouldBeFalse)
				So(errors.Is(err, syscall.EACCES), ShouldBeTrue)
				So(readErr, ShouldBeNil)
				So(entries, ShouldBeEmpty)
			})
		})
	})
}

func TestReplaceSymlinkReal(t *testing.T) {
	Convey("Given a real filesystem", t, func() {
		dir := t.TempDir()
		path := filepath.Join(dir, "alpha")

		Convey("When a symlink is created", func() {
			So(ReplaceSymlink(path, "target"), ShouldBeNil)

			link, err := os.Readlink(path)
			So(err, ShouldBeNil)

			entries, err := os.ReadDir(dir)

			Convey("Then it points at the target with no temp link left", func() {
				So(err, ShouldBeNil)
				So(link, ShouldEqual, "target")
				So(entries, ShouldHaveLength, 1)
			})
		})
	})
}
