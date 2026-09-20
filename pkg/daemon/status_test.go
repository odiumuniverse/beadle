package daemon_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/daemon"
)

type statusRunnerFunc func(name string, args []string, stdin []byte) ([]byte, int, error)

func (f statusRunnerFunc) Run(name string, args []string, stdin []byte) ([]byte, int, error) {
	return f(name, args, stdin)
}

func writeUnit(t *testing.T, path string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("unit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUnitPathMatchesRender(t *testing.T) {
	Convey("Given a daemon spec", t, func() {
		home := t.TempDir()
		spec := daemon.Spec{Binary: "/bin/beadle", Args: []string{"watch"}, Home: home}

		Convey("When the unit path is resolved", func() {
			renderPath, _, err := daemon.Render(spec)
			So(err, ShouldBeNil)

			Convey("Then it matches what Render writes", func() {
				So(daemon.UnitPath(home, daemon.DefaultLabel), ShouldEqual, renderPath)
			})
		})
	})
}

func TestCheckStates(t *testing.T) {
	loaded := statusRunnerFunc(func(string, []string, []byte) ([]byte, int, error) {
		return []byte("active\n"), 0, nil
	})

	Convey("Given no unit file", t, func() {
		status, err := daemon.Check(t.TempDir(), daemon.DefaultLabel, loaded)

		Convey("When it is checked", func() {
			Convey("Then it is not installed", func() {
				So(err, ShouldBeNil)
				So(status.Installed, ShouldBeFalse)
				So(status.Loaded, ShouldBeFalse)
			})
		})
	})

	Convey("Given an installed unit whose manager reports failure", t, func() {
		home := t.TempDir()
		writeUnit(t, daemon.UnitPath(home, daemon.DefaultLabel))

		status, err := daemon.Check(home, daemon.DefaultLabel, statusRunnerFunc(func(string, []string, []byte) ([]byte, int, error) {
			return nil, 1, errors.New("service not found")
		}))

		Convey("When it is checked", func() {
			Convey("Then it is installed but not loaded", func() {
				So(err, ShouldBeNil)
				So(status.Installed, ShouldBeTrue)
				So(status.Loaded, ShouldBeFalse)
				So(status.Path, ShouldEqual, daemon.UnitPath(home, daemon.DefaultLabel))
			})
		})
	})

	Convey("Given an installed and loaded unit", t, func() {
		home := t.TempDir()
		writeUnit(t, daemon.UnitPath(home, daemon.DefaultLabel))

		status, err := daemon.Check(home, daemon.DefaultLabel, loaded)

		Convey("When it is checked", func() {
			Convey("Then it is installed and loaded", func() {
				So(err, ShouldBeNil)
				So(status.Installed, ShouldBeTrue)
				So(status.Loaded, ShouldBeTrue)
			})
		})
	})

	if runtime.GOOS == "darwin" {
		Convey("Given a Homebrew-managed unit", t, func() {
			home := t.TempDir()
			writeUnit(t, daemon.UnitPath(home, "homebrew.mxcl.beadle"))

			status, err := daemon.Check(home, daemon.DefaultLabel, loaded)

			Convey("When it is checked", func() {
				Convey("Then it counts as installed and loaded", func() {
					So(err, ShouldBeNil)
					So(status.Installed, ShouldBeTrue)
					So(status.Loaded, ShouldBeTrue)
				})
			})
		})
	}
}

func TestInstallRetriesLoadedLaunchdService(t *testing.T) {
	if runtime.GOOS != "darwin" {
		return
	}

	Convey("Given a launchd service that is already loaded", t, func() {
		home := t.TempDir()
		spec := daemon.Spec{Binary: "/bin/beadle", Args: []string{"watch"}, Home: home}

		bootstraps := 0
		bootouts := 0

		run := func(_ context.Context, _ string, args ...string) error {
			switch {
			case len(args) > 0 && args[0] == "bootstrap":
				bootstraps++
				if bootstraps == 1 {
					return errors.New("service already loaded")
				}
			case len(args) > 0 && args[0] == "bootout":
				bootouts++
			}

			return nil
		}

		Convey("When the service is installed", func() {
			_, err := daemon.Install(t.Context(), spec, run)
			So(err, ShouldBeNil)

			Convey("Then it bootstraps again after bootout", func() {
				So(bootstraps, ShouldEqual, 2)
				So(bootouts, ShouldEqual, 1)
			})
		})
	})
}
