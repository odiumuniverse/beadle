package skill_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/skill"
)

func TestHasRoot(t *testing.T) {
	Convey("Given a directory with a root SKILL.md", t, func() {
		dir := t.TempDir()
		So(os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# x\n"), 0o600), ShouldBeNil)

		Convey("Then it is a skill root", func() {
			So(skill.HasRoot(dir), ShouldBeTrue)
		})
	})

	Convey("Given a directory without SKILL.md", t, func() {
		dir := t.TempDir()
		So(os.WriteFile(filepath.Join(dir, "README.md"), []byte("x\n"), 0o600), ShouldBeNil)

		Convey("Then it is not a skill root", func() {
			So(skill.HasRoot(dir), ShouldBeFalse)
		})
	})

	Convey("Given only a nested SKILL.md", t, func() {
		dir := t.TempDir()
		So(os.MkdirAll(filepath.Join(dir, "nested"), 0o750), ShouldBeNil)
		So(os.WriteFile(filepath.Join(dir, "nested", "SKILL.md"), []byte("# x\n"), 0o600), ShouldBeNil)

		Convey("Then the directory is not a skill root", func() {
			So(skill.HasRoot(dir), ShouldBeFalse)
		})
	})

	Convey("Given SKILL.md with a different case", t, func() {
		dir := t.TempDir()
		So(os.WriteFile(filepath.Join(dir, "skill.md"), []byte("# x\n"), 0o600), ShouldBeNil)

		Convey("Then the name match stays case-sensitive", func() {
			So(skill.HasRoot(dir), ShouldBeFalse)
		})
	})

	Convey("Given a broken SKILL.md symlink", t, func() {
		dir := t.TempDir()
		So(os.Symlink(filepath.Join(dir, "missing.md"), filepath.Join(dir, "SKILL.md")), ShouldBeNil)

		Convey("Then it is not a skill root", func() {
			So(skill.HasRoot(dir), ShouldBeFalse)
		})
	})

	Convey("Given a SKILL.md symlink to a regular file", t, func() {
		dir := t.TempDir()
		target := filepath.Join(t.TempDir(), "real.md")
		So(os.WriteFile(target, []byte("# x\n"), 0o600), ShouldBeNil)
		So(os.Symlink(target, filepath.Join(dir, "SKILL.md")), ShouldBeNil)

		Convey("Then it is a skill root", func() {
			So(skill.HasRoot(dir), ShouldBeTrue)
		})
	})

	Convey("Given a directory named SKILL.md", t, func() {
		dir := t.TempDir()
		So(os.MkdirAll(filepath.Join(dir, "SKILL.md"), 0o750), ShouldBeNil)

		Convey("Then it is not a skill root", func() {
			So(skill.HasRoot(dir), ShouldBeFalse)
		})
	})

	Convey("Given a missing directory", t, func() {
		Convey("Then it is not a skill root", func() {
			So(skill.HasRoot(filepath.Join(t.TempDir(), "missing")), ShouldBeFalse)
		})
	})
}

func TestTreeDigest(t *testing.T) {
	Convey("Given a skill tree", t, func() {
		tree := skill.Tree{"SKILL.md": []byte("# x\n"), "docs/a.txt": []byte("a\n")}

		Convey("When the digest is computed twice", func() {
			Convey("Then it is deterministic", func() {
				So(skill.TreeDigest(tree), ShouldEqual, skill.TreeDigest(tree))
			})
		})

		Convey("When a file changes", func() {
			changed := skill.Tree{"SKILL.md": []byte("# y\n"), "docs/a.txt": []byte("a\n")}

			Convey("Then the digest changes", func() {
				So(skill.TreeDigest(changed), ShouldNotEqual, skill.TreeDigest(tree))
			})
		})

		Convey("When a file moves", func() {
			moved := skill.Tree{"SKILL.md": []byte("# x\n"), "docs/b.txt": []byte("a\n")}

			Convey("Then the digest changes", func() {
				So(skill.TreeDigest(moved), ShouldNotEqual, skill.TreeDigest(tree))
			})
		})
	})
}

func TestReadDirRequiresRoot(t *testing.T) {
	Convey("Given a skills directory with one valid and one rootless entry", t, func() {
		dir := t.TempDir()
		So(os.MkdirAll(filepath.Join(dir, "valid"), 0o750), ShouldBeNil)
		So(os.WriteFile(filepath.Join(dir, "valid", "SKILL.md"), []byte("# x\n"), 0o600), ShouldBeNil)
		So(os.MkdirAll(filepath.Join(dir, "rootless"), 0o750), ShouldBeNil)
		So(os.WriteFile(filepath.Join(dir, "rootless", "note.txt"), []byte("x\n"), 0o600), ShouldBeNil)

		Convey("When the directory is read", func() {
			skills, err := skill.ReadDir(dir)

			Convey("Then only the valid skill is returned", func() {
				So(err, ShouldBeNil)
				So(skills, ShouldHaveLength, 1)
				So(skills, ShouldContainKey, "valid")
			})
		})
	})
}
