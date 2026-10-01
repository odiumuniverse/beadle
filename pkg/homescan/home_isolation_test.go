package homescan_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/homescan"
)

// This package resolves host roots, and a host root is resolved from the
// environment as much as from the home it is given: Roots builds a
// hostpath.Env whose Lookup is os.LookupEnv, so an ambient XDG_CONFIG_HOME
// moves the opencode and kilo surfaces out of the home the caller passed and
// the caller gets a root list for a tree it never named. That is the product
// being honest about what the host reads, and the suite pins the environment
// around it rather than arguing with it.
//
// The pin is the same one pkg/agent, pkg/cli, pkg/engine and pkg/vergerx use.
var (
	realHome     string
	realBefore   homescan.Snapshot
	isolatedHome string
)

func TestMain(m *testing.M) {
	os.Exit(isolateTestHome(m))
}

// isolateTestHome pins HOME, the XDG locations, BEADLE_HOME, DSH_HOME and the
// omp keys to a temp directory before the suite runs: a test that forgets its
// own t.Setenv("HOME", …) must never touch the developer's real home, and a
// test that forgets to pin the environment must not have its expectations
// rewritten by whatever the machine happens to export.
//
// The XDG variables are pinned rather than left alone, which is the part worth
// arguing for. An ubuntu GitHub runner exports XDG_CONFIG_HOME=$HOME/.config,
// and beadle's hostpath follows it: the opencode and kilo surfaces then live
// under the runner's XDG root instead of under the home a test passed. Before
// the pin that turned TestRootsAreTheSurfacesAndNotTheHomesAroundThem red on
// ubuntu and green on a mac — a test that could only be run on the machine that
// wrote it. XDG_CONFIG_HOME points at the same <home>/.config a fallback would
// resolve to, so tests that compute the path from HOME keep matching, and the
// other three are pinned empty because the product only reads them through
// verger's store root, which treats a relative or absent value as "under the
// home". A test that wants another location overrides with t.Setenv, which wins.
func isolateTestHome(m *testing.M) int {
	// The machine's real home, recorded BEFORE the pin, so the suite can be
	// asked afterwards whether it left anything there.
	realHome = homescan.GuardHome()

	realBefore = homescan.Take(realHome)

	home, err := os.MkdirTemp("", "beadle-homescan-test-home") //nolint:usetesting // TestMain has no *testing.T to hang t.TempDir on
	if err != nil {
		fmt.Fprintln(os.Stderr, "create test home:", err)

		return 1
	}

	defer func() { _ = os.RemoveAll(home) }()

	for name, value := range map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_DATA_HOME":   "",
		"XDG_STATE_HOME":  "",
		"XDG_CACHE_HOME":  "",
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
	} {
		//nolint:usetesting // TestMain cannot use t.Setenv; tests override per test
		if err := os.Setenv(name, value); err != nil {
			fmt.Fprintln(os.Stderr, "set", name+":", err)

			return 1
		}
	}

	isolatedHome = home

	code := m.Run()

	// The suite is over, so this is the only moment at which the real home can
	// be weighed against the snapshot taken before the pin.
	code = reportRealHome(realBefore.Changed(realBefore.Recheck()), code)

	return code
}

// reportRealHome turns what the guard found in the machine's own home into the
// suite's exit code, and says out loud which of the two answers it is giving.
//
// It is stderr and not t.Log because a TestMain has no *testing.T. That is the
// one point of the design that could not be built as written, and nothing is
// lost by the channel: every change is printed, not summarised.
func reportRealHome(changes []string, code int) int {
	for _, line := range homescan.WarnLines(changes) {
		fmt.Fprintln(os.Stderr, line)
	}

	if homescan.Judge(changes) == homescan.Fail && code == 0 {
		code = 1
	}

	return code
}

// The pin is the primary defence and the strict one: it holds in every mode,
// and neither CI nor BEADLE_TEST_GUARD relaxes it.
func TestTheSuiteIsPinnedToATemporaryHome(t *testing.T) {
	Convey("Given a running suite", t, func() {
		Convey("Then every root-moving variable names the temp home, not the machine's", func() {
			// Cleaned on both sides: TMPDIR arrives from macOS with a trailing
			// separator, so the temp dir and the variable can spell the same
			// directory two ways.
			for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "BEADLE_HOME", "DSH_HOME"} {
				So(filepath.Clean(os.Getenv(name)), ShouldContainSubstring, filepath.Clean(isolatedHome))
			}

			// And the XDG root is the <home>/.config a fallback would resolve
			// to, so a test that computes the path from HOME still matches.
			So(filepath.Clean(os.Getenv("XDG_CONFIG_HOME")), ShouldEqual, filepath.Join(filepath.Clean(isolatedHome), ".config"))
		})

		Convey("And nothing is pinned to an ambient value a runner exported", func() {
			for _, name := range []string{
				"XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME",
				"VERGER_HOME", "DSH_AGENTS_HOME", "PI_CONFIG_DIR",
				"PI_CODING_AGENT_DIR", "OMP_PROFILE", "PI_PROFILE",
				"BEADLE_ALLOW_HOME_MOVE",
			} {
				So(os.Getenv(name), ShouldBeEmpty)
			}
		})

		Convey("And the home the guard weighs is the machine's own, not the pin", func() {
			// The pin and the guard are two different things. Collapsing them
			// would make the guard watch the temp tree and report a clean home
			// no matter what the suite did to the real one.
			So(realHome, ShouldNotEqual, isolatedHome)
		})
	})
}
