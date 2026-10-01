package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/daemon"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
)

// withDaemonRunner returns the suite's options with the install call replaced by
// run. It is an options value, not a package variable, so a test that drives
// the service installer cannot change what another test's run does — which is
// what made this suite order-dependent under `go test -count=3`.
func withDaemonRunner(run daemon.Runner) Options {
	opts := testOptions()
	opts.DaemonInstall = run

	return opts
}

// withTemporaryHome returns the suite's options with the temporary-root check
// answering `temporary`: the CLI tests run in a temp home, so the install path
// needs the gate lifted. It is an options value, not a package variable — the
// old helper assigned a global and restored it in a cleanup, so under
// `go test -count=3` a later iteration, and anything running meanwhile, saw
// whichever value happened to be left behind.
func withTemporaryHome(temporary bool) Options {
	opts := testOptions()
	opts.DaemonTemporaryHome = func(string) bool { return temporary }

	return opts
}

// daemonOpts is the pair the daemon tests need in one value: the temporary-root
// check and the install recorder. Composing them here means a test cannot set
// one and leave the other at the suite default by accident.
func daemonOpts(temporary bool, run daemon.Runner) Options {
	opts := withTemporaryHome(temporary)
	opts.DaemonInstall = run

	return opts
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

		opts := daemonOpts(false, func(_ context.Context, name string, args ...string) error {
			calls = append(calls, append([]string{name}, args...))

			return nil
		})

		Convey("When init --daemon runs", func() {
			out, err := gwsRunOpts(t, opts, nil, "init", "--daemon")
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

		opts := daemonOpts(false, func(context.Context, string, ...string) error {
			runs++

			return nil
		})

		Convey("When init --daemon runs twice", func() {
			_, err := gwsRunOpts(t, opts, nil, "init", "--daemon")
			So(err, ShouldBeNil)

			first := runs
			So(first, ShouldBeGreaterThan, 0)

			out, err := gwsRunOpts(t, opts, nil, "init", "--daemon")
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

		opts := daemonOpts(false, func(context.Context, string, ...string) error {
			calls++

			return nil
		})

		Convey("When init runs with --no-daemon", func() {
			out, err := gwsRunOpts(t, opts, nil, "init", "--no-daemon")
			So(err, ShouldBeNil)

			Convey("Then nothing is installed and no hint is printed", func() {
				So(unitFileExists(t, daemonUnitPath(t, home)), ShouldBeFalse)
				So(calls, ShouldEqual, 0)
				So(out, ShouldContainSubstring, "daemon: skipped (--no-daemon)")
				So(out, ShouldNotContainSubstring, "daemon install")
				So(out, ShouldNotContainSubstring, "installed and started")
			})
		})

		Convey("When init runs without flags", func() {
			out, err := gwsRunOpts(t, opts, nil, "init")
			So(err, ShouldBeNil)

			Convey("Then the watcher is installed by default", func() {
				So(unitFileExists(t, daemonUnitPath(t, home)), ShouldBeTrue)
				So(calls, ShouldBeGreaterThan, 0)
				So(out, ShouldContainSubstring, "daemon: installed and started")
				So(out, ShouldNotContainSubstring, "beadle daemon install")
			})
		})

		Convey("When both flags are passed", func() {
			_, err := gwsRunOpts(t, opts, nil, "init", "--daemon", "--no-daemon")

			Convey("Then it is rejected", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "--daemon")
			})
		})
	})
}

func TestInitDaemonSkipsTemporaryHome(t *testing.T) {
	Convey("Given a temporary home and the real temporary check", t, func() {
		home := gwsHome(t)

		calls := 0

		opts := withDaemonRunner(func(context.Context, string, ...string) error {
			calls++

			return nil
		})

		_, err := gwsRunOpts(t, opts, nil, "init", "--no-daemon")
		So(err, ShouldBeNil)

		Convey("When init runs without daemon flags", func() {
			out, err := gwsRunOpts(t, opts, nil, "init")
			So(err, ShouldBeNil)

			Convey("Then the watcher is skipped with a note", func() {
				So(unitFileExists(t, daemonUnitPath(t, home)), ShouldBeFalse)
				So(calls, ShouldEqual, 0)
				So(out, ShouldContainSubstring, "daemon: skipped (temporary home)")
				So(out, ShouldNotContainSubstring, "beadle daemon install")
			})
		})

		Convey("When init --daemon runs", func() {
			_, err := gwsRunOpts(t, opts, nil, "init", "--daemon")

			Convey("Then the explicit request is refused with the init hint", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "temporary")
				So(err.Error(), ShouldContainSubstring, "--no-daemon")
				So(calls, ShouldEqual, 0)
			})
		})

		Convey("When daemon install runs", func() {
			_, err := gwsRunOpts(t, opts, nil, "daemon", "install")

			Convey("Then it is refused without offering the init-only flag", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "temporary")
				So(err.Error(), ShouldContainSubstring, "use a permanent HOME/BEADLE_HOME")
				So(err.Error(), ShouldNotContainSubstring, "--no-daemon")
				So(calls, ShouldEqual, 0)
			})
		})
	})
}

func TestInitDaemonPinsEnv(t *testing.T) {
	Convey("Given a permanent home and a fake service runner", t, func() {
		home := gwsHome(t)
		opts := daemonOpts(false, func(context.Context, string, ...string) error { return nil })

		Convey("When init --daemon runs with a custom vault", func() {
			t.Setenv("BEADLE_HOME", filepath.Join(home, "vault-elsewhere"))

			_, err := gwsRunOpts(t, opts, nil, "init", "--daemon")
			So(err, ShouldBeNil)

			Convey("Then the unit pins the home, the vault and PATH", func() {
				env, err := daemon.UnitEnvFromFile(daemonUnitPath(t, home))
				So(err, ShouldBeNil)
				So(env["HOME"], ShouldEqual, fsutil.Root(home))
				So(env["BEADLE_HOME"], ShouldEqual, fsutil.Root(home)+"/vault-elsewhere")
				So(env["PATH"], ShouldNotBeEmpty)
			})
		})

		Convey("When init --daemon runs with the default vault", func() {
			_, err := gwsRunOpts(t, opts, nil, "init", "--daemon")
			So(err, ShouldBeNil)

			Convey("Then BEADLE_HOME stays implicit and HOME is pinned", func() {
				env, err := daemon.UnitEnvFromFile(daemonUnitPath(t, home))
				So(err, ShouldBeNil)
				So(env["HOME"], ShouldEqual, fsutil.Root(home))
				So(env, ShouldNotContainKey, "BEADLE_HOME")
			})
		})
	})
}

func TestDaemonInstallRewritesEnv(t *testing.T) {
	Convey("Given an installed unit with an outdated environment", t, func() {
		home := gwsHome(t)
		opts := daemonOpts(false, func(context.Context, string, ...string) error { return nil })

		_, err := gwsRunOpts(t, opts, nil, "init", "--no-daemon")
		So(err, ShouldBeNil)

		_, err = gwsRunOpts(t, opts, nil, "daemon", "install")
		So(err, ShouldBeNil)

		before, err := daemon.UnitEnvFromFile(daemonUnitPath(t, home))
		So(err, ShouldBeNil)
		So(before, ShouldNotContainKey, "XDG_CONFIG_HOME")

		Convey("When the environment changes and install runs again", func() {
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-new"))

			_, err := gwsRunOpts(t, opts, nil, "daemon", "install")
			So(err, ShouldBeNil)

			Convey("Then the unit carries the new value and stays a single file", func() {
				after, err := daemon.UnitEnvFromFile(daemonUnitPath(t, home))
				So(err, ShouldBeNil)
				So(after["XDG_CONFIG_HOME"], ShouldEqual, filepath.Join(home, "xdg-new"))
				So(after["HOME"], ShouldEqual, home)
				So(unitFileExists(t, daemonUnitPath(t, home)), ShouldBeTrue)
			})
		})
	})
}

func TestInitDaemonSkipsExistingService(t *testing.T) {
	Convey("Given an existing service file", t, func() {
		home := gwsHome(t)
		calls := 0

		opts := daemonOpts(false, func(context.Context, string, ...string) error {
			calls++

			return nil
		})

		path := daemonUnitPath(t, home)
		So(os.MkdirAll(filepath.Dir(path), 0o750), ShouldBeNil)
		So(os.WriteFile(path, []byte("plist"), 0o600), ShouldBeNil)

		Convey("When init runs", func() {
			out, err := gwsRunOpts(t, opts, nil, "init")
			So(err, ShouldBeNil)

			Convey("Then the existing watcher is left alone", func() {
				So(calls, ShouldEqual, 0)
				So(out, ShouldContainSubstring, "daemon: already installed")
				So(out, ShouldNotContainSubstring, "installed and started")
			})
		})
	})
}
