package agent_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
	proj "github.com/odiumuniverse/beadle/pkg/project"
)

func projectMCPSurfaceOf(t *testing.T, cwd string) (agent.Surface, string) {
	t.Helper()

	surface := agent.ClaudeCode(t.TempDir(), cwd).Surface(kind.Projects)
	if surface == nil {
		t.Fatal("no projects surface")
	}

	file, ok := surface.(agent.ProjectFile)
	if !ok {
		t.Fatal("projects surface is not a ProjectFile")
	}

	return surface, proj.Resolve(cwd).ID + "/" + file.ProjectRel()
}

func TestProjectMCPSurfaceWriteRefusesLinks(t *testing.T) {
	Convey("Given a symlinked project .mcp.json", t, func() {
		home := t.TempDir()
		cwd := t.TempDir()

		surface, key := projectMCPSurfaceOf(t, cwd)

		target := filepath.Join(home, "target.json")
		writeFile(t, target, `{"orig": true}`)

		So(os.Symlink(target, filepath.Join(cwd, ".mcp.json")), ShouldBeNil)

		Convey("When the surface writes", func() {
			err := surface.Write(t.Context(), kind.Items{key: []byte(`{"mcpServers": {"evil": {}}}`)})

			Convey("Then it refuses and never writes through the link", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "symlink")
				So(readFile(t, target), ShouldEqualJSON, `{"orig": true}`)

				info, statErr := os.Lstat(filepath.Join(cwd, ".mcp.json"))
				So(statErr, ShouldBeNil)
				So(info.Mode()&os.ModeSymlink, ShouldNotEqual, os.FileMode(0))
			})
		})

		Convey("When the file is replaced by a hard link", func() {
			So(os.Remove(filepath.Join(cwd, ".mcp.json")), ShouldBeNil)

			other := filepath.Join(cwd, "shared.json")
			writeFile(t, other, `{"mcpServers": {}}`)
			So(os.Link(other, filepath.Join(cwd, ".mcp.json")), ShouldBeNil)

			err := surface.Write(t.Context(), kind.Items{key: []byte(`{"mcpServers": {"evil": {}}}`)})

			Convey("Then it refuses and leaves the sibling untouched", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "hard links")
				So(readFile(t, other), ShouldEqualJSON, `{"mcpServers": {}}`)
			})
		})
	})
}

func TestProjectRulesSurfaceRefusesSymlinkRead(t *testing.T) {
	Convey("Given a symlinked project rules file", t, func() {
		home := t.TempDir()
		cwd := t.TempDir()

		secret := filepath.Join(home, "id_rsa")
		writeFile(t, secret, "PRIVATE KEY MATERIAL\n")
		So(os.Symlink(secret, filepath.Join(cwd, "AGENTS.md")), ShouldBeNil)

		a := agent.OpenCode(home, cwd)
		surface := a.Surface(kind.Projects)
		So(surface, ShouldNotBeNil)

		Convey("When the surface reads", func() {
			_, err := surface.Read(t.Context())

			Convey("Then it refuses the symlink", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "symlink")
			})
		})
	})
}
