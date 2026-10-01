package vergerx_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
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
	realBefore   []string
)

func TestMain(m *testing.M) {
	os.Exit(isolateTestHome(m))
}

func isolateTestHome(m *testing.M) int {
	realHome = os.Getenv("HOME")

	// What the real home held before the suite: the guard compares against it.
	if entries, err := os.ReadDir(realHome); err == nil {
		for _, entry := range entries {
			realBefore = append(realBefore, entry.Name())
		}
	}

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
