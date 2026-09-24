package agent_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// isolatedHome is the temp home TestMain pins the environment to.
var isolatedHome string

// isolateTestHome pins HOME, BEADLE_HOME, DSH_HOME and an empty
// DSH_AGENTS_HOME to a temp directory before the suite runs: a test that
// forgets its own t.Setenv("HOME", …) must never touch the developer's real
// home. The empty DSH_AGENTS_HOME makes the DSH adapter resolve its shared
// root (~/.agents) from each test's own home instead of a developer's value;
// a test that wants another location overrides it with t.Setenv, which wins.
// XDG_CONFIG_HOME is deliberately left alone here: most of this package's
// tests override HOME per test and rely on the code falling back to
// <home>/.config, so pinning XDG would send the code into the isolation
// directory while the test writes into its own temp home.
func isolateTestHome(m *testing.M) int {
	home, err := os.MkdirTemp("", "beadle-agent-test-home") //nolint:usetesting // TestMain has no *testing.T to hang t.TempDir on
	if err != nil {
		fmt.Fprintln(os.Stderr, "create test home:", err)

		return 1
	}

	defer func() { _ = os.RemoveAll(home) }()

	for name, value := range map[string]string{
		"HOME":            home,
		"BEADLE_HOME":     filepath.Join(home, ".beadle"),
		"DSH_HOME":        filepath.Join(home, ".dsh"),
		"DSH_AGENTS_HOME": "",
	} {
		//nolint:usetesting // TestMain cannot use t.Setenv; tests override per test
		if err := os.Setenv(name, value); err != nil {
			fmt.Fprintln(os.Stderr, "set", name+":", err)

			return 1
		}
	}

	isolatedHome = home

	return m.Run()
}

func TestMain(m *testing.M) {
	os.Exit(isolateTestHome(m))
}

func TestSuiteHomeIsolation(t *testing.T) {
	Convey("Given the isolated test suite", t, func() {
		Convey("When a test reads the environment", func() {
			Convey("Then every pinned home-shaped variable points into the temp isolation dir", func() {
				So(isolatedHome, ShouldNotBeEmpty)

				home, err := os.UserHomeDir()
				So(err, ShouldBeNil)
				So(strings.HasPrefix(home, isolatedHome), ShouldBeTrue)

				for _, name := range []string{"HOME", "BEADLE_HOME", "DSH_HOME"} {
					So(os.Getenv(name), ShouldContainSubstring, isolatedHome)
				}

				// DSH_AGENTS_HOME is pinned empty: the DSH adapter resolves
				// its default (~/.agents) from each test's own home, so a
				// developer's value cannot leak into the suite.
				So(os.Getenv("DSH_AGENTS_HOME"), ShouldBeEmpty)
			})
		})
	})
}
