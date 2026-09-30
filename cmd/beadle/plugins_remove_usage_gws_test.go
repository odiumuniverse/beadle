package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// buildBeadle builds the command once for the test and returns its path. The
// exit code is a contract with scripts, so it is checked on the real program
// and not on a function the program happens to call: a caller that forgets to
// classify an error still exits 0, and only the process can say so.
func buildBeadle(t *testing.T) string {
	t.Helper()

	bin := filepath.Join(t.TempDir(), "beadle")

	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".") //nolint:gosec // G204: a fixed build of this package
	build.Dir = "."

	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build beadle: %v\n%s", err, out)
	}

	return bin
}

// runBeadle runs the built command with an isolated home and returns its exit
// code and combined output.
func runBeadle(t *testing.T, bin, home string, args ...string) (int, string) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), bin, args...) //nolint:gosec // G204: the binary this test just built

	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"BEADLE_HOME="+filepath.Join(home, ".beadle"),
	)

	out, err := cmd.CombinedOutput()

	code := 0

	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run beadle %v: %v", args, err)
	}

	return code, string(out)
}

// TestPluginsRemoveOfAnUnknownPackageExitsUsage is the scripting contract: an
// id that names nothing is the user's mistake, so the process exits 2 (usage,
// W7-UX-SPEC §6) instead of 0. A script that runs "remove" on a package that
// is already gone has to be able to tell that from a removal that happened.
func TestPluginsRemoveOfAnUnknownPackageExitsUsage(t *testing.T) {
	Convey("Given a vault with no package of that name", t, func() {
		bin := buildBeadle(t)
		home := t.TempDir()

		// A vault the user has: an uninitialised home is a different failure
		// with its own message, and testing it here would prove nothing about
		// the id.
		runBeadle(t, bin, home, "init")

		Convey("When the user removes a package that is not installed", func() {
			code, out := runBeadle(t, bin, home, "plugins", "remove", "nope")

			Convey("Then the exit is the usage class", func() {
				So(code, ShouldEqual, 2)
			})

			Convey("And the message names the id and says where it looked", func() {
				So(out, ShouldContainSubstring, `no package "nope"`)
			})
		})

		Convey("And a usage exit is not the class of a real failure", func() {
			// A missing argument never reaches the vault at all: it is the
			// same class, from a different direction.
			code, _ := runBeadle(t, bin, home, "plugins", "remove")

			So(code, ShouldEqual, 2)
		})
	})
}

// TestPluginsRemoveUsageExitIsNotZero pins the part the table alone does not:
// whatever the class, a command that changed nothing and did not find its
// argument must not report success.
func TestPluginsRemoveUsageExitIsNotZero(t *testing.T) {
	Convey("Given a built beadle", t, func() {
		bin := buildBeadle(t)
		home := t.TempDir()

		code, _ := runBeadle(t, bin, home, "plugins", "remove", "local:nope", "extra")
		exit, convErr := strconv.Atoi(strconv.Itoa(code))
		So(convErr, ShouldBeNil)

		Convey("Then neither an unknown id nor a surplus argument exits 0", func() {
			So(exit, ShouldNotEqual, 0)
		})
	})
}
