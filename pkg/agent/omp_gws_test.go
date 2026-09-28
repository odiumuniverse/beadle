package agent_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// unsetEnv removes an environment variable for the test duration: t.Setenv
// cannot express "unset", and the machine running the tests may have the
// variable set.
func unsetEnv(t *testing.T, name string) {
	t.Helper()

	if value, ok := os.LookupEnv(name); ok {
		// t.Setenv restores the original value once the test ends.
		t.Setenv(name, value)
	}

	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unset %s: %v", name, err)
	}
}

// withoutOmpEnv clears the omp root and profile environment: a developer's
// PI_CONFIG_DIR/OMP_PROFILE must not leak into path assertions, and the omp
// binary on PATH must not make detection look positive by accident.
func withoutOmpEnv(t *testing.T) {
	t.Helper()

	unsetEnv(t, "PI_CONFIG_DIR")
	unsetEnv(t, "PI_CODING_AGENT_DIR")
	unsetEnv(t, "OMP_PROFILE")
	unsetEnv(t, "PI_PROFILE")
	t.Setenv("PATH", "/usr/bin:/bin")
}

func TestOmpHomeAndAgentDir(t *testing.T) {
	Convey("Given a home without omp environment", t, func() {
		home := t.TempDir()

		withoutOmpEnv(t)

		Convey("When the root and the agent dir resolve", func() {
			dir, emptyEnv := agent.OmpHome(home)

			Convey("Then they are the omp defaults", func() {
				So(emptyEnv, ShouldBeFalse)
				So(dir, ShouldEqual, filepath.Join(home, ".omp"))
				So(agent.OmpAgentDir(home), ShouldEqual, filepath.Join(home, ".omp", "agent"))
			})
		})

		Convey("When PI_CONFIG_DIR names another root", func() {
			t.Setenv("PI_CONFIG_DIR", ".omp-alt")

			Convey("Then the root and the agent dir follow it", func() {
				dir, emptyEnv := agent.OmpHome(home)
				So(emptyEnv, ShouldBeFalse)
				So(dir, ShouldEqual, filepath.Join(home, ".omp-alt"))
				So(agent.OmpAgentDir(home), ShouldEqual, filepath.Join(home, ".omp-alt", "agent"))
			})
		})

		Convey("When PI_CONFIG_DIR is empty", func() {
			t.Setenv("PI_CONFIG_DIR", "")

			Convey("Then it is ignored and ~/.omp applies", func() {
				dir, emptyEnv := agent.OmpHome(home)
				So(emptyEnv, ShouldBeTrue)
				So(dir, ShouldEqual, filepath.Join(home, ".omp"))
			})
		})

		Convey("When PI_CODING_AGENT_DIR overrides the agent dir", func() {
			target := filepath.Join(home, "custom-agent")

			t.Setenv("PI_CODING_AGENT_DIR", target)

			Convey("Then the agent dir is taken literally", func() {
				So(agent.OmpAgentDir(home), ShouldEqual, target)
			})
		})

		Convey("When a named profile is active", func() {
			t.Setenv("OMP_PROFILE", "work")
			t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, "ignored"))

			Convey("Then the profile dir wins and PI_CODING_AGENT_DIR is ignored", func() {
				So(agent.OmpAgentDir(home), ShouldEqual, filepath.Join(home, ".omp", "profiles", "work", "agent"))
			})
		})

		Convey("When only the legacy PI_PROFILE is set", func() {
			t.Setenv("PI_PROFILE", "legacy")

			Convey("Then it selects the profile", func() {
				So(agent.OmpAgentDir(home), ShouldEqual, filepath.Join(home, ".omp", "profiles", "legacy", "agent"))
			})
		})

		Convey("When OMP_PROFILE and PI_PROFILE disagree", func() {
			t.Setenv("OMP_PROFILE", "primary")
			t.Setenv("PI_PROFILE", "legacy")

			Convey("Then OMP_PROFILE wins", func() {
				So(agent.OmpAgentDir(home), ShouldEqual, filepath.Join(home, ".omp", "profiles", "primary", "agent"))
			})
		})

		for _, name := range []string{"default", "  "} {
			Convey("When OMP_PROFILE is "+name, func() {
				t.Setenv("OMP_PROFILE", name)

				Convey("Then the default profile applies", func() {
					So(agent.OmpAgentDir(home), ShouldEqual, filepath.Join(home, ".omp", "agent"))
				})
			})
		}
	})
}

func TestOmpDetect(t *testing.T) {
	Convey("Given a home without omp and a PATH without the binary", t, func() {
		home := t.TempDir()

		withoutOmpEnv(t)

		Convey("When the adapter detects", func() {
			detected, err := agent.OmpDetected(home)

			Convey("Then it is not found and reports no error", func() {
				So(err, ShouldBeNil)
				So(detected, ShouldBeFalse)

				found, detectErr := agent.Omp(home, home).Detect()
				So(detectErr, ShouldBeNil)
				So(found, ShouldBeFalse)

				So(agent.OmpProfiles(home), ShouldBeEmpty)
			})
		})
	})

	Convey("Given an omp agent config.yml", t, func() {
		home := t.TempDir()

		withoutOmpEnv(t)
		writeFile(t, filepath.Join(home, ".omp", "agent", "config.yml"), "setupVersion: 2\n")
		So(os.MkdirAll(filepath.Join(home, ".omp", "profiles", "work"), 0o750), ShouldBeNil)

		Convey("When the adapter detects", func() {
			detected, err := agent.OmpDetected(home)

			Convey("Then the config file alone marks omp present", func() {
				So(err, ShouldBeNil)
				So(detected, ShouldBeTrue)
				So(agent.OmpProfiles(home), ShouldResemble, []string{"work"})
				So(agent.InboxPath(home, agent.OmpID), ShouldEqual, filepath.Join(home, ".omp", "agent", "inbox.md"))
			})
		})
	})

	Convey("Given an omp binary on PATH", t, func() {
		home := t.TempDir()
		bin := t.TempDir()

		withoutOmpEnv(t)

		if err := os.WriteFile(filepath.Join(bin, "omp"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil { //nolint:gosec // G306: the fake omp has to be executable for LookPath
			t.Fatalf("write omp: %v", err)
		}

		t.Setenv("PATH", bin)

		Convey("When the adapter detects", func() {
			detected, err := agent.OmpDetected(home)

			Convey("Then the binary alone marks omp present", func() {
				So(err, ShouldBeNil)
				So(detected, ShouldBeTrue)
			})
		})
	})
}

func TestOmpSurfaces(t *testing.T) {
	Convey("Given the omp adapter", t, func() {
		home := t.TempDir()
		cwd := t.TempDir()

		withoutOmpEnv(t)

		a := agent.Omp(home, cwd)
		agentDir := filepath.Join(home, ".omp", "agent")

		Convey("Then the identity and the surface set are the documented ones", func() {
			So(a.ID, ShouldEqual, agent.OmpID)
			So(a.ID, ShouldEqual, "omp")
			So(a.Name, ShouldEqual, "oh-my-pi")
			So(a.OptIn, ShouldBeFalse)
			So(agent.ByID(agent.All(home, cwd), agent.OmpID), ShouldNotBeNil)

			So(a.Surfaces, ShouldHaveLength, 7)
			So(a.Surfaces[0].Kind(), ShouldEqual, kind.Rules)
			So(a.Surfaces[1].Kind(), ShouldEqual, kind.MCP)
			So(a.Surfaces[2].Kind(), ShouldEqual, kind.Skills)
			So(a.Surfaces[3].Kind(), ShouldEqual, kind.Subagents)
			So(a.Surfaces[4].Kind(), ShouldEqual, kind.Commands)
			So(a.Surfaces[5].Kind(), ShouldEqual, kind.Projects)
			So(a.Surfaces[6].Kind(), ShouldEqual, kind.Projects)
		})

		Convey("Then every path, watch path and default mode is pinned", func() {
			So(a.Surfaces[0].Path(), ShouldEqual, filepath.Join(agentDir, "AGENTS.md"))
			So(a.Surfaces[0].WatchPaths(), ShouldResemble, []string{filepath.Join(agentDir, "AGENTS.md")})
			So(a.Surfaces[0].Traits().DefaultMode, ShouldEqual, config.ModeSync)
			So(a.Surfaces[0].Traits().Creatable, ShouldBeTrue)

			So(a.Surfaces[1].Path(), ShouldEqual, filepath.Join(agentDir, "mcp.json"))
			So(a.Surfaces[1].WatchPaths(), ShouldResemble, []string{filepath.Join(agentDir, "mcp.json")})
			So(a.Surfaces[1].Traits().DefaultMode, ShouldEqual, config.ModeSync)
			So(a.Surfaces[1].Traits().ReloadHint, ShouldContainSubstring, "/mcp reload")

			So(a.Surfaces[2].Path(), ShouldEqual, filepath.Join(agentDir, "skills"))
			So(a.Surfaces[2].WatchPaths(), ShouldResemble, []string{filepath.Join(agentDir, "skills")})
			So(a.Surfaces[2].Traits().DefaultMode, ShouldEqual, config.ModePull)
			So(a.Surfaces[2].Traits().Creatable, ShouldBeTrue)

			So(a.Surfaces[3].Path(), ShouldEqual, filepath.Join(agentDir, "agents"))
			So(a.Surfaces[3].WatchPaths(), ShouldResemble, []string{filepath.Join(agentDir, "agents")})
			So(a.Surfaces[3].Traits().DefaultMode, ShouldEqual, config.ModeSync)
			So(a.Surfaces[3].Traits().ReloadHint, ShouldEqual, "omp discovers task agents per session")

			So(a.Surfaces[4].Path(), ShouldEqual, filepath.Join(agentDir, "commands"))
			So(a.Surfaces[4].WatchPaths(), ShouldResemble, []string{filepath.Join(agentDir, "commands")})
			So(a.Surfaces[4].Traits().DefaultMode, ShouldEqual, config.ModeSync)
			So(a.Surfaces[4].Traits().ReloadHint, ShouldContainSubstring, "/reload-plugins")

			// The project scope lives under .omp/, not at the checkout root.
			So(a.Surfaces[5].Path(), ShouldEqual, filepath.Join(cwd, ".omp", "mcp.json"))
			So(a.Surfaces[5].WatchPaths(), ShouldResemble, []string{filepath.Join(cwd, ".omp", "mcp.json")})
			So(a.Surfaces[6].Path(), ShouldEqual, filepath.Join(cwd, ".omp", "AGENTS.md"))
			So(a.Surfaces[6].WatchPaths(), ShouldResemble, []string{filepath.Join(cwd, ".omp", "AGENTS.md")})

			for _, surface := range a.Surfaces[5:] {
				projector, ok := surface.(agent.ProjectFile)
				So(ok, ShouldBeTrue)
				So(projector.ProjectRel(), ShouldNotEqual, "AGENTS.md")
			}
		})

		Convey("Then the non-goal surfaces are absent", func() {
			So(a.Surface(kind.Permissions), ShouldBeNil)
			So(a.Surface(kind.Memory), ShouldBeNil)
		})
	})
}
