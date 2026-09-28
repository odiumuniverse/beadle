package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
)

// ompDoctorEnv clears the omp root and profile environment for a doctor test:
// a developer's PI_CONFIG_DIR/OMP_PROFILE must not decide where the reported
// root lands.
func ompDoctorEnv(t *testing.T) {
	t.Helper()

	for _, name := range []string{"PI_CONFIG_DIR", "PI_CODING_AGENT_DIR", "OMP_PROFILE", "PI_PROFILE"} {
		if value, ok := os.LookupEnv(name); ok {
			t.Setenv(name, value)
		}

		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
	}
}

// ompDoctorIssue finds the omp doctor issue carrying the severity and message
// substring.
func ompDoctorIssue(issues []engine.Issue, severity, substring string) *engine.Issue {
	for i := range issues {
		if issues[i].Agent == agent.OmpID && issues[i].Severity == severity &&
			strings.Contains(issues[i].Message, substring) {
			return &issues[i]
		}
	}

	return nil
}

func TestOmpDoctorHealthySystem(t *testing.T) {
	Convey("Given a detected omp install with one named profile", t, func() {
		ompDoctorEnv(t)

		f := newFixture(t)

		write(t, filepath.Join(f.home, ".omp", "agent", "config.yml"), "setupVersion: 2\n")

		So(os.MkdirAll(filepath.Join(f.home, ".omp", "profiles", "work"), 0o750), ShouldBeNil)

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the resolved root, agent dir and profile are reported and nothing warns", func() {
				issue := ompDoctorIssue(issues, engine.SeverityInfo, "oh-my-pi: home ")
				So(issue, ShouldNotBeNil)
				// displayHomePath abbreviates anything under the home.
				So(issue.Message, ShouldContainSubstring, "~/.omp")
				So(issue.Message, ShouldContainSubstring, "~/.omp/agent")
				So(issue.Message, ShouldContainSubstring, "profile default")
				So(issue.Message, ShouldContainSubstring, "profiles 1")

				for _, reported := range issues {
					if reported.Agent == agent.OmpID {
						So(reported.Severity, ShouldNotEqual, engine.SeverityWarn)
					}
				}
			})
		})
	})

	Convey("Given an active named omp profile", t, func() {
		ompDoctorEnv(t)
		t.Setenv("OMP_PROFILE", "work")

		f := newFixture(t)

		write(t, filepath.Join(f.home, ".omp", "agent", "config.yml"), "setupVersion: 2\n")

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the profile and its agent dir are named", func() {
				issue := ompDoctorIssue(issues, engine.SeverityInfo, "oh-my-pi: home ")
				So(issue, ShouldNotBeNil)
				So(issue.Message, ShouldContainSubstring, "profile work")
				So(issue.Message, ShouldContainSubstring, "~/.omp/profiles/work/agent")
				So(issue.Message, ShouldNotContainSubstring, "~/.omp/agent;")
			})
		})
	})

	Convey("Given an omp binary on PATH but no config yet", t, func() {
		ompDoctorEnv(t)

		f := newFixture(t)

		bin := t.TempDir()

		So(os.WriteFile(filepath.Join(bin, "omp"), []byte("#!/bin/sh\nexit 0\n"), 0o700), ShouldBeNil) //nolint:gosec // G306: the fake omp has to be executable for LookPath
		t.Setenv("PATH", bin)

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the binary-only state is named and still does not warn", func() {
				issue := ompDoctorIssue(issues, engine.SeverityInfo, "binary on PATH only")
				So(issue, ShouldNotBeNil)
				So(issue.Message, ShouldContainSubstring, "no config.yml yet")
				So(ompDoctorIssue(issues, engine.SeverityWarn, "oh-my-pi"), ShouldBeNil)
			})
		})
	})

	Convey("Given a home without omp", t, func() {
		ompDoctorEnv(t)

		f := newFixture(t)

		// omp is also detected by its binary: keep a developer's install out
		// of PATH so the missing-host branch is what the test observes.
		t.Setenv("PATH", "/usr/bin:/bin")

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then omp is only an info line", func() {
				So(ompDoctorIssue(issues, engine.SeverityInfo, "oh-my-pi: not found"), ShouldNotBeNil)
			})
		})
	})
}
