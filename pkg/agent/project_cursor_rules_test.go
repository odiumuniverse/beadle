package agent_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func cursorRulesSurfaceOf(t *testing.T, cwd string) agent.Surface {
	t.Helper()

	for _, surface := range agent.Cursor(t.TempDir(), cwd).SurfacesOf(kind.Projects) {
		if file, ok := surface.(agent.ProjectFile); ok && file.ProjectRel() == ".cursor/rules" {
			return surface
		}
	}

	t.Fatal("no .cursor/rules surface")

	return nil
}

var cursorRuleKey = regexp.MustCompile(`^[^/]+/\.cursor/rules/[A-Za-z0-9][A-Za-z0-9._-]*\.mdc$`)

func TestCursorRulesSurfaceReadsOnlyValidNames(t *testing.T) {
	Convey("Given a .cursor/rules directory with valid and invalid entries", t, func() {
		cwd := t.TempDir()
		dir := filepath.Join(cwd, ".cursor", "rules")

		writeFile(t, filepath.Join(dir, "style.mdc"), "# style\n")
		writeFile(t, filepath.Join(dir, "A-B_c.mdc"), "# mixed\n")
		writeFile(t, filepath.Join(dir, ".hidden.mdc"), "# hidden\n")
		writeFile(t, filepath.Join(dir, "notes.txt"), "# not mdc\n")
		writeFile(t, filepath.Join(dir, "sub", "nested.mdc"), "# nested\n")

		surface := cursorRulesSurfaceOf(t, cwd)

		Convey("When the surface reads", func() {
			snap, err := surface.Read(t.Context())

			Convey("Then only flat valid .mdc names become rules", func() {
				So(err, ShouldBeNil)
				So(snap.Items, ShouldHaveLength, 2)

				for key := range snap.Items {
					So(cursorRuleKey.MatchString(key), ShouldBeTrue)
				}
			})
		})
	})
}

func TestCursorRulesSurfaceWriteKeepsForeignFiles(t *testing.T) {
	Convey("Given a .cursor/rules directory with a stale rule and a foreign file", t, func() {
		cwd := t.TempDir()
		dir := filepath.Join(cwd, ".cursor", "rules")

		writeFile(t, filepath.Join(dir, "style.mdc"), "# style\n")
		writeFile(t, filepath.Join(dir, "old.mdc"), "# old\n")
		writeFile(t, filepath.Join(dir, "notes.txt"), "foreign\n")

		surface := cursorRulesSurfaceOf(t, cwd)

		snap, err := surface.Read(t.Context())
		So(err, ShouldBeNil)
		So(snap.Items, ShouldHaveLength, 2)

		styleKey := ""

		for key := range snap.Items {
			if filepath.Base(key) == "style.mdc" {
				styleKey = key
			}
		}

		So(styleKey, ShouldNotBeEmpty)

		prefix := styleKey[:len(styleKey)-len("style.mdc")]

		desired := kind.Items{
			styleKey:                 []byte("# style v2\n"),
			prefix + "new.mdc":       []byte("# new\n"),
			prefix + "ghost.mdc":     []byte("# ghost\n"),
			prefix + "notes.txt":     []byte("# ignored\n"),
			prefix + "sub/nested.md": []byte("# ignored\n"),
		}

		Convey("When the surface writes", func() {
			So(surface.Write(t.Context(), desired), ShouldBeNil)

			Convey("Then changed and new rules land, the stale one is removed and foreign files stay", func() {
				So(readFile(t, filepath.Join(dir, "style.mdc")), ShouldEqual, "# style v2\n")
				So(readFile(t, filepath.Join(dir, "new.mdc")), ShouldEqual, "# new\n")
				So(readFile(t, filepath.Join(dir, "ghost.mdc")), ShouldEqual, "# ghost\n")

				_, err := os.Stat(filepath.Join(dir, "old.mdc"))
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)

				_, err = os.Stat(filepath.Join(dir, "sub", "nested.mdc"))
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)

				So(readFile(t, filepath.Join(dir, "notes.txt")), ShouldEqual, "foreign\n")
			})
		})
	})
}
