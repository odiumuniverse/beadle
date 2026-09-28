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

// isolateTestHome pins HOME, BEADLE_HOME, DSH_HOME, an empty
// DSH_AGENTS_HOME and the omp root/profile environment to a temp directory
// before the suite runs: a test that forgets its own t.Setenv("HOME", …) must
// never touch the developer's real home. The empty DSH_AGENTS_HOME makes the
// DSH adapter resolve its shared root (~/.agents) from each test's own home
// instead of a developer's value; a test that wants another location
// overrides it with t.Setenv, which wins. The omp keys are pinned empty for
// the same reason: the omp adapter resolves its root from each test's own
// home unless a test sets PI_CONFIG_DIR/PI_CODING_AGENT_DIR/OMP_PROFILE or
// the legacy PI_PROFILE.
// XDG_CONFIG_HOME is pinned empty for the same reason, not left alone: the
// OpenCode adapters resolve ~/.config/opencode from it whenever it is set,
// even when a test passes an explicit home, so an ambient value (GitHub's
// ubuntu runners export XDG_CONFIG_HOME=$HOME/.config) would redirect the
// suite into the developer's or the runner's real config tree. Empty keeps
// the code on each test's own <home>/.config; tests that exercise the XDG
// override set it themselves with t.Setenv, which wins.
func isolateTestHome(m *testing.M) int {
	home, err := os.MkdirTemp("", "beadle-agent-test-home") //nolint:usetesting // TestMain has no *testing.T to hang t.TempDir on
	if err != nil {
		fmt.Fprintln(os.Stderr, "create test home:", err)

		return 1
	}

	defer func() { _ = os.RemoveAll(home) }()

	for name, value := range map[string]string{
		"HOME":                home,
		"BEADLE_HOME":         filepath.Join(home, ".beadle"),
		"XDG_CONFIG_HOME":     "",
		"DSH_HOME":            filepath.Join(home, ".dsh"),
		"DSH_AGENTS_HOME":     "",
		"PI_CONFIG_DIR":       "",
		"PI_CODING_AGENT_DIR": "",
		"OMP_PROFILE":         "",
		"PI_PROFILE":          "",
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

				// The omp root/profile keys are pinned empty for the same
				// reason: the omp adapter resolves its root from each test's
				// own home.
				for _, name := range []string{"PI_CONFIG_DIR", "PI_CODING_AGENT_DIR", "OMP_PROFILE", "PI_PROFILE"} {
					So(os.Getenv(name), ShouldBeEmpty)
				}

				// XDG_CONFIG_HOME is pinned empty: the OpenCode adapters
				// prefer it over the home a test passes, so an ambient value
				// (a Linux CI runner exports one) would redirect the suite
				// into a real config tree.
				So(os.Getenv("XDG_CONFIG_HOME"), ShouldBeEmpty)
			})
		})
	})
}
