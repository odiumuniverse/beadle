package agent_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func memorySurfaceOf(t *testing.T, home string) agent.Surface {
	t.Helper()

	return surfaceOf(t, agent.ClaudeCode(home, t.TempDir()), kind.Memory)
}

func projectsDir(home string) string {
	return filepath.Join(home, ".claude", "projects")
}

func memoryDir(home, slug string) string {
	return filepath.Join(projectsDir(home), slug, "memory")
}

func TestMemorySurfaceRead(t *testing.T) {
	Convey("Given a memory tree with notes, jsonl and nested junk", t, func() {
		home := t.TempDir()

		writeFile(t, filepath.Join(memoryDir(home, "-Users-a"), "MEMORY.md"), "# a\n")
		writeFile(t, filepath.Join(memoryDir(home, "-Users-a"), "feedback_x.md"), "# x\n")
		writeFile(t, filepath.Join(projectsDir(home), "-Users-a", "session.jsonl"), "{}\n")
		writeFile(t, filepath.Join(projectsDir(home), "-Users-a", "abc", "tool-results", "f.jsonl"), "x\n")
		writeFile(t, filepath.Join(memoryDir(home, "-Users-b"), "MEMORY.md"), "# b\n")
		writeFile(t, filepath.Join(memoryDir(home, "-Users-b"), "notes.jsonl"), "{}\n")

		Convey("When the surface reads", func() {
			snap, err := memorySurfaceOf(t, home).Read(t.Context())

			Convey("Then only flat .md notes are items", func() {
				So(err, ShouldBeNil)
				So(snap.Present, ShouldBeTrue)
				So(snap.Items, ShouldResemble, kind.Items{
					"-Users-a/MEMORY.md":     []byte("# a\n"),
					"-Users-a/feedback_x.md": []byte("# x\n"),
					"-Users-b/MEMORY.md":     []byte("# b\n"),
				})
			})
		})
	})
}

func TestMemorySurfaceReadMissingProjects(t *testing.T) {
	Convey("Given a home without a projects directory", t, func() {
		Convey("When the surface reads", func() {
			snap, err := memorySurfaceOf(t, t.TempDir()).Read(t.Context())

			Convey("Then it is absent and empty", func() {
				So(err, ShouldBeNil)
				So(snap.Present, ShouldBeFalse)
				So(snap.Items, ShouldBeEmpty)
			})
		})
	})
}

func TestMemorySurfaceReadSkipsSymlinks(t *testing.T) {
	Convey("Given symlinked notes, slugs and memory directories", t, func() {
		home := t.TempDir()
		target := t.TempDir()

		writeFile(t, filepath.Join(target, "note.md"), "# target\n")
		writeFile(t, filepath.Join(target, "MEMORY.md"), "# real\n")

		writeFile(t, filepath.Join(memoryDir(home, "-Users-a"), "MEMORY.md"), "# a\n")
		So(os.Symlink(target, filepath.Join(memoryDir(home, "-Users-a"), "linked.md")), ShouldBeNil)
		So(os.Symlink(target, filepath.Join(projectsDir(home), "linked-slug")), ShouldBeNil)
		So(os.MkdirAll(filepath.Join(projectsDir(home), "-Users-b"), 0o750), ShouldBeNil)
		So(os.Symlink(target, memoryDir(home, "-Users-b")), ShouldBeNil)

		Convey("When the surface reads", func() {
			snap, err := memorySurfaceOf(t, home).Read(t.Context())

			Convey("Then only the real note is returned", func() {
				So(err, ShouldBeNil)
				So(snap.Items, ShouldResemble, kind.Items{"-Users-a/MEMORY.md": []byte("# a\n")})
			})
		})
	})
}

func TestMemorySurfaceReadSymlinkedProjects(t *testing.T) {
	Convey("Given a symlinked projects directory", t, func() {
		home := t.TempDir()
		target := t.TempDir()

		writeFile(t, filepath.Join(target, "-Users-a", "memory", "MEMORY.md"), "# a\n")
		So(os.MkdirAll(filepath.Join(home, ".claude"), 0o750), ShouldBeNil)
		So(os.Symlink(target, projectsDir(home)), ShouldBeNil)

		surface := memorySurfaceOf(t, home)

		Convey("When the surface reads", func() {
			_, err := surface.Read(t.Context())

			Convey("Then it errors and has no watch paths", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "symlink")
				So(surface.WatchPaths(), ShouldBeNil)
			})
		})

		Convey("When projects is a plain file", func() {
			So(os.Remove(projectsDir(home)), ShouldBeNil)
			So(os.WriteFile(projectsDir(home), []byte("x"), 0o600), ShouldBeNil)

			_, err := surface.Read(t.Context())

			Convey("Then it errors and has no watch paths", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "not a directory")
				So(surface.WatchPaths(), ShouldBeNil)
			})
		})
	})
}

func TestMemorySurfaceReadError(t *testing.T) {
	Convey("Given an unreadable note", t, func() {
		home := t.TempDir()

		note := filepath.Join(memoryDir(home, "-Users-a"), "broken.md")
		writeFile(t, note, "x\n")
		So(os.Chmod(note, 0o000), ShouldBeNil)
		t.Cleanup(func() { _ = os.Chmod(note, 0o600) })

		Convey("When the surface reads", func() {
			_, err := memorySurfaceOf(t, home).Read(t.Context())

			Convey("Then it fails", func() {
				So(err, ShouldBeError)
			})
		})
	})
}

func TestMemorySurfaceWrite(t *testing.T) {
	Convey("Given a memory directory with notes and a session file", t, func() {
		home := t.TempDir()

		writeFile(t, filepath.Join(memoryDir(home, "-Users-a"), "a.md"), "v1\n")
		writeFile(t, filepath.Join(memoryDir(home, "-Users-a"), "b.md"), "stale\n")
		writeFile(t, filepath.Join(projectsDir(home), "-Users-a", "session.jsonl"), "{}\n")

		surface := memorySurfaceOf(t, home)

		Convey("When items are written", func() {
			err := surface.Write(t.Context(), kind.Items{
				"-Users-a/a.md": []byte("v2\n"),
				"-Users-a/c.md": []byte("new\n"),
				"-Users-z/n.md": []byte("nope\n"),
			})

			Convey("Then updates land, stale notes are gone, jsonl survives and no slug is created", func() {
				So(err, ShouldBeNil)
				So(readFile(t, filepath.Join(memoryDir(home, "-Users-a"), "a.md")), ShouldEqual, "v2\n")
				So(readFile(t, filepath.Join(memoryDir(home, "-Users-a"), "c.md")), ShouldEqual, "new\n")

				_, staleErr := os.Stat(filepath.Join(memoryDir(home, "-Users-a"), "b.md"))
				So(staleErr, ShouldNotBeNil)

				So(readFile(t, filepath.Join(projectsDir(home), "-Users-a", "session.jsonl")), ShouldEqual, "{}\n")

				_, slugErr := os.Stat(filepath.Join(projectsDir(home), "-Users-z"))
				So(slugErr, ShouldNotBeNil)

				_, dirErr := os.Stat(memoryDir(home, "-Users-a"))
				So(dirErr, ShouldBeNil)

				Convey("And clearing items keeps the memory directory", func() {
					So(surface.Write(t.Context(), kind.Items{}), ShouldBeNil)

					_, noteErr := os.Stat(filepath.Join(memoryDir(home, "-Users-a"), "a.md"))
					So(noteErr, ShouldNotBeNil)

					_, dirErr := os.Stat(memoryDir(home, "-Users-a"))
					So(dirErr, ShouldBeNil)

					So(readFile(t, filepath.Join(projectsDir(home), "-Users-a", "session.jsonl")), ShouldEqual, "{}\n")
				})
			})
		})
	})
}

func TestMemorySurfaceWriteCreatesMemoryDir(t *testing.T) {
	Convey("Given a slug directory without a memory subdirectory", t, func() {
		home := t.TempDir()
		So(os.MkdirAll(filepath.Join(projectsDir(home), "-Users-a"), 0o750), ShouldBeNil)

		Convey("When a note is written", func() {
			So(memorySurfaceOf(t, home).Write(t.Context(), kind.Items{"-Users-a/n.md": []byte("x\n")}), ShouldBeNil)

			info, err := os.Stat(memoryDir(home, "-Users-a"))

			Convey("Then memory is created with 0750", func() {
				So(err, ShouldBeNil)
				So(info.Mode().Perm(), ShouldEqual, os.FileMode(0o750))
				So(readFile(t, filepath.Join(memoryDir(home, "-Users-a"), "n.md")), ShouldEqual, "x\n")
			})
		})
	})
}

func TestMemorySurfaceWriteSkipsSymlinks(t *testing.T) {
	Convey("Given a symlinked memory dir and a symlinked note", t, func() {
		home := t.TempDir()
		target := t.TempDir()

		writeFile(t, filepath.Join(target, "keep.md"), "keep\n")

		So(os.MkdirAll(filepath.Join(projectsDir(home), "-Users-a"), 0o750), ShouldBeNil)
		So(os.Symlink(target, memoryDir(home, "-Users-a")), ShouldBeNil)

		surface := memorySurfaceOf(t, home)

		Convey("When writing through the symlinked dir", func() {
			So(surface.Write(t.Context(), kind.Items{"-Users-a/new.md": []byte("x\n")}), ShouldBeNil)

			Convey("Then the target is untouched", func() {
				_, newErr := os.Stat(filepath.Join(target, "new.md"))
				So(newErr, ShouldNotBeNil)
				So(readFile(t, filepath.Join(target, "keep.md")), ShouldEqual, "keep\n")
			})
		})

		Convey("When writing through a symlinked note", func() {
			writeFile(t, filepath.Join(memoryDir(home, "-Users-b"), "real.md"), "real\n")
			link := filepath.Join(memoryDir(home, "-Users-b"), "linked.md")
			So(os.Symlink(filepath.Join(target, "keep.md"), link), ShouldBeNil)

			So(surface.Write(t.Context(), kind.Items{"-Users-b/linked.md": []byte("changed\n")}), ShouldBeNil)

			info, err := os.Lstat(link)

			Convey("Then the symlink is not written through", func() {
				So(err, ShouldBeNil)
				So(info.Mode()&os.ModeSymlink, ShouldNotBeZeroValue)
				So(readFile(t, filepath.Join(target, "keep.md")), ShouldEqual, "keep\n")
			})
		})
	})
}

func TestMemorySurfaceWriteKeepsEqualNotes(t *testing.T) {
	Convey("Given a note equal to the desired content", t, func() {
		home := t.TempDir()

		path := filepath.Join(memoryDir(home, "-Users-a"), "same.md")
		writeFile(t, path, "same\n")

		past := time.Now().Add(-time.Hour)
		So(os.Chtimes(path, past, past), ShouldBeNil)

		surface := memorySurfaceOf(t, home)

		Convey("When the same content is written", func() {
			So(surface.Write(t.Context(), kind.Items{"-Users-a/same.md": []byte("same\n")}), ShouldBeNil)

			info, err := os.Stat(path)

			Convey("Then it is not rewritten", func() {
				So(err, ShouldBeNil)
				So(info.ModTime().Equal(past), ShouldBeTrue)
			})
		})
	})
}

func TestMemorySurfaceProject(t *testing.T) {
	Convey("Given a memory surface with a real, a missing and a symlinked slug", t, func() {
		home := t.TempDir()

		writeFile(t, filepath.Join(memoryDir(home, "-Users-a"), "MEMORY.md"), "# a\n")

		So(os.MkdirAll(filepath.Join(projectsDir(home), "-Users-b"), 0o750), ShouldBeNil)
		So(os.Symlink(t.TempDir(), filepath.Join(projectsDir(home), "-Users-c")), ShouldBeNil)

		surface := memorySurfaceOf(t, home)
		projector, ok := surface.(agent.Projector)
		So(ok, ShouldBeTrue)

		Convey("When keys are projected", func() {
			key, value, visible := projector.Project("-Users-a/MEMORY.md", []byte("# a\n"))
			So(visible, ShouldBeTrue)
			So(key, ShouldEqual, "-Users-a/MEMORY.md")
			So(string(value), ShouldEqual, "# a\n")

			_, _, missing := projector.Project("-Users-missing/MEMORY.md", []byte("# m\n"))
			_, _, linked := projector.Project("-Users-c/MEMORY.md", []byte("# c\n"))
			_, _, malformed := projector.Project("malformed", []byte("x"))

			Convey("Then only the real slug is visible", func() {
				So(missing, ShouldBeFalse)
				So(linked, ShouldBeFalse)
				So(malformed, ShouldBeFalse)
			})
		})
	})
}
