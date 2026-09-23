package agent_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func TestKiloDualReadPaths(t *testing.T) {
	Convey("Given a Kilo home", t, func() {
		home := t.TempDir()
		configDir := filepath.Join(home, ".config", "kilo")

		Convey("Then the canonical skills directory is the host-code plural path", func() {
			So(agent.KiloSkillsDir(home), ShouldEqual, filepath.Join(configDir, "skills"))
			So(agent.KiloSkillReadDirs(home), ShouldResemble, []string{
				filepath.Join(configDir, "skills"),
				filepath.Join(configDir, "skill"),
				filepath.Join(home, ".kilo", "skills"),
				filepath.Join(home, ".kilo", "skill"),
			})
		})

		Convey("Then the skills surface dual-reads and writes the canonical path", func() {
			surface := agent.Kilo(home, t.TempDir()).Surface(kind.Skills)
			So(surface, ShouldNotBeNil)
			So(surface.Path(), ShouldEqual, filepath.Join(configDir, "skills"))

			area, ok := surface.(agent.SkillReadArea)
			So(ok, ShouldBeTrue)

			readOrder := append(agent.KiloSkillReadDirs(home), filepath.Join(home, ".agents", "skills"))
			So(area.ReadDirs(), ShouldResemble, readOrder)

			caps, ok := surface.(agent.SkillCapsSurface)
			So(ok, ShouldBeTrue)
			So(caps.SkillCaps().Shadowing, ShouldBeTrue)
			So(caps.SkillCaps().ReadOrder, ShouldResemble, readOrder)
		})

		Convey("Then the agent is detected through the docs directory alone", func() {
			legacy := t.TempDir()
			So(os.MkdirAll(filepath.Join(legacy, ".kilo", "skills"), 0o750), ShouldBeNil)

			detected, err := agent.Kilo(legacy, t.TempDir()).Detect()
			So(err, ShouldBeNil)
			So(detected, ShouldBeTrue)
		})

		Convey("Then the future agent and command dirs keep singular and plural", func() {
			So(agent.KiloAgentDirs(home), ShouldResemble, []string{
				filepath.Join(configDir, "agents"),
				filepath.Join(configDir, "agent"),
			})
			So(agent.KiloCommandDirs(home), ShouldResemble, []string{
				filepath.Join(configDir, "commands"),
				filepath.Join(configDir, "command"),
			})
		})
	})
}

func TestPiMCPAdapterDetection(t *testing.T) {
	Convey("Given a Pi home without the adapter", t, func() {
		home := t.TempDir()
		piDir := filepath.Join(home, ".pi", "agent")
		cwd := t.TempDir()

		Convey("Then nothing is detected", func() {
			So(agent.PiMCPAdapterPresent(home, cwd), ShouldBeFalse)
		})

		Convey("When the package is listed as a string with a version", func() {
			writeFile(t, filepath.Join(piDir, "settings.json"), `{"packages": ["npm:pi-mcp-adapter@1.2.3"]}`)

			Convey("Then the adapter is detected", func() {
				So(agent.PiMCPAdapterPresent(home, cwd), ShouldBeTrue)
			})
		})

		Convey("When the package is listed in the object form", func() {
			writeFile(t, filepath.Join(piDir, "settings.json"),
				`{"packages": [{"source": "git:github.com/nicobailon/pi-mcp-adapter", "extensions": []}]}`)

			Convey("Then the adapter is detected", func() {
				So(agent.PiMCPAdapterPresent(home, cwd), ShouldBeTrue)
			})
		})

		Convey("When the settings name other packages only", func() {
			writeFile(t, filepath.Join(piDir, "settings.json"), `{"packages": ["npm:pi-skills"], "extensions": []}`)

			Convey("Then nothing is detected", func() {
				So(agent.PiMCPAdapterPresent(home, cwd), ShouldBeFalse)
			})
		})

		Convey("When the managed npm catalog holds the package", func() {
			writeFile(t, filepath.Join(piDir, "npm", "node_modules", "pi-mcp-adapter", "package.json"), `{"name":"pi-mcp-adapter"}`)

			Convey("Then the adapter is detected", func() {
				So(agent.PiMCPAdapterPresent(home, cwd), ShouldBeTrue)
			})
		})

		Convey("When the settings file is broken", func() {
			writeFile(t, filepath.Join(piDir, "settings.json"), `{"packages": `)

			Convey("Then detection stays quiet", func() {
				So(agent.PiMCPAdapterPresent(home, cwd), ShouldBeFalse)
			})
		})

		Convey("When the adapter is registered in the project scope", func() {
			writeFile(t, filepath.Join(cwd, ".pi", "settings.json"), `{"packages": ["npm:pi-mcp-adapter"]}`)

			Convey("Then the adapter is detected", func() {
				So(agent.PiMCPAdapterPresent(home, cwd), ShouldBeTrue)
			})
		})

		Convey("When the project npm catalog holds the package", func() {
			writeFile(t, filepath.Join(cwd, ".pi", "npm", "node_modules", "pi-mcp-adapter", "package.json"), `{"name":"pi-mcp-adapter"}`)

			Convey("Then the adapter is detected", func() {
				So(agent.PiMCPAdapterPresent(home, cwd), ShouldBeTrue)
			})
		})

		Convey("When no cwd is given", func() {
			writeFile(t, filepath.Join(cwd, ".pi", "settings.json"), `{"packages": ["npm:pi-mcp-adapter"]}`)

			Convey("Then only the user scope is checked", func() {
				So(agent.PiMCPAdapterPresent(home, ""), ShouldBeFalse)
			})
		})
	})
}
