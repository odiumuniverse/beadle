package skill_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/skill"
)

func TestStatTree(t *testing.T) {
	Convey("Given a skill tree with a nested file", t, func() {
		dir := t.TempDir()
		So(os.MkdirAll(filepath.Join(dir, "refs"), 0o750), ShouldBeNil)
		So(os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# alpha\n"), 0o600), ShouldBeNil)
		So(os.WriteFile(filepath.Join(dir, "refs", "notes.md"), []byte("note\n"), 0o600), ShouldBeNil)

		base, err := skill.StatTree(dir)
		So(err, ShouldBeNil)

		Convey("When nothing changes", func() {
			again, err := skill.StatTree(dir)

			Convey("Then the fingerprint is stable", func() {
				So(err, ShouldBeNil)
				So(again.Fingerprint, ShouldEqual, base.Fingerprint)
			})
		})

		Convey("When a file's content changes with the same size", func() {
			before, err := os.Stat(filepath.Join(dir, "SKILL.md"))
			So(err, ShouldBeNil)

			So(os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# betaa\n"), 0o600), ShouldBeNil)
			So(os.Chtimes(filepath.Join(dir, "SKILL.md"), before.ModTime(), before.ModTime()), ShouldBeNil)

			changed, err := skill.StatTree(dir)

			Convey("Then the fingerprint does not change: size and mtime decide", func() {
				So(err, ShouldBeNil)
				So(changed.Fingerprint, ShouldEqual, base.Fingerprint)
			})
		})

		Convey("When a file's content changes with a new size", func() {
			So(os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# alpha and more\n"), 0o600), ShouldBeNil)

			changed, err := skill.StatTree(dir)

			Convey("Then the fingerprint changes", func() {
				So(err, ShouldBeNil)
				So(changed.Fingerprint, ShouldNotEqual, base.Fingerprint)
			})
		})

		Convey("When only a file's modification time changes", func() {
			later := time.Now().Add(time.Minute)
			So(os.Chtimes(filepath.Join(dir, "refs", "notes.md"), later, later), ShouldBeNil)

			changed, err := skill.StatTree(dir)

			Convey("Then the fingerprint changes and Latest moves", func() {
				So(err, ShouldBeNil)
				So(changed.Fingerprint, ShouldNotEqual, base.Fingerprint)
				So(changed.Latest, ShouldEqual, later)
			})
		})

		Convey("When a file's permission bits change", func() {
			So(os.Chmod(filepath.Join(dir, "SKILL.md"), 0o640), ShouldBeNil) //nolint:gosec // G302: loosened on purpose to prove a chmod invalidates the fingerprint

			changed, err := skill.StatTree(dir)

			Convey("Then the fingerprint changes: a chmod is not served from the cache", func() {
				So(err, ShouldBeNil)
				So(changed.Fingerprint, ShouldNotEqual, base.Fingerprint)
			})
		})

		Convey("When a junk file appears", func() {
			So(os.WriteFile(filepath.Join(dir, ".DS_Store"), []byte("junk\n"), 0o600), ShouldBeNil)

			after, err := skill.StatTree(dir)

			Convey("Then the fingerprint is unchanged: junk is not part of the tree", func() {
				So(err, ShouldBeNil)
				So(after.Fingerprint, ShouldEqual, base.Fingerprint)
			})
		})

		Convey("When a symlink appears", func() {
			So(os.Symlink(filepath.Join(dir, "SKILL.md"), filepath.Join(dir, "link.md")), ShouldBeNil)

			after, err := skill.StatTree(dir)

			Convey("Then the fingerprint is unchanged: symlinks are not part of the tree", func() {
				So(err, ShouldBeNil)
				So(after.Fingerprint, ShouldEqual, base.Fingerprint)
			})
		})

		Convey("When a file is removed", func() {
			So(os.Remove(filepath.Join(dir, "refs", "notes.md")), ShouldBeNil)

			after, err := skill.StatTree(dir)

			Convey("Then the fingerprint changes", func() {
				So(err, ShouldBeNil)
				So(after.Fingerprint, ShouldNotEqual, base.Fingerprint)
			})
		})
	})

	Convey("Given a flat skill file", t, func() {
		file := filepath.Join(t.TempDir(), "flat.md")
		So(os.WriteFile(file, []byte("# flat\n"), 0o600), ShouldBeNil)

		Convey("When it is listed", func() {
			stat, err := skill.StatTree(file)

			Convey("Then it is one SKILL.md entry, exactly like ReadTree reads it", func() {
				So(err, ShouldBeNil)
				So(stat.Fingerprint, ShouldNotBeEmpty)

				tree, err := skill.ReadTree(file)
				So(err, ShouldBeNil)
				So(tree, ShouldHaveLength, 1)
				So(tree, ShouldContainKey, "SKILL.md")
			})
		})
	})
}

func TestStatTreeCoversReadTree(t *testing.T) {
	Convey("Given two twin trees, one with junk and a symlink", t, func() {
		root := t.TempDir()
		plain := filepath.Join(root, "plain")
		noisy := filepath.Join(root, "noisy")

		for _, dir := range []string{plain, noisy} {
			So(os.MkdirAll(filepath.Join(dir, "refs"), 0o750), ShouldBeNil)
			So(os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# alpha\n"), 0o600), ShouldBeNil)
			So(os.WriteFile(filepath.Join(dir, "refs", "notes.md"), []byte("note\n"), 0o600), ShouldBeNil)
		}

		So(os.WriteFile(filepath.Join(noisy, ".DS_Store"), []byte("junk\n"), 0o600), ShouldBeNil)
		So(os.MkdirAll(filepath.Join(noisy, "__pycache__"), 0o750), ShouldBeNil)
		So(os.WriteFile(filepath.Join(noisy, "__pycache__", "x.pyc"), []byte("junk\n"), 0o600), ShouldBeNil)
		So(os.Symlink(filepath.Join(noisy, "SKILL.md"), filepath.Join(noisy, "link.md")), ShouldBeNil)

		stamp := time.Now().Add(-time.Hour)
		for _, rel := range []string{"SKILL.md", filepath.Join("refs", "notes.md")} {
			So(os.Chtimes(filepath.Join(plain, rel), stamp, stamp), ShouldBeNil)
			So(os.Chtimes(filepath.Join(noisy, rel), stamp, stamp), ShouldBeNil)
		}

		Convey("When both are listed", func() {
			plainStat, err := skill.StatTree(plain)
			So(err, ShouldBeNil)

			noisyStat, err := skill.StatTree(noisy)
			So(err, ShouldBeNil)

			Convey("Then junk and symlinks are invisible: the fingerprints match", func() {
				So(noisyStat.Fingerprint, ShouldEqual, plainStat.Fingerprint)
				So(noisyStat.Latest, ShouldEqual, plainStat.Latest)
			})
		})
	})
}
