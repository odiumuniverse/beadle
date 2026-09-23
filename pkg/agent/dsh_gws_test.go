package agent_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
)

// withoutDSHHome unsets DSH_HOME for the test: t.Setenv cannot express
// "unset", and the machine running the tests may have it set.
func withoutDSHHome(t *testing.T) {
	t.Helper()

	if value, ok := os.LookupEnv("DSH_HOME"); ok {
		// t.Setenv restores the original value once the test ends.
		t.Setenv("DSH_HOME", value)
	}

	if err := os.Unsetenv("DSH_HOME"); err != nil {
		t.Fatalf("unset DSH_HOME: %v", err)
	}
}

// withoutDSHBinary keeps user installs out of PATH, so a machine with dsh
// installed still exercises the "not found" path.
func withoutDSHBinary(t *testing.T) {
	t.Helper()

	t.Setenv("PATH", "/usr/bin:/bin")
}

func TestDSHDetect(t *testing.T) {
	Convey("Given a home without DSH", t, func() {
		home := t.TempDir()

		withoutDSHHome(t)
		withoutDSHBinary(t)

		Convey("When the adapter detects", func() {
			detected, err := agent.DSHDetected(home)

			Convey("Then it is not found and reports no error", func() {
				So(err, ShouldBeNil)
				So(detected, ShouldBeFalse)

				found, detectErr := agent.DSH(home, home).Detect()
				So(detectErr, ShouldBeNil)
				So(found, ShouldBeFalse)

				So(agent.DSHProfiles(home), ShouldBeEmpty)
			})
		})
	})

	Convey("Given a dsh binary on PATH", t, func() {
		home := t.TempDir()
		bin := t.TempDir()

		withoutDSHHome(t)

		if err := os.WriteFile(filepath.Join(bin, "dsh"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil { //nolint:gosec // G306: the fake dsh has to be executable for LookPath
			t.Fatalf("write dsh: %v", err)
		}

		t.Setenv("PATH", bin)

		Convey("When the adapter detects", func() {
			detected, err := agent.DSHDetected(home)

			Convey("Then the binary alone marks the harness present", func() {
				So(err, ShouldBeNil)
				So(detected, ShouldBeTrue)
			})
		})
	})

	Convey("Given DSH_HOME with instructions, skills and profiles", t, func() {
		dir := t.TempDir()

		writeFile(t, filepath.Join(dir, "AGENTS.md"), "# harness\n")
		writeFile(t, filepath.Join(dir, "skills", "alpha", "SKILL.md"), "# alpha\n")
		So(os.MkdirAll(filepath.Join(dir, "profiles", "default"), 0o750), ShouldBeNil)
		So(os.MkdirAll(filepath.Join(dir, "profiles", "headless"), 0o750), ShouldBeNil)

		t.Setenv("DSH_HOME", dir)

		Convey("When the adapter detects", func() {
			detected, err := agent.DSHDetected(t.TempDir())

			Convey("Then the paths, defaults and profiles are reported", func() {
				So(err, ShouldBeNil)
				So(detected, ShouldBeTrue)

				rules, skills := agent.DSHSurfacePaths(t.TempDir())
				So(rules, ShouldEqual, filepath.Join(dir, "AGENTS.md"))
				So(skills, ShouldEqual, filepath.Join(dir, "skills"))

				So(agent.DSHProfiles(t.TempDir()), ShouldResemble, []string{"default", "headless"})

				adapter := agent.DSH(t.TempDir(), t.TempDir())
				So(adapter.ID, ShouldEqual, agent.DSHID)
				So(adapter.Surfaces, ShouldHaveLength, 2)
				So(agent.ByID(agent.All(t.TempDir(), t.TempDir()), agent.DSHID), ShouldNotBeNil)

				// A-38 flips the rules surface to write; skills stay pull
				// until A-39.
				So(adapter.Surfaces[0].Traits().DefaultMode, ShouldEqual, config.ModeSync)
				So(adapter.Surfaces[1].Traits().DefaultMode, ShouldEqual, config.ModePull)
			})
		})
	})

	Convey("Given an empty DSH_HOME", t, func() {
		home := t.TempDir()

		So(os.MkdirAll(filepath.Join(home, ".dsh", "profiles", "default"), 0o750), ShouldBeNil)

		t.Setenv("DSH_HOME", "")

		Convey("When the adapter resolves the home", func() {
			dir, emptyEnv := agent.DSHHome(home)

			Convey("Then the empty value is ignored and ~/.dsh is used", func() {
				So(emptyEnv, ShouldBeTrue)
				So(dir, ShouldEqual, filepath.Join(home, ".dsh"))

				detected, err := agent.DSHDetected(home)
				So(err, ShouldBeNil)
				So(detected, ShouldBeTrue)

				So(agent.DSHProfiles(home), ShouldResemble, []string{"default"})
			})
		})
	})
}
