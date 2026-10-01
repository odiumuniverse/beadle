package vergerx_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/homescan"
)

// This package opens plugin homes, and a plugin home is resolved from the
// environment. Without a pin, a client opened on a temporary vault falls through
// to the machine's REAL ~/.verger and writes there: the suite left a
// watch.lease in the developer's home, with a pid from a finished test run.
//
// The pin is the same one pkg/agent, pkg/cli and pkg/engine use, plus VERGER_HOME:
// discovery's FIRST rule is that variable, so a developer who has it set would
// otherwise have every test in this package read and write their own home while
// the tests pass.
var (
	isolatedHome string
	realHome     string
	realBefore   homescan.Snapshot
)

func TestMain(m *testing.M) {
	os.Exit(isolateTestHome(m))
}

func isolateTestHome(m *testing.M) int {
	realHome = homescan.GuardHome()

	realBefore = homescan.Take(realHome)

	home, err := os.MkdirTemp("", "beadle-vergerx-test-home") //nolint:usetesting // TestMain has no *testing.T
	if err != nil {
		return 1
	}

	defer func() { _ = os.RemoveAll(home) }()

	for name, value := range map[string]string{
		"HOME":            home,
		"BEADLE_HOME":     filepath.Join(home, ".beadle"),
		"VERGER_HOME":     "",
		"XDG_CONFIG_HOME": "",
		"XDG_DATA_HOME":   "",
		"XDG_CACHE_HOME":  "",
		"XDG_STATE_HOME":  "",
	} {
		//nolint:usetesting // TestMain cannot use t.Setenv; tests override per test
		if err := os.Setenv(name, value); err != nil {
			return 1
		}
	}

	isolatedHome = home

	code := m.Run()

	// The suite is over, so this is the only moment at which the real home can
	// be weighed against the snapshot taken before the pin. Inside a test the
	// check runs in file order, and in pkg/engine alone 93 test files sort
	// after this one: it would report a clean home while two thirds of the
	// suite had yet to run.
	code = reportRealHome(realBefore.Changed(realBefore.Recheck()), code)

	return code
}

// TestSuiteHomeIsolation is the guard the whole arrangement exists for: after a
// run, the machine's real home must hold nothing this suite created. A test that
// only checked "HOME is pinned" would pass with the suite happily writing to
// ~/.verger through a home it resolved before the pin.
func TestSuiteHomeIsolation(t *testing.T) {
	// Only the pins are checked here. Whether the suite left the machine's real home
	// untouched is realHomeUnchanged, run by TestMain after m.Run(): a check made
	// in a test would only weigh the files that happen to sort before it.
	Convey("Given a suite that pinned the home", t, func() {
		Convey("Then the process is running inside the pin", func() {
			So(isolatedHome, ShouldNotBeEmpty)

			home, err := os.UserHomeDir()
			So(err, ShouldBeNil)
			So(filepath.Clean(home), ShouldEqual, filepath.Clean(isolatedHome))

			for _, name := range []string{"VERGER_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME"} {
				So(os.Getenv(name), ShouldBeEmpty)
			}
		})
	})
}

// reportRealHome turns what the guard found in the machine's own home into the
// suite's exit code, and says out loud which of the two answers it is giving.
//
// The weighing stays in TestMain rather than moving into a test file. A test
// runs in file order, and -run or -shuffle can leave a real-home test as the
// only one that runs at all — which would report a clean home over a suite of
// zero, the one case where a guard must not be trusted. Here the snapshot is
// taken before the first test and weighed after the last, so a filtered run is
// weighed too.
//
// It is stderr and not t.Log because a TestMain has no *testing.T. That is the
// one point of the design that could not be built as written, and nothing is
// lost by the channel: every change is printed, not summarised.
func reportRealHome(changes []string, code int) int {
	switch homescan.Judge(changes) {
	case homescan.Clean:
		return code
	case homescan.Warn:
		homescan.WarnAbout(changes)

		return code
	case homescan.Fail:
		homescan.FailAbout(changes)

		return 1
	default:
		return code
	}
}

// The pin is the primary defence and the strict one: it holds in every mode,
// and neither CI nor BEADLE_TEST_GUARD relaxes it. The guard above is
// deliberately lenient on a developer machine, so if this ever stopped being
// true there would be nothing left to notice.
func TestTheSuiteIsPinnedToATemporaryHome(t *testing.T) {
	Convey("Given the home this suite runs against", t, func() {
		envHome := os.Getenv("HOME")

		Convey("Then both ways of asking for a home name it", func() {
			// os.UserHomeDir is not a synonym for $HOME on every platform, and
			// code that derives a home does not always go through the
			// environment. Both are asked, because both are ways out.
			dir, err := os.UserHomeDir()
			So(err, ShouldBeNil)
			So(dir, ShouldEqual, envHome)
			So(envHome, ShouldEqual, isolatedHome)
		})

		Convey("And it is a temporary directory rather than a home on this machine", func() {
			So(envHome, ShouldNotEqual, realHome)

			rel, err := filepath.Rel(os.TempDir(), envHome)
			So(err, ShouldBeNil)
			So(rel, ShouldNotStartWith, "..")
		})
	})
}
