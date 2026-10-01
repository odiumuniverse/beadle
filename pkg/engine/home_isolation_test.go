package engine_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// isolatedHome is the temp home TestMain pins the environment to, and
// isolatedBinDir the PATH it pins: a bin directory of the suite's own holding
// git and no agent CLI.
var (
	realHome       string
	realBefore     []string
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
	// The machine's real home, recorded BEFORE the pin, so the suite can be
	// asked afterwards whether it left anything there. These packages resolve
	// host roots, which is how a suite with no pin ends up writing into the
	// developer's ~/.verger: a root that does not exist in a temporary vault
	// falls through to the standalone home, and the standalone home is the
	// real one.
	realHome = os.Getenv("HOME")

	if entries, readErr := os.ReadDir(realHome); readErr == nil {
		for _, entry := range entries {
			realBefore = append(realBefore, entry.Name())
		}
	}

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
		"HOME":            home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"BEADLE_HOME":     filepath.Join(home, ".beadle"),
		// VERGER_HOME is discovery's FIRST rule, so a developer who has it
		// set would otherwise have the whole suite read and write their own
		// plugin home while every test passes.
		"VERGER_HOME":            "",
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

	code := m.Run()

	// The suite is over, so this is the only moment at which the real home can
	// be weighed against the snapshot taken before the pin. Inside a test the
	// check runs in file order, and in pkg/engine alone 93 test files sort
	// after this one: it would report a clean home while two thirds of the
	// suite had yet to run.
	if !realHomeUnchanged() {
		code = 1
	}

	return code
}

// realHomeUnchanged reports whether the suite left the machine's real home as it
// found it. A run with no HOME has nothing to weigh against; the pin is still in
// place, so there is nothing to guard.
func realHomeUnchanged() bool {
	if realHome == "" {
		return true
	}

	after, err := os.ReadDir(realHome)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read the real home", realHome+":", err)

		return false
	}

	clean := true

	for _, entry := range after {
		if slices.Contains(realBefore, entry.Name()) {
			continue
		}

		// Every offender is named, not just the first: one run that leaves
		// three entries should say so once, rather than send the reader back
		// for the other two.
		fmt.Fprintln(os.Stderr, "the suite created", filepath.Join(realHome, entry.Name()))

		clean = false
	}

	return clean
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
	// Only the pins are checked here. Whether the suite left the machine's real home
	// untouched is realHomeUnchanged, run by TestMain after m.Run(): a check made
	// in a test would only weigh the files that happen to sort before it.
	Convey("Given the isolated test suite", t, func() {
		Convey("When a test reads the environment", func() {
			Convey("Then every home-shaped variable points into the temp isolation dir", func() {
				So(isolatedHome, ShouldNotBeEmpty)

				home, err := os.UserHomeDir()
				So(err, ShouldBeNil)
				// Cleaned on both sides, for the same reason as the variables below:
				// os.UserHomeDir hands over the spelling the process environment carries.
				So(strings.HasPrefix(filepath.Clean(home), filepath.Clean(isolatedHome)), ShouldBeTrue)

				for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "BEADLE_HOME", "DSH_HOME"} {
					// Cleaned on both sides: TMPDIR arrives from macOS with a trailing
					// separator and a mktemp template doubles it, so the variable and
					// the temp dir can spell the same directory two ways.
					So(filepath.Clean(os.Getenv(name)), ShouldContainSubstring, filepath.Clean(isolatedHome))
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
