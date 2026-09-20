package cli

import (
	"context"
	"os"
	"runtime"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/daemon"
)

func fakeDaemonRunner(t *testing.T, run daemon.Runner) {
	t.Helper()

	previous := daemonInstallRunner
	daemonInstallRunner = run

	t.Cleanup(func() { daemonInstallRunner = previous })
}

func daemonUnitPath(t *testing.T, home string) string {
	t.Helper()

	return daemon.UnitPath(home, daemon.DefaultLabel)
}

func unitFileExists(t *testing.T, path string) bool {
	t.Helper()

	_, err := os.Stat(path)

	return err == nil
}

func TestInitDaemonInstallsWatcher(t *testing.T) {
	Convey("Given a fresh HOME and a fake service runner", t, func() {
		home := gwsHome(t)

		var calls [][]string

		fakeDaemonRunner(t, func(_ context.Context, name string, args ...string) error {
			calls = append(calls, append([]string{name}, args...))

			return nil
		})

		Convey("When init --daemon runs", func() {
			out, err := gwsRun(t, "init", "--daemon")
			So(err, ShouldBeNil)

			Convey("Then the unit file exists and the service was registered", func() {
				So(unitFileExists(t, daemonUnitPath(t, home)), ShouldBeTrue)
				So(out, ShouldContainSubstring, "daemon: installed and started")
				So(calls, ShouldNotBeEmpty)

				if runtime.GOOS == "darwin" {
					So(calls[0][1], ShouldEqual, "bootstrap")
					So(calls[0][3], ShouldEqual, daemonUnitPath(t, home))
				}
			})
		})
	})
}

func TestInitDaemonIsIdempotent(t *testing.T) {
	Convey("Given a fake service runner", t, func() {
		home := gwsHome(t)

		runs := 0

		fakeDaemonRunner(t, func(context.Context, string, ...string) error {
			runs++

			return nil
		})

		Convey("When init --daemon runs twice", func() {
			_, err := gwsRun(t, "init", "--daemon")
			So(err, ShouldBeNil)

			first := runs
			So(first, ShouldBeGreaterThan, 0)

			out, err := gwsRun(t, "init", "--daemon")
			So(err, ShouldBeNil)

			Convey("Then it re-registers without an error", func() {
				So(runs, ShouldBeGreaterThan, first)
				So(unitFileExists(t, daemonUnitPath(t, home)), ShouldBeTrue)
				So(out, ShouldContainSubstring, "daemon: installed and started")
			})
		})
	})
}

func TestInitDaemonFlags(t *testing.T) {
	Convey("Given a fresh HOME and a fake service runner", t, func() {
		home := gwsHome(t)

		calls := 0

		fakeDaemonRunner(t, func(context.Context, string, ...string) error {
			calls++

			return nil
		})

		Convey("When init runs with --no-daemon", func() {
			out, err := gwsRun(t, "init", "--no-daemon")
			So(err, ShouldBeNil)

			Convey("Then nothing is installed and no hint is printed", func() {
				So(unitFileExists(t, daemonUnitPath(t, home)), ShouldBeFalse)
				So(calls, ShouldEqual, 0)
				So(out, ShouldNotContainSubstring, "daemon install")
				So(out, ShouldNotContainSubstring, "installed and started")
			})
		})

		Convey("When init runs without flags", func() {
			out, err := gwsRun(t, "init")
			So(err, ShouldBeNil)

			Convey("Then it hints at daemon install without installing", func() {
				So(unitFileExists(t, daemonUnitPath(t, home)), ShouldBeFalse)
				So(calls, ShouldEqual, 0)
				So(out, ShouldContainSubstring, "beadle daemon install")
			})
		})

		Convey("When both flags are passed", func() {
			_, err := gwsRun(t, "init", "--daemon", "--no-daemon")

			Convey("Then it is rejected", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "--daemon")
			})
		})
	})
}
