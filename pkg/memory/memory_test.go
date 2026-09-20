package memory_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/memory"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestSlug(t *testing.T) {
	Convey("Given path-to-slug conversion", t, func() {
		Convey("When absolute and relative paths are converted", func() {
			abs, err := filepath.Abs("rel/file")

			Convey("Then slashes become dashes", func() {
				So(err, ShouldBeNil)
				So(memory.Slug("/Users/x/my/proj"), ShouldEqual, "-Users-x-my-proj")
				So(memory.Slug("/"), ShouldEqual, "-")
				So(memory.Slug("/Users/x/my agents"), ShouldEqual, "-Users-x-my agents")
				So(memory.Slug("/tmp/a_b/c.d+"), ShouldEqual, "-tmp-a_b-c.d+")
				So(memory.Slug("rel/file"), ShouldEqual, strings.ReplaceAll(abs, "/", "-"))
			})
		})
	})
}

func TestValidSlug(t *testing.T) {
	Convey("Given a table of slug candidates", t, func() {
		tests := []struct {
			name string
			want bool
		}{
			{name: "-Users-universe-my-beadle", want: true},
			{name: "-Users-universe", want: true},
			{name: "plain", want: true},
			{name: "with space", want: true},
			{name: "-Users-universe-ish_2.0+x", want: true},
			{name: ".", want: false},
			{name: "..", want: false},
			{name: "a/b", want: false},
			{name: "", want: false},
			{name: ".hidden", want: false},
			{name: "/abs", want: false},
			{name: "a/../b", want: false},
		}

		for _, tt := range tests {
			Convey("When validating "+tt.name, func() {
				Convey("Then validity matches", func() {
					So(memory.ValidSlug(tt.name), ShouldEqual, tt.want)
				})
			})
		}
	})
}

func TestValidNote(t *testing.T) {
	Convey("Given a table of note-name candidates", t, func() {
		tests := []struct {
			name string
			want bool
		}{
			{name: "MEMORY.md", want: true},
			{name: "feedback_workflow.md", want: true},
			{name: "project state.md", want: true},
			{name: "a.md", want: true},
			{name: strings.Repeat("long", 60) + ".md", want: true},
			{name: ".md", want: false},
			{name: ".hidden.md", want: false},
			{name: "x.MD", want: false},
			{name: "x.Md", want: false},
			{name: "note", want: false},
			{name: "x.md.bak", want: false},
			{name: "nested/x.md", want: false},
			{name: "", want: false},
			{name: "..", want: false},
			{name: "x.", want: false},
		}

		for _, tt := range tests {
			Convey("When validating "+tt.name, func() {
				Convey("Then validity matches", func() {
					So(memory.ValidNote(tt.name), ShouldEqual, tt.want)
				})
			})
		}
	})
}

func TestReadDirMissingRoot(t *testing.T) {
	Convey("Given a missing memory root", t, func() {
		Convey("When it is read", func() {
			slugs, err := memory.ReadDir(filepath.Join(t.TempDir(), "missing"))

			Convey("Then it is an empty map", func() {
				So(err, ShouldBeNil)
				So(slugs, ShouldBeEmpty)
			})
		})
	})
}

func TestReadDirLayout(t *testing.T) {
	Convey("Given a memory root with notes, junk and symlinks", t, func() {
		root := t.TempDir()

		writeFile(t, filepath.Join(root, "-Users-universe", "MEMORY.md"), "# memory\n")
		writeFile(t, filepath.Join(root, "-Users-universe", "feedback_x.md"), "# feedback\n")

		writeFile(t, filepath.Join(root, "-Users-universe", "session.jsonl"), "{}\n")
		writeFile(t, filepath.Join(root, "-Users-universe", "nested", "deep.md"), "x\n")
		writeFile(t, filepath.Join(root, "-Users-universe", ".hidden.md"), "x\n")
		writeFile(t, filepath.Join(root, "-Users-universe", "upper.MD"), "x\n")
		writeFile(t, filepath.Join(root, "top-level.md"), "x\n")

		So(os.MkdirAll(filepath.Join(root, ".hidden-slug"), 0o750), ShouldBeNil)
		So(os.MkdirAll(filepath.Join(root, "hosts-memory", "memory"), 0o750), ShouldBeNil)

		target := filepath.Join(t.TempDir(), "outside")
		writeFile(t, filepath.Join(target, "note.md"), "x\n")

		So(os.Symlink(filepath.Join(target, "note.md"), filepath.Join(root, "-Users-universe", "link.md")), ShouldBeNil)
		So(os.Symlink(target, filepath.Join(root, "linked-slug")), ShouldBeNil)

		Convey("When the root is read", func() {
			slugs, err := memory.ReadDir(root)

			Convey("Then only valid slugs and notes are returned", func() {
				So(err, ShouldBeNil)
				So(slugs, ShouldResemble, map[string]memory.Tree{
					"-Users-universe": {
						"MEMORY.md":     []byte("# memory\n"),
						"feedback_x.md": []byte("# feedback\n"),
					},
					"hosts-memory": {},
				})
			})
		})
	})
}

func TestReadDirNoteError(t *testing.T) {
	Convey("Given an unreadable note", t, func() {
		root := t.TempDir()

		note := filepath.Join(root, "-slug", "broken.md")
		writeFile(t, note, "x\n")
		So(os.Chmod(note, 0o000), ShouldBeNil)
		t.Cleanup(func() { _ = os.Chmod(note, 0o600) })

		Convey("When the root is read", func() {
			_, err := memory.ReadDir(root)

			Convey("Then the error names the note", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "broken.md")
			})
		})
	})
}

func TestFlattenGroupRoundTrip(t *testing.T) {
	Convey("Given a map of memory trees", t, func() {
		slugs := map[string]memory.Tree{
			"a": {"one.md": []byte("1\n"), "two.md": []byte("2\n")},
			"b": {"MEMORY.md": []byte("m\n")},
		}

		Convey("When flattened and grouped back", func() {
			Convey("Then the map round-trips", func() {
				So(memory.Group(memory.Flatten(slugs)), ShouldResemble, slugs)
			})
		})
	})
}

func TestGroupDropsInvalidKeys(t *testing.T) {
	Convey("Given items with valid and invalid keys", t, func() {
		items := map[string][]byte{
			"good/ok.md":        []byte("x"),
			"../evil/note.md":   []byte("x"),
			".hidden/note.md":   []byte("x"),
			"slug/.hidden.md":   []byte("x"),
			"slug/note.txt":     []byte("x"),
			"slug/nested/x.md":  []byte("x"),
			"slug/":             []byte("x"),
			"noslash":           []byte("x"),
			"":                  []byte("x"),
			"slug/bad.md.bak":   []byte("x"),
			"slug/..":           []byte("x"),
			"deep/slug/note.md": []byte("x"),
		}

		Convey("When they are grouped", func() {
			Convey("Then only valid keys survive", func() {
				So(memory.Group(items), ShouldResemble, map[string]memory.Tree{"good": {"ok.md": []byte("x")}})
			})
		})
	})
}

func TestSyncTreeWrites(t *testing.T) {
	Convey("Given a memory root", t, func() {
		root := filepath.Join(t.TempDir(), "memory")

		So(memory.SyncTree(root, "-slug", memory.Tree{"MEMORY.md": []byte("v1\n")}), ShouldBeNil)

		info, err := os.Stat(filepath.Join(root, "-slug"))
		So(err, ShouldBeNil)
		So(info.Mode().Perm(), ShouldEqual, os.FileMode(0o750))

		note := filepath.Join(root, "-slug", "MEMORY.md")

		info, err = os.Stat(note)
		So(err, ShouldBeNil)
		So(info.Mode().Perm(), ShouldEqual, os.FileMode(0o644))

		Convey("When the tree is synced again", func() {
			So(memory.SyncTree(root, "-slug", memory.Tree{"MEMORY.md": []byte("v2\n")}), ShouldBeNil)

			data, err := os.ReadFile(note) //nolint:gosec // G304: tests read their own temp files

			Convey("Then the note is updated", func() {
				So(err, ShouldBeNil)
				So(string(data), ShouldEqual, "v2\n")
			})
		})
	})
}

func TestReadDirRejectsBadRoots(t *testing.T) {
	Convey("Given a symlinked memory root", t, func() {
		root := filepath.Join(t.TempDir(), "memory")
		So(os.Symlink(t.TempDir(), root), ShouldBeNil)

		Convey("When it is read", func() {
			_, err := memory.ReadDir(root)

			Convey("Then it is rejected", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "symlink")
			})
		})
	})

	Convey("Given a memory root that is a file", t, func() {
		root := filepath.Join(t.TempDir(), "memory")
		So(os.WriteFile(root, []byte("x"), 0o600), ShouldBeNil)

		Convey("When it is read", func() {
			_, err := memory.ReadDir(root)

			Convey("Then it is rejected", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "not a directory")
			})
		})
	})
}

func TestSyncTreeRejectsSymlinks(t *testing.T) {
	Convey("Given a symlinked memory root", t, func() {
		target := t.TempDir()
		root := filepath.Join(t.TempDir(), "memory")

		So(os.Symlink(target, root), ShouldBeNil)

		Convey("When a tree is synced", func() {
			Convey("Then it is rejected", func() {
				So(memory.SyncTree(root, "-slug", memory.Tree{"MEMORY.md": []byte("x")}), ShouldBeError)
			})
		})
	})

	Convey("Given a symlinked slug directory", t, func() {
		target := t.TempDir()
		root := filepath.Join(t.TempDir(), "memory")

		So(os.MkdirAll(root, 0o750), ShouldBeNil)
		So(os.Symlink(target, filepath.Join(root, "-slug")), ShouldBeNil)

		Convey("When a tree is synced", func() {
			Convey("Then it is rejected", func() {
				So(memory.SyncTree(root, "-slug", memory.Tree{"MEMORY.md": []byte("x")}), ShouldBeError)
			})
		})
	})

	Convey("Given a symlinked canon note", t, func() {
		target := t.TempDir()
		root := filepath.Join(t.TempDir(), "memory")

		So(memory.SyncTree(root, "-slug", memory.Tree{"MEMORY.md": []byte("v1")}), ShouldBeNil)
		So(os.Remove(filepath.Join(root, "-slug", "MEMORY.md")), ShouldBeNil)
		So(os.Symlink(filepath.Join(target, "external.md"), filepath.Join(root, "-slug", "MEMORY.md")), ShouldBeNil)

		Convey("When a tree is synced", func() {
			err := memory.SyncTree(root, "-slug", memory.Tree{"MEMORY.md": []byte("v2")})

			Convey("Then it is rejected and the symlink is kept", func() {
				So(err, ShouldBeError)

				info, statErr := os.Lstat(filepath.Join(root, "-slug", "MEMORY.md"))
				So(statErr, ShouldBeNil)
				So(info.Mode()&os.ModeSymlink, ShouldNotBeZeroValue)
			})
		})
	})
}

func TestSyncTreeRejectsInvalidNames(t *testing.T) {
	Convey("Given an invalid slug and note name", t, func() {
		root := filepath.Join(t.TempDir(), "memory")

		Convey("When they are synced", func() {
			Convey("Then invalid names are rejected and an empty tree creates nothing", func() {
				So(memory.SyncTree(root, ".hidden", memory.Tree{"MEMORY.md": []byte("x")}), ShouldBeError)
				So(memory.SyncTree(root, "-slug", memory.Tree{"note.txt": []byte("x")}), ShouldBeError)
				So(memory.SyncTree(root, "-slug", memory.Tree{}), ShouldBeNil)

				_, err := os.Stat(filepath.Join(root, "-slug"))
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}
