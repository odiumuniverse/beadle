package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

// withoutDSHHome unsets DSH_HOME for the test: t.Setenv cannot express
// "unset", and the machine running the tests may have it set.
func withoutDSHHome(t *testing.T) {
	t.Helper()

	if value, ok := os.LookupEnv("DSH_HOME"); ok {
		// t.Setenv restores the original value once the test ends.
		t.Setenv("DSH_HOME", value)
	}

	if err := os.Unsetenv("DSH_HOME"); err != nil {
		t.Fatalf("unset DSH_HOME: %v", err)
	}
}

// dshIssue finds the DSH issue carrying the severity and message substring.
func dshIssue(issues []engine.Issue, severity, substring string) *engine.Issue {
	for i := range issues {
		if issues[i].Severity == severity && strings.Contains(issues[i].Message, substring) {
			return &issues[i]
		}
	}

	return nil
}

func TestDSHDoctorIssues(t *testing.T) {
	Convey("Given a home without DSH", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("PATH", "/usr/bin:/bin")

		withoutDSHHome(t)

		f := newFixture(t)
		f.emptyConfigs(t)

		Convey("When the doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())

			Convey("Then the missing harness is an info line, not an error", func() {
				So(err, ShouldBeNil)

				issue := dshIssue(issues, engine.SeverityInfo, "DSH: not found")
				So(issue, ShouldNotBeNil)
				So(issue.Agent, ShouldEqual, agent.DSHID)
				So(issue.Kind, ShouldEqual, kind.ID(""))

				So(hasIssue(issues, engine.SeverityWarn, agent.DSHHomeNote), ShouldBeFalse)
			})
		})
	})

	Convey("Given a detected DSH home", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("PATH", "/usr/bin:/bin")

		withoutDSHHome(t)

		f := newFixture(t)
		f.emptyConfigs(t)

		dsh := filepath.Join(f.home, ".dsh")

		write(t, filepath.Join(dsh, "AGENTS.md"), "# harness\n")
		write(t, filepath.Join(dsh, "skills", "alpha", "SKILL.md"), "# alpha\n")
		So(os.MkdirAll(filepath.Join(dsh, "profiles", "default"), 0o750), ShouldBeNil)
		So(os.MkdirAll(filepath.Join(dsh, "profiles", "headless"), 0o750), ShouldBeNil)

		Convey("When the doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())

			Convey("Then the read paths and the profile count are reported", func() {
				So(err, ShouldBeNil)

				rules := dshIssue(issues, engine.SeverityInfo, "rules ~/.dsh/AGENTS.md")
				So(rules, ShouldNotBeNil)
				So(rules.Agent, ShouldEqual, agent.DSHID)
				So(rules.Kind, ShouldEqual, kind.Rules)

				skills := dshIssue(issues, engine.SeverityInfo, "skills ~/.dsh/skills; profiles 2")
				So(skills, ShouldNotBeNil)
				So(skills.Agent, ShouldEqual, agent.DSHID)
				So(skills.Kind, ShouldEqual, kind.Skills)

				So(hasIssue(issues, engine.SeverityInfo, "DSH: not found"), ShouldBeFalse)
			})
		})
	})

	Convey("Given an empty DSH_HOME", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("PATH", "/usr/bin:/bin")
		t.Setenv("DSH_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		dsh := filepath.Join(f.home, ".dsh")

		write(t, filepath.Join(dsh, "AGENTS.md"), "# harness\n")
		So(os.MkdirAll(filepath.Join(dsh, "profiles", "default"), 0o750), ShouldBeNil)

		Convey("When the doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())

			Convey("Then the empty value warns and the fallback home is reported", func() {
				So(err, ShouldBeNil)

				warn := dshIssue(issues, engine.SeverityWarn, agent.DSHHomeNote)
				So(warn, ShouldNotBeNil)
				So(warn.Agent, ShouldEqual, agent.DSHID)

				So(hasIssue(issues, engine.SeverityInfo, "skills ~/.dsh/skills; profiles 1"), ShouldBeTrue)
			})
		})
	})
}

func TestDSHSharedSkillsNotShadowed(t *testing.T) {
	Convey("Given DSH_HOME pointing at the shared skills directory", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("PATH", "/usr/bin:/bin")

		f := newFixture(t)
		f.emptyConfigs(t)

		f.enableAgent(t, agent.DSHID)
		f.enableAgent(t, agent.SharedID)

		t.Setenv("DSH_HOME", filepath.Join(f.home, ".agents"))

		// The adapter resolves DSH_HOME when the agents are built, so the
		// engine is rebuilt once the environment is in place.
		e, err := engine.New(f.vault, f.config, agent.All(f.home, t.TempDir()), engine.WithHome(f.home))
		So(err, ShouldBeNil)

		// The shared directory pre-exists, so both surfaces are present and
		// the owners dedup has to pick one.
		So(os.MkdirAll(filepath.Join(f.home, ".agents", "skills"), 0o750), ShouldBeNil)

		write(t, filepath.Join(f.vault.SkillsDir(), "beta", "SKILL.md"),
			"---\nname: beta\ndescription: canon skill\n---\n\n# beta\n")

		report, err := e.Sync(t.Context(), engine.SyncOptions{})
		So(err, ShouldBeNil)
		So(report.Errors(), ShouldBeEmpty)

		Convey("When the sync runs", func() {
			Convey("Then the canon skill is delivered and the shared surface is not an alias", func() {
				So(read(t, filepath.Join(f.home, ".agents", "skills", "beta", "SKILL.md")), ShouldContainSubstring, "# beta")

				kr := report.Kind(kind.Skills)
				So(kr, ShouldNotBeNil)

				shared := 0

				for _, result := range kr.Agents {
					if result.Agent != agent.SharedID {
						continue
					}

					shared++

					So(result.Action, ShouldNotEqual, engine.ActionAlias)
				}

				So(shared, ShouldBeGreaterThan, 0)
			})
		})
	})
}

func TestDSHDoctorSkipsWithoutHome(t *testing.T) {
	Convey("Given an engine without a home directory", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("PATH", "/usr/bin:/bin")

		withoutDSHHome(t)

		cwd := t.TempDir()

		So(os.MkdirAll(filepath.Join(cwd, ".dsh"), 0o750), ShouldBeNil)

		// Without a home the relative ".dsh" fallback would resolve against
		// the process working directory, so the test moves there.
		t.Chdir(cwd)

		v := vault.New(filepath.Join(t.TempDir(), "vault"))
		So(v.Init(), ShouldBeNil)

		cfg, err := config.Load(v.ConfigPath())
		So(err, ShouldBeNil)

		e, err := engine.New(v, cfg, agent.All(t.TempDir(), cwd))
		So(err, ShouldBeNil)

		Convey("When the DSH issues are collected", func() {
			Convey("Then no home means no DSH lines at all", func() {
				So(e.DSHIssues(), ShouldBeEmpty)
			})
		})
	})
}
