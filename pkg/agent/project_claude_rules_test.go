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

func claudeRulesSurfaceOf(t *testing.T, cwd string) agent.Surface {
	t.Helper()

	for _, surface := range agent.ClaudeCode(t.TempDir(), cwd).SurfacesOf(kind.Projects) {
		if file, ok := surface.(agent.ProjectFile); ok && file.ProjectRel() == ".claude/rules" {
			return surface
		}
	}

	t.Fatal("no .claude/rules surface")

	return nil
}

var claudeRuleKey = regexp.MustCompile(`^[^/]+/\.claude/rules/[A-Za-z0-9][A-Za-z0-9._-]*\.md$`)

func TestClaudeRulesSurfaceReadsOnlyValidNames(t *testing.T) {
	Convey("Given a .claude/rules directory with valid and invalid entries", t, func() {
		cwd := t.TempDir()
		dir := filepath.Join(cwd, ".claude", "rules")

		writeFile(t, filepath.Join(dir, "style.md"), "# style\n")
		writeFile(t, filepath.Join(dir, "A-B_c.md"), "# mixed\n")
		writeFile(t, filepath.Join(dir, ".hidden.md"), "# hidden\n")
		writeFile(t, filepath.Join(dir, "notes.txt"), "# not md\n")
		writeFile(t, filepath.Join(dir, "legacy.mdc"), "# cursor dialect\n")
		writeFile(t, filepath.Join(dir, "sub", "nested.md"), "# nested\n")

		surface := claudeRulesSurfaceOf(t, cwd)

		Convey("When the surface reads", func() {
			snap, err := surface.Read(t.Context())

			Convey("Then only flat valid .md names become rules", func() {
				So(err, ShouldBeNil)
				So(snap.Items, ShouldHaveLength, 2)

				for key := range snap.Items {
					So(claudeRuleKey.MatchString(key), ShouldBeTrue)
				}
			})
		})
	})
}

func TestClaudeRulesSurfaceWriteKeepsForeignFiles(t *testing.T) {
	Convey("Given a .claude/rules directory with a stale rule and foreign files", t, func() {
		cwd := t.TempDir()
		dir := filepath.Join(cwd, ".claude", "rules")

		writeFile(t, filepath.Join(dir, "style.md"), "# style\n")
		writeFile(t, filepath.Join(dir, "old.md"), "# old\n")
		writeFile(t, filepath.Join(dir, "notes.txt"), "foreign\n")
		writeFile(t, filepath.Join(dir, "legacy.mdc"), "# cursor dialect\n")

		surface := claudeRulesSurfaceOf(t, cwd)

		snap, err := surface.Read(t.Context())
		So(err, ShouldBeNil)
		So(snap.Items, ShouldHaveLength, 2)

		styleKey := ""

		for key := range snap.Items {
			if filepath.Base(key) == "style.md" {
				styleKey = key
			}
		}

		So(styleKey, ShouldNotBeEmpty)

		prefix := styleKey[:len(styleKey)-len("style.md")]

		desired := kind.Items{
			styleKey:                    []byte("# style v2\n"),
			prefix + "new.md":           []byte("# new\n"),
			prefix + "ghost.md":         []byte("# ghost\n"),
			prefix + "notes.txt":        []byte("# ignored\n"),
			prefix + "legacy.mdc":       []byte("# ignored\n"),
			prefix + "sub/nested.md":    []byte("# ignored\n"),
			prefix + "sibling/other.md": []byte("# ignored\n"),
		}

		Convey("When the surface writes", func() {
			So(surface.Write(t.Context(), desired), ShouldBeNil)

			Convey("Then changed and new rules land, the stale one is removed and foreign files stay", func() {
				So(readFile(t, filepath.Join(dir, "style.md")), ShouldEqual, "# style v2\n")
				So(readFile(t, filepath.Join(dir, "new.md")), ShouldEqual, "# new\n")
				So(readFile(t, filepath.Join(dir, "ghost.md")), ShouldEqual, "# ghost\n")

				_, err := os.Stat(filepath.Join(dir, "old.md"))
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)

				_, err = os.Stat(filepath.Join(dir, "sub", "nested.md"))
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)

				So(readFile(t, filepath.Join(dir, "notes.txt")), ShouldEqual, "foreign\n")
				So(readFile(t, filepath.Join(dir, "legacy.mdc")), ShouldEqual, "# cursor dialect\n")

				rule, err := os.Stat(filepath.Join(dir, "new.md"))
				So(err, ShouldBeNil)
				So(rule.Mode().Perm(), ShouldEqual, os.FileMode(0o644))
			})

			Convey("When the directory is missing again", func() {
				So(os.RemoveAll(dir), ShouldBeNil)
				So(surface.Write(t.Context(), desired), ShouldBeNil)

				Convey("Then the surface recreates it with 0750 and writes rules 0644", func() {
					info, err := os.Stat(dir)
					So(err, ShouldBeNil)
					So(info.Mode().Perm(), ShouldEqual, os.FileMode(0o750))

					rule, err := os.Stat(filepath.Join(dir, "style.md"))
					So(err, ShouldBeNil)
					So(rule.Mode().Perm(), ShouldEqual, os.FileMode(0o644))
				})
			})
		})
	})
}

func TestClaudeRulesSurfaceRemoveKeepsForeignFiles(t *testing.T) {
	Convey("Given a .claude/rules directory with rules and foreign files", t, func() {
		cwd := t.TempDir()
		dir := filepath.Join(cwd, ".claude", "rules")

		writeFile(t, filepath.Join(dir, "style.md"), "# style\n")
		writeFile(t, filepath.Join(dir, "notes.txt"), "foreign\n")
		writeFile(t, filepath.Join(dir, "legacy.mdc"), "# cursor dialect\n")

		surface := claudeRulesSurfaceOf(t, cwd)

		Convey("When the surface removes its rules", func() {
			remover, ok := surface.(agent.ProjectRemover)
			So(ok, ShouldBeTrue)
			So(remover.Remove(), ShouldBeNil)

			Convey("Then only the .md rules are gone", func() {
				_, err := os.Stat(filepath.Join(dir, "style.md"))
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)

				So(readFile(t, filepath.Join(dir, "notes.txt")), ShouldEqual, "foreign\n")
				So(readFile(t, filepath.Join(dir, "legacy.mdc")), ShouldEqual, "# cursor dialect\n")
			})
		})
	})
}

func TestClaudeRulesSurfaceSymlinkGate(t *testing.T) {
	Convey("Given a .claude/rules entry that is a symlink", t, func() {
		cwd := t.TempDir()
		dir := filepath.Join(cwd, ".claude", "rules")

		writeFile(t, filepath.Join(dir, "style.md"), "# style\n")

		target := filepath.Join(t.TempDir(), "outside.md")
		writeFile(t, target, "# outside\n")

		So(os.Symlink(target, filepath.Join(dir, "linked.md")), ShouldBeNil)

		surface := claudeRulesSurfaceOf(t, cwd)

		Convey("When the surface reads", func() {
			_, err := surface.Read(t.Context())

			Convey("Then the symlink is refused and the target is untouched", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "refusing to touch a symlink")
				So(readFile(t, target), ShouldEqual, "# outside\n")
			})
		})
	})
}

func TestClaudeRulesSurfaceMissingAndEmpty(t *testing.T) {
	Convey("Given a project without .claude/rules", t, func() {
		cwd := t.TempDir()

		surface := claudeRulesSurfaceOf(t, cwd)

		Convey("Then the reader is silent", func() {
			snap, err := surface.Read(t.Context())
			So(err, ShouldBeNil)
			So(snap.Items, ShouldBeEmpty)
		})

		Convey("Then an empty directory is silent too", func() {
			So(os.MkdirAll(filepath.Join(cwd, ".claude", "rules"), 0o750), ShouldBeNil)

			snap, err := surface.Read(t.Context())
			So(err, ShouldBeNil)
			So(snap.Items, ShouldBeEmpty)
		})
	})
}
