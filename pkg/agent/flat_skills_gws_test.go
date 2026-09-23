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

func TestFlatSkillReadOpenCode(t *testing.T) {
	Convey("Given an OpenCode skills directory with a flat copy", t, func() {
		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "skills", "alpha.md")
		writeFile(t, path, "# alpha\n")

		a := agent.OpenCode(home, t.TempDir())

		Convey("When the surface is read", func() {
			snap := snapshot(t, a, kind.Skills)

			Convey("Then the flat copy is a skill tree", func() {
				So(snap.Items["alpha/SKILL.md"], ShouldResemble, []byte("# alpha\n"))
				So(snap.ReadOnly, ShouldNotContainKey, "alpha")
				So(snap.Warnings, ShouldBeEmpty)
			})

			Convey("And it is a readable flat ref", func() {
				reader, ok := surfaceOf(t, a, kind.Skills).(agent.SkillReader)
				So(ok, ShouldBeTrue)

				refs, err := reader.ReadableSkills()
				So(err, ShouldBeNil)
				So(refs, ShouldHaveLength, 1)
				So(refs[0].Name, ShouldEqual, "alpha")
				So(refs[0].Root, ShouldEqual, path)
				So(refs[0].File, ShouldEqual, path)
			})
		})
	})
}

func TestFlatSkillShadowedByDirectory(t *testing.T) {
	Convey("Given a flat copy and a same-name skill directory", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode", "skills")
		flat := filepath.Join(dir, "alpha.md")

		writeFile(t, flat, "# flat\n")
		writeFile(t, filepath.Join(dir, "alpha", "SKILL.md"), "# dir\n")

		a := agent.OpenCode(home, t.TempDir())

		Convey("When the surface is read", func() {
			snap := snapshot(t, a, kind.Skills)

			Convey("Then the directory wins and the flat file stays untouched", func() {
				So(snap.Items["alpha/SKILL.md"], ShouldResemble, []byte("# dir\n"))
				So(strings.Join(snap.Warnings, "\n"), ShouldContainSubstring, "shadowed by alpha/SKILL.md")
				So(snap.ReadOnly, ShouldNotContainKey, "alpha")
				So(readFile(t, flat), ShouldEqual, "# flat\n")
			})

			Convey("And the flat reader reports the collision", func() {
				reader, ok := surfaceOf(t, a, kind.Skills).(agent.SkillFlatReader)
				So(ok, ShouldBeTrue)

				readable, shadowed, err := reader.FlatSkillRefs()
				So(err, ShouldBeNil)
				So(readable, ShouldBeEmpty)
				So(shadowed, ShouldResemble, map[string]string{"alpha": flat})
			})

			Convey("And the shadowed file is not a readable ref", func() {
				reader, ok := surfaceOf(t, a, kind.Skills).(agent.SkillReader)
				So(ok, ShouldBeTrue)

				refs, err := reader.ReadableSkills()
				So(err, ShouldBeNil)
				So(refs, ShouldHaveLength, 1)
				So(refs[0].File, ShouldBeEmpty)
			})
		})
	})
}

func TestFlatSkillReadPiOwnDir(t *testing.T) {
	Convey("Given a flat copy in the Pi skills directory", t, func() {
		home := t.TempDir()
		path := filepath.Join(home, ".pi", "agent", "skills", "beta.md")
		writeFile(t, path, "# beta\n")

		a := agent.Pi(home, t.TempDir())

		Convey("When Pi reads its skills", func() {
			snap := snapshot(t, a, kind.Skills)

			Convey("Then the flat copy is read", func() {
				So(snap.Items["beta/SKILL.md"], ShouldResemble, []byte("# beta\n"))

				reader, ok := surfaceOf(t, a, kind.Skills).(agent.SkillReader)
				So(ok, ShouldBeTrue)

				refs, err := reader.ReadableSkills()
				So(err, ShouldBeNil)
				So(refs, ShouldHaveLength, 1)
				So(refs[0].File, ShouldEqual, path)
			})
		})
	})
}

func TestFlatSkillsIgnoredInAlsoReads(t *testing.T) {
	Convey("Given flat copies in the shared skills directory", t, func() {
		home := t.TempDir()
		writeFile(t, filepath.Join(home, ".agents", "skills", "shared-flat.md"), "# shared\n")

		Convey("When Pi reads its skills", func() {
			a := agent.Pi(home, t.TempDir())
			snap := snapshot(t, a, kind.Skills)

			reader, ok := surfaceOf(t, a, kind.Skills).(agent.SkillReader)
			So(ok, ShouldBeTrue)

			refs, err := reader.ReadableSkills()
			So(err, ShouldBeNil)

			Convey("Then the shared flat copy is not a skill", func() {
				So(snap.Items, ShouldBeEmpty)
				So(refs, ShouldBeEmpty)
			})
		})

		Convey("And when OpenCode reads its skills", func() {
			a := agent.OpenCode(home, t.TempDir())
			snap := snapshot(t, a, kind.Skills)

			reader, ok := surfaceOf(t, a, kind.Skills).(agent.SkillReader)
			So(ok, ShouldBeTrue)

			refs, err := reader.ReadableSkills()
			So(err, ShouldBeNil)

			Convey("Then the shared flat copy is not a skill either", func() {
				So(snap.Items, ShouldBeEmpty)
				So(refs, ShouldBeEmpty)
			})
		})
	})
}

func TestFlatSkillsIgnoredWithoutCapability(t *testing.T) {
	Convey("Given a flat copy in the Claude skills directory", t, func() {
		home := t.TempDir()
		writeFile(t, filepath.Join(home, ".claude", "skills", "alpha.md"), "# alpha\n")

		a := agent.ClaudeCode(home, t.TempDir())

		Convey("When the surface is read", func() {
			snap := snapshot(t, a, kind.Skills)

			Convey("Then the host reads directories only", func() {
				So(snap.Items, ShouldBeEmpty)
				So(snap.Warnings, ShouldBeEmpty)
			})

			Convey("And the flat reader reports nothing", func() {
				reader, ok := surfaceOf(t, a, kind.Skills).(agent.SkillFlatReader)
				So(ok, ShouldBeTrue)

				readable, shadowed, err := reader.FlatSkillRefs()
				So(err, ShouldBeNil)
				So(readable, ShouldBeEmpty)
				So(shadowed, ShouldBeEmpty)
			})
		})
	})
}

func TestFlatSkillSymlink(t *testing.T) {
	Convey("Given a flat copy delivered as a symlink", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode", "skills")
		target := filepath.Join(home, "canon", "alpha.md")

		writeFile(t, target, "# alpha\n")
		So(os.MkdirAll(dir, 0o750), ShouldBeNil)
		So(os.Symlink(target, filepath.Join(dir, "alpha.md")), ShouldBeNil)

		a := agent.OpenCode(home, t.TempDir())

		Convey("When the surface is read", func() {
			snap := snapshot(t, a, kind.Skills)

			Convey("Then it is read and marked read-only", func() {
				So(snap.Items["alpha/SKILL.md"], ShouldResemble, []byte("# alpha\n"))
				So(snap.ReadOnly["alpha"], ShouldContainSubstring, "symlink to")
			})
		})
	})

	Convey("Given a flat symlink into an ignored plugin cache", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode", "skills")
		target := filepath.Join(home, ".claude", "plugins", "market", "alpha.md")

		writeFile(t, target, "# alpha\n")
		So(os.MkdirAll(dir, 0o750), ShouldBeNil)
		So(os.Symlink(target, filepath.Join(dir, "alpha.md")), ShouldBeNil)

		a := agent.OpenCode(home, t.TempDir())

		Convey("When the surface is read", func() {
			snap := snapshot(t, a, kind.Skills)

			reader, ok := surfaceOf(t, a, kind.Skills).(agent.SkillReader)
			So(ok, ShouldBeTrue)

			refs, err := reader.ReadableSkills()
			So(err, ShouldBeNil)

			Convey("Then it is skipped like a symlinked directory", func() {
				So(snap.Items, ShouldBeEmpty)
				So(refs, ShouldBeEmpty)
			})
		})
	})
}

func TestFlatSkillNonRegularEntry(t *testing.T) {
	Convey("Given a directory named like a flat skill", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode", "skills")

		So(os.MkdirAll(filepath.Join(dir, "alpha.md"), 0o750), ShouldBeNil)

		a := agent.OpenCode(home, t.TempDir())

		Convey("When the surface is read", func() {
			snap := snapshot(t, a, kind.Skills)

			reader, ok := surfaceOf(t, a, kind.Skills).(agent.SkillFlatReader)
			So(ok, ShouldBeTrue)

			readable, shadowed, err := reader.FlatSkillRefs()
			So(err, ShouldBeNil)

			Convey("Then it is not a flat copy and the read does not fail", func() {
				So(snap.Items, ShouldBeEmpty)
				So(snap.Warnings, ShouldBeEmpty)
				So(readable, ShouldBeEmpty)
				So(shadowed, ShouldBeEmpty)
			})
		})
	})

	Convey("Given a directory named like a flat skill next to a same-name directory", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode", "skills")

		So(os.MkdirAll(filepath.Join(dir, "alpha.md"), 0o750), ShouldBeNil)
		writeFile(t, filepath.Join(dir, "alpha", "SKILL.md"), "# dir\n")

		a := agent.OpenCode(home, t.TempDir())

		Convey("When the surface is read", func() {
			snap := snapshot(t, a, kind.Skills)

			reader, ok := surfaceOf(t, a, kind.Skills).(agent.SkillFlatReader)
			So(ok, ShouldBeTrue)

			readable, shadowed, err := reader.FlatSkillRefs()
			So(err, ShouldBeNil)

			Convey("Then no false shadow warning is reported", func() {
				So(snap.Items["alpha/SKILL.md"], ShouldResemble, []byte("# dir\n"))
				So(snap.Warnings, ShouldBeEmpty)
				So(readable, ShouldBeEmpty)
				So(shadowed, ShouldBeEmpty)
			})
		})
	})
}

func TestFlatSkillWrite(t *testing.T) {
	Convey("Given an OpenCode flat copy matching the canon", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode", "skills")
		flat := filepath.Join(dir, "alpha.md")

		writeFile(t, flat, "# alpha\n")

		a := agent.OpenCode(home, t.TempDir())
		surface := surfaceOf(t, a, kind.Skills)

		Convey("When the same tree is written", func() {
			So(surface.Write(t.Context(), kind.Items{"alpha/SKILL.md": []byte("# alpha\n")}), ShouldBeNil)

			Convey("Then the flat file stays and no directory appears", func() {
				So(readFile(t, flat), ShouldEqual, "# alpha\n")

				_, err := os.Stat(filepath.Join(dir, "alpha"))
				So(os.IsNotExist(err), ShouldBeTrue)
			})
		})

		Convey("When a changed tree is written", func() {
			So(surface.Write(t.Context(), kind.Items{"alpha/SKILL.md": []byte("# changed\n")}), ShouldBeNil)

			Convey("Then the directory carries the new content and the file stays", func() {
				So(readFile(t, filepath.Join(dir, "alpha", "SKILL.md")), ShouldEqual, "# changed\n")
				So(readFile(t, flat), ShouldEqual, "# alpha\n")
			})
		})

		Convey("When the name leaves the canon", func() {
			So(surface.Write(t.Context(), kind.Items{}), ShouldBeNil)

			Convey("Then the flat file is removed", func() {
				_, err := os.Stat(flat)
				So(os.IsNotExist(err), ShouldBeTrue)
			})
		})
	})
}
