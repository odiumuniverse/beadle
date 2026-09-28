package engine_test

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

// isolateTestHome pins HOME, XDG_CONFIG_HOME, BEADLE_HOME, DSH_HOME and an
// empty DSH_AGENTS_HOME to a temp directory before the suite runs: a test that
// forgets its own t.Setenv("HOME", …) must never touch the developer's real
// home. The empty DSH_AGENTS_HOME makes the DSH adapter resolve its shared
// root (~/.agents) from each test's own home instead of a developer's value,
// and the empty omp variables keep the omp plugin root (~/.omp, or the
// PI_CONFIG_DIR directory) under each test's own home; a test that wants
// another location overrides them with t.Setenv, which wins.
// The XDG location points at the same <home>/.config a fallback would resolve
// to, so tests that compute the path from HOME keep matching.
func isolateTestHome(m *testing.M) int {
	home, err := os.MkdirTemp("", "beadle-engine-test-home") //nolint:usetesting // TestMain has no *testing.T to hang t.TempDir on
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
		"HOME":                   home,
		"XDG_CONFIG_HOME":        filepath.Join(home, ".config"),
		"BEADLE_HOME":            filepath.Join(home, ".beadle"),
		"DSH_HOME":               filepath.Join(home, ".dsh"),
		"DSH_AGENTS_HOME":        "",
		"PI_CONFIG_DIR":          "",
		"PI_CODING_AGENT_DIR":    "",
		"OMP_PROFILE":            "",
		"PI_PROFILE":             "",
		"BEADLE_ALLOW_HOME_MOVE": "",
		"PATH":                   path,
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

// isolatedTestBinDir builds the PATH the suite runs with: the suite's own bin
// directory — holding git and no agent CLI — followed by the two directories
// the POSIX tools live in. A host whose binary happens to be installed on the
// machine running the suite would otherwise be "detected" by the surfaces under
// test, and the sync would then write a host file the fixtures never created.
// The system directories stay because the host stubs the tests install are
// shell scripts that call grep/cut/head themselves.
//
// It returns the directory to remove and the PATH value to pin.
func isolatedTestBinDir() (dir, path string, err error) {
	dir, err = os.MkdirTemp("", "beadle-engine-test-bin") //nolint:usetesting // TestMain has no *testing.T to hang t.TempDir on
	if err != nil {
		return "", "", err
	}

	path = dir + ":/usr/bin:/bin"

	// git is linked in when the machine has one; a box without git simply
	// runs the suite with no history, which the tests already tolerate.
	if git, lookErr := exec.LookPath("git"); lookErr == nil {
		if err := os.Symlink(git, filepath.Join(dir, "git")); err != nil {
			return "", "", err
		}
	}

	return dir, path, nil
}

func TestMain(m *testing.M) {
	os.Exit(isolateTestHome(m))
}

func TestSuiteHomeIsolation(t *testing.T) {
	Convey("Given the isolated test suite", t, func() {
		Convey("When a test reads the environment", func() {
			Convey("Then every home-shaped variable points into the temp isolation dir", func() {
				So(isolatedHome, ShouldNotBeEmpty)

				home, err := os.UserHomeDir()
				So(err, ShouldBeNil)
				So(strings.HasPrefix(home, isolatedHome), ShouldBeTrue)

				for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "BEADLE_HOME", "DSH_HOME"} {
					So(os.Getenv(name), ShouldContainSubstring, isolatedHome)
				}

				// DSH_AGENTS_HOME and the omp variables are pinned empty: the
				// adapters resolve their defaults from each test's own home,
				// so a developer's value cannot leak into the suite.
				So(os.Getenv("DSH_AGENTS_HOME"), ShouldBeEmpty)

				for _, name := range []string{
					"PI_CONFIG_DIR", "PI_CODING_AGENT_DIR", "OMP_PROFILE", "PI_PROFILE", "BEADLE_ALLOW_HOME_MOVE",
				} {
					So(os.Getenv(name), ShouldBeEmpty)
				}

				// PATH is pinned to a bin directory of the suite's own, so no
				// host CLI installed on this machine can be detected.
				So(os.Getenv("PATH"), ShouldEqual, isolatedBinDir)
			})
		})
	})
}
