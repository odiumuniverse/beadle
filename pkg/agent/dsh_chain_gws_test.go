package agent_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
	proj "github.com/odiumuniverse/beadle/pkg/project"
)

// dshRepo builds a git checkout fixture and returns its root.
func dshRepo(t *testing.T) string {
	t.Helper()

	root := t.TempDir()

	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o750); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	return root
}

// dshChainRels lists the root-relative rels of the chain surfaces.
func dshChainRels(surfaces []agent.Surface) []string {
	rels := make([]string, 0, len(surfaces))

	for _, surface := range surfaces {
		file, ok := surface.(agent.ProjectFile)
		if !ok {
			continue
		}

		rels = append(rels, file.ProjectRel())
	}

	return rels
}

// dshChainItems reads every chain surface and returns the keys it renders.
func dshChainItems(t *testing.T, surfaces []agent.Surface) []string {
	t.Helper()

	var keys []string

	for _, surface := range surfaces {
		snap, err := surface.Read(t.Context())
		if err != nil {
			t.Fatalf("read %s: %v", surface.Path(), err)
		}

		for key := range snap.Items {
			keys = append(keys, key)
		}
	}

	return keys
}

func TestDSHChainSurfaces(t *testing.T) {
	Convey("Given a checkout with a nested instruction chain", t, func() {
		root := dshRepo(t)

		writeFile(t, filepath.Join(root, "AGENTS.md"), "# root agents\n")
		writeFile(t, filepath.Join(root, "CLAUDE.md"), "# root agents\n") // identical sibling
		writeFile(t, filepath.Join(root, "sub", "CLAUDE.md"), "# sub claude\n")

		cwd := filepath.Join(root, "sub")
		id := proj.Resolve(cwd).ID

		adapter := agent.DSH(t.TempDir(), cwd)

		Convey("When the adapter builds the chain", func() {
			surfaces := adapter.SurfacesOf(kind.Projects)

			Convey("Then every candidate is a root-relative surface", func() {
				So(surfaces, ShouldHaveLength, 4)

				So(dshChainRels(surfaces), ShouldResemble,
					[]string{"AGENTS.md", "CLAUDE.md", "sub/AGENTS.md", "sub/CLAUDE.md"})

				for _, surface := range surfaces {
					_, isRoot := surface.(agent.ProjectRootRel)
					So(isRoot, ShouldBeTrue)
					So(surface.Traits().DefaultMode, ShouldEqual, config.ModePull)
				}
			})

			Convey("Then every non-empty candidate renders under its own key", func() {
				So(dshChainItems(t, surfaces), ShouldResemble,
					[]string{id + "/AGENTS.md", id + "/CLAUDE.md", id + "/sub/CLAUDE.md"})
			})
		})
	})

	Convey("Given a cwd outside a checkout", t, func() {
		cwd := t.TempDir()

		Convey("Then there is no chain", func() {
			So(agent.DSH(t.TempDir(), cwd).SurfacesOf(kind.Projects), ShouldBeEmpty)
		})
	})

	Convey("Given cwd at the checkout root", t, func() {
		root := dshRepo(t)

		writeFile(t, filepath.Join(root, "AGENTS.md"), "# agents\n")
		writeFile(t, filepath.Join(root, "CLAUDE.md"), "# claude\n")

		Convey("Then both candidates keep root-relative rels", func() {
			surfaces := agent.DSH(t.TempDir(), root).SurfacesOf(kind.Projects)
			So(dshChainRels(surfaces), ShouldResemble, []string{"AGENTS.md", "CLAUDE.md"})
		})
	})

	Convey("Given identical contents at two chain levels", t, func() {
		root := dshRepo(t)

		writeFile(t, filepath.Join(root, "AGENTS.md"), "# shared\n")
		writeFile(t, filepath.Join(root, "sub", "AGENTS.md"), "# shared\n")

		Convey("Then each file renders under its own key", func() {
			cwd := filepath.Join(root, "sub")
			surfaces := agent.DSH(t.TempDir(), cwd).SurfacesOf(kind.Projects)
			id := proj.Resolve(cwd).ID

			So(dshChainItems(t, surfaces), ShouldResemble,
				[]string{id + "/AGENTS.md", id + "/sub/AGENTS.md"})
		})
	})

	Convey("Given an empty instruction file in the chain", t, func() {
		root := dshRepo(t)

		writeFile(t, filepath.Join(root, "AGENTS.md"), "\n")

		Convey("Then it renders nothing", func() {
			surfaces := agent.DSH(t.TempDir(), root).SurfacesOf(kind.Projects)
			So(surfaces, ShouldHaveLength, 2)
			So(dshChainItems(t, surfaces), ShouldBeEmpty)
		})
	})
}
