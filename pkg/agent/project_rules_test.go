package agent_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/digest"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/memory"
)

type fencedWriter interface {
	WriteFenced(context.Context, []byte) error
}

func projectSurface(t *testing.T, home, cwd string) agent.Surface {
	t.Helper()

	return surfaceOf(t, agent.OpenCode(home, cwd), kind.Projects)
}

func projectKey(cwd string) string {
	return memory.Slug(cwd) + "/AGENTS.md"
}

func blockFor(t *testing.T, slug string) []byte {
	t.Helper()

	notes := map[string][]byte{slug + "/MEMORY.md": []byte("---\ndescription: hook\n---\nbody\n")}
	block, _ := digest.Render("/home/u/.claude/projects/"+slug+"/memory", notes, digest.DefaultBudget)

	return block
}

func TestProjectSurfaceInactive(t *testing.T) {
	Convey("Given a project surface in an inactive (non-git, no file) directory", t, func() {
		home := t.TempDir()
		cwd := t.TempDir()
		surface := projectSurface(t, home, cwd)

		Convey("When it is read and written", func() {
			snap, err := surface.Read(t.Context())
			So(err, ShouldBeNil)

			So(surface.Write(t.Context(), kind.Items{projectKey(cwd): []byte("body\n")}), ShouldBeNil)

			Convey("Then nothing is present or written", func() {
				So(snap.Present, ShouldBeFalse)
				So(snap.Items, ShouldBeEmpty)

				_, statErr := os.Stat(filepath.Join(cwd, "AGENTS.md"))
				So(errors.Is(statErr, digest.ErrFence), ShouldBeFalse)
				So(statErr, ShouldNotBeNil)

				projector, ok := surface.(agent.Projector)
				So(ok, ShouldBeTrue)

				_, _, visible := projector.Project(projectKey(cwd), []byte("body\n"))
				So(visible, ShouldBeFalse)
			})
		})
	})
}

func TestProjectSurfaceActiveWithoutFile(t *testing.T) {
	Convey("Given an active project without an AGENTS.md", t, func() {
		home := t.TempDir()
		cwd := t.TempDir()
		So(os.MkdirAll(filepath.Join(cwd, ".git"), 0o750), ShouldBeNil)

		surface := projectSurface(t, home, cwd)

		Convey("When it is read and written", func() {
			snap, err := surface.Read(t.Context())
			So(err, ShouldBeNil)

			So(surface.Write(t.Context(), kind.Items{projectKey(cwd): []byte("# rules\n")}), ShouldBeNil)

			Convey("Then it becomes present and the body is written", func() {
				So(snap.Present, ShouldBeTrue)
				So(snap.Items, ShouldBeEmpty)
				So(readFile(t, filepath.Join(cwd, "AGENTS.md")), ShouldEqual, "# rules\n")
			})
		})
	})
}

func TestProjectSurfaceReadIsFenceBlind(t *testing.T) {
	Convey("Given an active project with a fenced AGENTS.md", t, func() {
		home := t.TempDir()
		cwd := t.TempDir()
		So(os.MkdirAll(filepath.Join(cwd, ".git"), 0o750), ShouldBeNil)

		surface := projectSurface(t, home, cwd)
		block := blockFor(t, memory.Slug(cwd))

		writeFile(t, filepath.Join(cwd, "AGENTS.md"), string(block)+"# user rules\n\nText.\n")

		Convey("When the file carries a body", func() {
			snap, err := surface.Read(t.Context())

			Convey("Then the fence never reaches items", func() {
				So(err, ShouldBeNil)
				So(snap.Present, ShouldBeTrue)
				So(string(snap.Items[projectKey(cwd)]), ShouldEqual, "# user rules\n\nText.\n")

				Convey("And a fence-only file has no item", func() {
					writeFile(t, filepath.Join(cwd, "AGENTS.md"), string(block))

					snap, err := surface.Read(t.Context())
					So(err, ShouldBeNil)
					So(snap.Items, ShouldBeEmpty)
				})

				Convey("And a broken fence is an error", func() {
					writeFile(t, filepath.Join(cwd, "AGENTS.md"), string(block)+"x\n"+digest.BeginPrefix+" broken\n"+digest.EndMarker+"\n")

					_, err := surface.Read(t.Context())
					So(errors.Is(err, digest.ErrFence), ShouldBeTrue)
				})
			})
		})
	})
}

func TestProjectSurfaceWriteKeepsFence(t *testing.T) {
	Convey("Given an active project file with a fence and a body", t, func() {
		home := t.TempDir()
		cwd := t.TempDir()
		So(os.MkdirAll(filepath.Join(cwd, ".git"), 0o750), ShouldBeNil)

		surface := projectSurface(t, home, cwd)
		block := blockFor(t, memory.Slug(cwd))
		path := filepath.Join(cwd, "AGENTS.md")

		writeFile(t, path, string(block)+"# v1\n")
		So(os.Chmod(path, 0o600), ShouldBeNil)

		Convey("When the body is written", func() {
			So(surface.Write(t.Context(), kind.Items{projectKey(cwd): []byte("# v2\n")}), ShouldBeNil)

			body, fence, found, err := digest.Strip([]byte(readFile(t, path)))
			So(err, ShouldBeNil)

			info, err := os.Stat(path)

			Convey("Then the fence and mode are preserved", func() {
				So(found, ShouldBeTrue)
				So(string(body), ShouldEqual, "# v2\n")
				So(string(fence), ShouldEqual, string(block))
				So(err, ShouldBeNil)
				So(info.Mode().Perm(), ShouldEqual, os.FileMode(0o600))
			})
		})
	})
}

func TestProjectSurfaceWriteFenced(t *testing.T) {
	Convey("Given an active project and a fenced writer", t, func() {
		home := t.TempDir()
		cwd := t.TempDir()
		So(os.MkdirAll(filepath.Join(cwd, ".git"), 0o750), ShouldBeNil)

		surface := projectSurface(t, home, cwd)
		writer, ok := surface.(fencedWriter)
		So(ok, ShouldBeTrue)

		path := filepath.Join(cwd, "AGENTS.md")
		block := blockFor(t, memory.Slug(cwd))

		Convey("When a fence is written into an empty file", func() {
			So(writer.WriteFenced(t.Context(), block), ShouldBeNil)

			body, fence, found, err := digest.Strip([]byte(readFile(t, path)))
			So(err, ShouldBeNil)

			Convey("Then only the fence is present", func() {
				So(found, ShouldBeTrue)
				So(body, ShouldBeEmpty)
				So(string(fence), ShouldEqual, string(block))

				Convey("And rewriting the fence keeps the local body", func() {
					writeFile(t, path, string(block)+"# local body\n")

					fresh := blockFor(t, memory.Slug(cwd))
					So(writer.WriteFenced(t.Context(), fresh), ShouldBeNil)

					body, fence, found, err = digest.Strip([]byte(readFile(t, path)))
					So(err, ShouldBeNil)
					So(found, ShouldBeTrue)
					So(string(body), ShouldEqual, "# local body\n")
					So(string(fence), ShouldEqual, string(fresh))

					Convey("And a nil fence removes it and leaves the body", func() {
						So(writer.WriteFenced(t.Context(), nil), ShouldBeNil)
						So(readFile(t, path), ShouldEqual, "# local body\n")
					})
				})
			})
		})
	})
}

func TestProjectSurfaceNormalizesUnterminatedFence(t *testing.T) {
	Convey("Given a file whose fence has no trailing newline", t, func() {
		home := t.TempDir()
		cwd := t.TempDir()
		So(os.MkdirAll(filepath.Join(cwd, ".git"), 0o750), ShouldBeNil)

		surface := projectSurface(t, home, cwd)
		path := filepath.Join(cwd, "AGENTS.md")
		block := blockFor(t, memory.Slug(cwd))

		writeFile(t, path, string(bytes.TrimSuffix(block, []byte("\n"))))

		Convey("When the body is written", func() {
			So(surface.Write(t.Context(), kind.Items{projectKey(cwd): []byte("\n# body\n")}), ShouldBeNil)

			body, fence, found, err := digest.Strip([]byte(readFile(t, path)))
			So(err, ShouldBeNil)

			Convey("Then the fence is terminated and the body survives", func() {
				So(found, ShouldBeTrue)
				So(string(body), ShouldEqual, "\n# body\n")
				So(strings.HasSuffix(string(fence), "\n"), ShouldBeTrue)
			})
		})
	})
}

func TestProjectSurfaceWriteDeletesCanonicalFile(t *testing.T) {
	Convey("Given an active project file", t, func() {
		home := t.TempDir()
		cwd := t.TempDir()
		So(os.MkdirAll(filepath.Join(cwd, ".git"), 0o750), ShouldBeNil)

		surface := projectSurface(t, home, cwd)
		path := filepath.Join(cwd, "AGENTS.md")
		block := blockFor(t, memory.Slug(cwd))

		Convey("When it carried canonical content and desired is empty", func() {
			writeFile(t, path, string(block)+"# body\n")
			So(surface.Write(t.Context(), kind.Items{}), ShouldBeNil)

			Convey("Then it is removed", func() {
				_, err := os.Stat(path)
				So(errors.Is(err, os.ErrNotExist), ShouldBeTrue)
			})
		})

		Convey("When it was fence-only and desired is empty", func() {
			writeFile(t, path, string(block))
			So(surface.Write(t.Context(), kind.Items{}), ShouldBeNil)

			Convey("Then it is kept", func() {
				_, err := os.Stat(path)
				So(err, ShouldBeNil)
			})
		})

		Convey("When it is a symlink and desired is empty", func() {
			writeFile(t, path, "# body\n")

			link := "sibling.md"
			writeFile(t, filepath.Join(cwd, link), "# else\n")
			So(os.Remove(path), ShouldBeNil)
			So(os.Symlink(filepath.Join(cwd, link), path), ShouldBeNil)

			So(surface.Write(t.Context(), kind.Items{}), ShouldBeNil)

			Convey("Then the symlink is not deleted", func() {
				_, err := os.Stat(filepath.Join(cwd, link))
				So(err, ShouldBeNil)
			})
		})
	})
}

func TestProjectSurfaceProject(t *testing.T) {
	Convey("Given an active project surface", t, func() {
		home := t.TempDir()
		cwd := t.TempDir()
		So(os.MkdirAll(filepath.Join(cwd, ".git"), 0o750), ShouldBeNil)

		surface := projectSurface(t, home, cwd)
		projector, ok := surface.(agent.Projector)
		So(ok, ShouldBeTrue)

		Convey("When its own key is projected", func() {
			key, value, visible := projector.Project(projectKey(cwd), []byte("# rules\n"))

			Convey("Then it is visible unchanged", func() {
				So(visible, ShouldBeTrue)
				So(key, ShouldEqual, projectKey(cwd))
				So(string(value), ShouldEqual, "# rules\n")

				Convey("And another slug or file is hidden", func() {
					_, _, visible := projector.Project(memory.Slug(t.TempDir())+"/AGENTS.md", []byte("x"))
					So(visible, ShouldBeFalse)

					_, _, visible = projector.Project(memory.Slug(cwd)+"/GEMINI.md", []byte("x"))
					So(visible, ShouldBeFalse)
				})
			})
		})
	})
}
