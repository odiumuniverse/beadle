package agent_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// isolatedHome is the temp home TestMain pins the environment to, and
// isolatedBinDir the PATH it pins: a bin directory of the suite's own holding
// git and no agent CLI.
var (
	isolatedHome   string
	isolatedBinDir string
)

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

	dir, path, err := isolatedTestBinDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "create test bin dir:", err)

		return 1
	}

	defer func() { _ = os.RemoveAll(dir) }()

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
		"PATH":                path,
	} {
		//nolint:usetesting // TestMain cannot use t.Setenv; tests override per test
		if err := os.Setenv(name, value); err != nil {
			fmt.Fprintln(os.Stderr, "set", name+":", err)

			return 1
		}
	}

	isolatedHome = home
	isolatedBinDir = path

	return m.Run()
}

func TestMain(m *testing.M) {
	os.Exit(isolateTestHome(m))
}

// isolatedTestBinDir builds the PATH the suite runs with: the suite's own bin
// directory — holding git and no agent CLI — followed by the two directories the
// POSIX tools live in. This package's Detect functions fall back to
// exec.LookPath when a host's home directory is missing, so without the pin a
// host CLI installed on the machine running the suite would change what Detect
// answers (and, with it, every test that calls it). The system directories stay
// because the stubs the tests install are shell scripts that call
// grep/cut/head themselves.
//
// It returns the directory to remove and the PATH value to pin.
func isolatedTestBinDir() (dir, path string, err error) {
	dir, err = os.MkdirTemp("", "beadle-agent-test-bin") //nolint:usetesting // TestMain has no *testing.T to hang t.TempDir on
	if err != nil {
		return "", "", err
	}

	path = dir + ":/usr/bin:/bin"

	// git is linked in when the machine has one; a box without git simply runs
	// the suite with no history, which the tests already tolerate.
	if git, lookErr := exec.LookPath("git"); lookErr == nil {
		if err := os.Symlink(git, filepath.Join(dir, "git")); err != nil {
			return "", "", err
		}
	}

	return dir, path, nil
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

				// PATH is pinned to a bin directory of the suite's own: the
				// Detect functions fall back to exec.LookPath, so a host CLI
				// installed on the machine running the suite would otherwise
				// change what Detect answers.
				So(os.Getenv("PATH"), ShouldEqual, isolatedBinDir)

				// dshBinary and ompBinary (pkg/agent/dsh.go, omp.go) are
				// unexported, so the names the Detect fallbacks probe are
				// spelled here.
				for _, binary := range []string{"dsh", "omp"} {
					path, lookErr := exec.LookPath(binary)
					So(lookErr, ShouldNotBeNil)
					So(path, ShouldBeEmpty)
				}
			})
		})
	})
}
