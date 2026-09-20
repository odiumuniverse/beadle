package engine_test

import (
	"errors"
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

func stubDaemonUnit(t *testing.T, f *fixture) {
	t.Helper()

	write(t, daemon.UnitPath(f.home, daemon.DefaultLabel), "unit\n")

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
