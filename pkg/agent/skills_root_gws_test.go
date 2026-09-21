package agent_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func TestSkillsSurfaceRequiresRootSkillFile(t *testing.T) {
	Convey("Given a skills directory with valid, rootless and nested-only entries", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".claude", "skills")

		writeFile(t, filepath.Join(dir, "valid", "SKILL.md"), "# valid\n")
		writeFile(t, filepath.Join(dir, "rootless", "notes.txt"), "x\n")
		writeFile(t, filepath.Join(dir, "nested-only", "sub", "SKILL.md"), "# nested\n")

		target := filepath.Join(home, "foreign", "linked")
		writeFile(t, filepath.Join(target, "SKILL.md"), "# linked\n")
		So(os.Symlink(target, filepath.Join(dir, "linked")), ShouldBeNil)

		invalid := filepath.Join(home, "foreign", "invalid")
		writeFile(t, filepath.Join(invalid, "notes.txt"), "x\n")
		So(os.Symlink(invalid, filepath.Join(dir, "linked-invalid")), ShouldBeNil)

		claude := agent.ClaudeCode(home, home)

		Convey("When the surface reads the directory", func() {
			snap := snapshot(t, claude, kind.Skills)

			Convey("Then only valid skills enter the snapshot", func() {
				So(snap.Items, ShouldContainKey, "valid/SKILL.md")
				So(snap.Items, ShouldContainKey, "linked/SKILL.md")
				So(snap.Items, ShouldNotContainKey, "rootless/notes.txt")
				So(snap.Items, ShouldNotContainKey, "nested-only/sub/SKILL.md")
				So(snap.Items, ShouldNotContainKey, "linked-invalid/notes.txt")
			})
		})

		Convey("When the readable refs are listed", func() {
			refs := readableRefs(t, claude)
			names := make([]string, 0, len(refs))

			for _, ref := range refs {
				names = append(names, ref.Name)
			}

			Convey("Then only valid skills are readable", func() {
				So(names, ShouldResemble, []string{"linked", "valid"})
			})
		})
	})
}
