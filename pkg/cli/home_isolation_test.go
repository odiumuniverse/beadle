package cli

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
// git and nothing else.
var (
	isolatedHome   string
	isolatedBinDir string
)

// isolateTestRoots pins every host root that is not $HOME itself to the home
// the test has just claimed. A test that moves HOME but leaves DSH_HOME at the
// value TestMain pinned writes dsh's rules and skills into the *shared* suite
// home, where the next test finds them: `beadle init` then enables dsh, and the
// first sync opens a conflict out of the previous test's files. That is why
// the suite only broke under `go test -count=3` — the second pass found the
// first pass's files, and the pass that created them had answered correctly.
//
// It is deliberately not part of TestMain: that home is shared on purpose, and
// the point here is that a per-test home is hermetic for *every* host, not only
// for the ones that read $HOME.
func isolateTestRoots(t *testing.T) {
	t.Helper()

	home := os.Getenv("HOME")

	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("DSH_HOME", filepath.Join(home, ".dsh"))

	// These fall back to a path under $HOME when empty, which is what
	// isolation needs. Pinning them empty also stops a value inherited from
	// the developer's shell from sending a host outside the test home.
	for _, name := range []string{
		"DSH_AGENTS_HOME", "PI_CONFIG_DIR", "PI_CODING_AGENT_DIR", "OMP_PROFILE",
	} {
		t.Setenv(name, "")
	}

	// The vault is the other root a test has to own. A test that moves HOME
	// but leaves BEADLE_HOME at the suite's value runs `init` into a vault
	// every other test shares, and the second pass of `go test -count=3`
	// finds the first pass's state there — an already-absorbed local package,
	// for one, whose source the first pass consumed.
	t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))
}

// isolateTestHome pins every variable that steers beadle to a host's files —
// HOME, XDG_CONFIG_HOME, BEADLE_HOME, DSH_HOME, DSH_AGENTS_HOME, the omp root
// and profile keys, BEADLE_ALLOW_HOME_MOVE and PATH — to a temp directory
// before the suite runs: a test that forgets its own t.Setenv("HOME", …) must
// never touch the developer's real home, and one that runs `beadle init` must
// not enable an agent just because its CLI happens to be installed on the
// machine running the suite. The XDG location points at the same
// <home>/.config a fallback would resolve to, so tests that compute the path
// from HOME keep matching. A test that needs another location overrides it
// with t.Setenv, which wins.

func isolateTestHome(m *testing.M) int {
	home, err := os.MkdirTemp("", "beadle-test-home") //nolint:usetesting // TestMain has no *testing.T to hang t.TempDir on
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
// machine running the suite would otherwise be "detected" by `beadle init`,
// and the first sync would then write a host file the fixtures never created:
// the exact Linux-only failure seen on a box that carries dsh. The system
// directories stay because the host stubs the tests install are shell scripts
// that call grep/cut/head themselves.
//
// It returns the directory to remove and the PATH value to pin.
func isolatedTestBinDir() (dir, path string, err error) {
	dir, err = os.MkdirTemp("", "beadle-test-bin") //nolint:usetesting // TestMain has no *testing.T to hang t.TempDir on
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

				So(os.Getenv("XDG_CONFIG_HOME"), ShouldEqual, filepath.Join(isolatedHome, ".config"))

				for _, name := range []string{"PI_CONFIG_DIR", "PI_CODING_AGENT_DIR", "OMP_PROFILE"} {
					So(os.Getenv(name), ShouldBeEmpty)
				}
			})
		})
	})
}
