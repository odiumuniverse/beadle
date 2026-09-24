package engine_test

import (
	"errors"
	"runtime"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/daemon"
	"github.com/odiumuniverse/beadle/pkg/engine"
)

type daemonRunnerFunc func(name string, args []string, stdin []byte) ([]byte, int, error)

func (f daemonRunnerFunc) Run(name string, args []string, stdin []byte) ([]byte, int, error) {
	return f(name, args, stdin)
}

// writeUnit renders a service unit with the given pinned environment into the
// fixture home.
func writeUnit(t *testing.T, home string, env map[string]string) {
	t.Helper()

	spec := daemon.Spec{
		Binary: "/usr/local/bin/beadle",
		Args:   []string{"watch"},
		Home:   home,
		Env:    env,
	}

	path, content, err := daemon.Render(spec)
	if err != nil {
		t.Fatalf("render unit: %v", err)
	}

	write(t, path, content)
}

func stubDaemonUnit(t *testing.T, f *fixture) {
	t.Helper()

	writeUnit(t, f.home, daemon.UnitEnv(f.home, f.vault.Root()))

	restore := engine.SetDaemonStatusRunnerForTest(daemonRunnerFunc(func(string, []string, []byte) ([]byte, int, error) {
		return []byte("active\n"), 0, nil
	}))
	t.Cleanup(restore)
}

func daemonIssuesOf(issues []engine.Issue) []engine.Issue {
	var out []engine.Issue

	for _, issue := range issues {
		if strings.Contains(issue.Message, "daemon") {
			out = append(out, issue)
		}
	}

	return out
}

func TestDoctorDaemonStates(t *testing.T) {
	Convey("Given a fixture without a daemon unit", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		restore := engine.SetDaemonStatusRunnerForTest(daemonRunnerFunc(func(string, []string, []byte) ([]byte, int, error) {
			return []byte("active\n"), 0, nil
		}))
		t.Cleanup(restore)

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then it warns that the daemon is not installed", func() {
				daemonIssues := daemonIssuesOf(issues)
				So(daemonIssues, ShouldHaveLength, 1)
				So(daemonIssues[0].Severity, ShouldEqual, engine.SeverityWarn)
				So(daemonIssues[0].Message, ShouldContainSubstring, "daemon is not installed")
			})
		})
	})

	Convey("Given an installed but not loaded daemon unit", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, daemon.UnitPath(f.home, daemon.DefaultLabel), "unit\n")

		restore := engine.SetDaemonStatusRunnerForTest(daemonRunnerFunc(func(string, []string, []byte) ([]byte, int, error) {
			return nil, 1, errors.New("service not found")
		}))
		t.Cleanup(restore)

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then it warns that the daemon is installed but not loaded", func() {
				daemonIssues := daemonIssuesOf(issues)
				So(daemonIssues, ShouldHaveLength, 1)
				So(daemonIssues[0].Severity, ShouldEqual, engine.SeverityWarn)
				So(daemonIssues[0].Message, ShouldContainSubstring, "daemon is installed but not loaded")
			})
		})
	})

	Convey("Given an installed and loaded daemon unit", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		stubDaemonUnit(t, f)

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then no daemon issue is reported", func() {
				So(daemonIssuesOf(issues), ShouldBeEmpty)
			})
		})
	})
}

func TestDoctorDaemonEnvPins(t *testing.T) {
	Convey("Given a daemon unit pinned to another home", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		writeUnit(t, f.home, map[string]string{"HOME": "/somewhere/else", "BEADLE_HOME": "/somewhere/else/vault"})

		restore := engine.SetDaemonStatusRunnerForTest(daemonRunnerFunc(func(string, []string, []byte) ([]byte, int, error) {
			return []byte("active\n"), 0, nil
		}))
		t.Cleanup(restore)

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the mismatch warns with the reinstall hint", func() {
				daemonIssues := daemonIssuesOf(issues)
				So(daemonIssues, ShouldHaveLength, 1)
				So(daemonIssues[0].Severity, ShouldEqual, engine.SeverityWarn)
				So(daemonIssues[0].Message, ShouldContainSubstring, "does not match this vault")
				So(daemonIssues[0].Message, ShouldContainSubstring, "beadle daemon install")
			})
		})
	})

	Convey("Given a daemon unit without pinned values", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, daemon.UnitPath(f.home, daemon.DefaultLabel), "unit\n")

		restore := engine.SetDaemonStatusRunnerForTest(daemonRunnerFunc(func(string, []string, []byte) ([]byte, int, error) {
			return []byte("active\n"), 0, nil
		}))
		t.Cleanup(restore)

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the pre-pinning install warns", func() {
				daemonIssues := daemonIssuesOf(issues)
				So(daemonIssues, ShouldHaveLength, 1)
				So(daemonIssues[0].Message, ShouldContainSubstring, "pins no environment")
			})
		})
	})

	// Homebrew candidates exist on macOS only: on Linux daemon.Check never
	// looks for the brew labels.
	if runtime.GOOS == "darwin" {
		Convey("Given a Homebrew-managed daemon unit", t, func() {
			t.Setenv("XDG_CONFIG_HOME", "")

			f := newFixture(t)
			f.emptyConfigs(t)

			write(t, daemon.UnitPath(f.home, "sh.brew.beadle"), "unit\n")

			restore := engine.SetDaemonStatusRunnerForTest(daemonRunnerFunc(func(string, []string, []byte) ([]byte, int, error) {
				return []byte("active\n"), 0, nil
			}))
			t.Cleanup(restore)

			Convey("When doctor runs", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)

				Convey("Then the foreign unit is not checked for pinned values", func() {
					So(daemonIssuesOf(issues), ShouldBeEmpty)
				})
			})
		})
	}
}
