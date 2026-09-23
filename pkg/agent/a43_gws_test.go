package agent_test

import (
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func TestClaudeProjectRulesSurface(t *testing.T) {
	Convey("Given a Claude Code adapter", t, func() {
		cwd := t.TempDir()

		adapter := agent.ClaudeCode(t.TempDir(), cwd)

		Convey("Then the native project AGENTS.md is a write surface", func() {
			var found agent.Surface

			for _, surface := range adapter.SurfacesOf(kind.Projects) {
				file, ok := surface.(agent.ProjectFile)
				if ok && file.ProjectRel() == "AGENTS.md" {
					found = surface
				}
			}

			So(found, ShouldNotBeNil)
			So(found.Path(), ShouldEqual, filepath.Join(cwd, "AGENTS.md"))

			traits := found.Traits()
			So(traits.DefaultMode, ShouldEqual, config.ModeSync)
			So(traits.Creatable, ShouldBeTrue)
		})
	})
}
