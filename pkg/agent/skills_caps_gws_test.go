package agent_test

import (
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func TestSkillsSurfaceCaps(t *testing.T) {
	Convey("Given the claude skills surface", t, func() {
		surface := agent.ClaudeCode("/home/u", "/tmp").Surface(kind.Skills)

		declared, ok := surface.(agent.SkillCapsSurface)
		So(ok, ShouldBeTrue)

		area, isArea := surface.(agent.SkillReadArea)
		So(isArea, ShouldBeTrue)

		caps := declared.SkillCaps()

		Convey("Then the bundle skills are namespaced and nothing is shadowed", func() {
			So(caps.NamespacedBundle, ShouldBeTrue)
			So(caps.Shadowing, ShouldBeFalse)
			So(caps.ReadOrder, ShouldBeEmpty)
			So(area.ReadDirs(), ShouldResemble, []string{filepath.Join("/home/u", ".claude", "skills")})
		})
	})

	Convey("Given the opencode skills surface", t, func() {
		surface := agent.OpenCode("/home/u", "/tmp").Surface(kind.Skills)

		declared, ok := surface.(agent.SkillCapsSurface)
		So(ok, ShouldBeTrue)

		caps := declared.SkillCaps()

		Convey("Then the verified read order is declared with shadowing on", func() {
			So(caps.Shadowing, ShouldBeTrue)
			So(caps.ReadOrder, ShouldResemble, []string{
				filepath.Join("/home/u", ".config", "opencode", "skills"),
				filepath.Join("/home/u", ".agents", "skills"),
				filepath.Join("/home/u", ".claude", "skills"),
			})
			So(caps.NamespacedBundle, ShouldBeFalse)
		})
	})

	Convey("Given the omp skills surface", t, func() {
		t.Setenv("PI_CONFIG_DIR", "")
		t.Setenv("PI_CODING_AGENT_DIR", "")
		t.Setenv("OMP_PROFILE", "")

		surface := agent.Omp("/home/u", "/tmp").Surface(kind.Skills)

		declared, ok := surface.(agent.SkillCapsSurface)
		So(ok, ShouldBeTrue)

		area, isArea := surface.(agent.SkillReadArea)
		So(isArea, ShouldBeTrue)

		caps := declared.SkillCaps()

		Convey("Then the native dir shadows the shared agents home", func() {
			So(caps.Shadowing, ShouldBeTrue)
			So(caps.ReadOrder, ShouldResemble, []string{
				filepath.Join("/home/u", ".omp", "agent", "skills"),
				filepath.Join("/home/u", ".agents", "skills"),
			})
			So(caps.NamespacedBundle, ShouldBeFalse)
			So(area.ReadDirs(), ShouldResemble, caps.ReadOrder)
			So(surface.Traits().DefaultMode, ShouldEqual, config.ModePull)
		})
	})

	Convey("Given the cursor skills surface", t, func() {
		surface := agent.Cursor("/home/u", "/tmp").Surface(kind.Skills)

		declared, ok := surface.(agent.SkillCapsSurface)
		So(ok, ShouldBeTrue)

		caps := declared.SkillCaps()

		Convey("Then the undocumented precedence stays fail-open", func() {
			So(caps.Shadowing, ShouldBeFalse)
		})
	})
}
