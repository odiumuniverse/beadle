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

func shadowIssues(t *testing.T, f *fixture) []engine.Issue {
	t.Helper()

	issues, err := f.engine.Doctor(t.Context())
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}

	return issues
}

func findShadowIssue(t *testing.T, issues []engine.Issue, name string) engine.Issue {
	t.Helper()

	for _, issue := range issues {
		if strings.Contains(issue.Message, "skill "+name+" differs between") {
			return issue
		}
	}

	t.Fatalf("no shadow issue for %s: %v", name, issues)

	return engine.Issue{}
}

func TestSkillShadowWarnsOnDivergentContent(t *testing.T) {
	Convey("Given the same skill differing between two directories", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		cursorHost(t, f)

		write(t, filepath.Join(f.home, ".cursor", "skills", "alpha", "SKILL.md"), "# one\n")
		write(t, f.sharedSkill("alpha"), "# two\n")

		issue := findShadowIssue(t, shadowIssues(t, f), "alpha")

		Convey("When doctor runs", func() {
			Convey("Then a warning names both directories", func() {
				So(issue.Severity, ShouldEqual, engine.SeverityWarn)
				So(issue.Agent, ShouldEqual, agent.CursorID)
				So(issue.Message, ShouldContainSubstring, "~/.cursor/skills")
				So(issue.Message, ShouldContainSubstring, "~/.agents/skills")
				So(issue.Message, ShouldContainSubstring, "keep one copy or align the contents")
			})
		})
	})
}

func TestSkillShadowSilentOnIdenticalContent(t *testing.T) {
	Convey("Given identical skill copies", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		cursorHost(t, f)

		write(t, filepath.Join(f.home, ".cursor", "skills", "alpha", "SKILL.md"), "# one\n")
		write(t, f.sharedSkill("alpha"), "# one\n")

		Convey("When doctor runs", func() {
			Convey("Then no shadow warning appears", func() {
				So(hasIssue(shadowIssues(t, f), engine.SeverityWarn, "differs between"), ShouldBeFalse)
			})
		})
	})
}

func TestSkillShadowSilentOnSingleCopy(t *testing.T) {
	Convey("Given a single skill copy", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		cursorHost(t, f)

		write(t, filepath.Join(f.home, ".cursor", "skills", "alpha", "SKILL.md"), "# one\n")

		issues := shadowIssues(t, f)

		Convey("When doctor runs", func() {
			Convey("Then there is no shadow or read warning", func() {
				_, err := os.Stat(filepath.Join(f.home, ".claude", "skills"))
				So(err, ShouldNotBeNil)

				So(hasIssue(issues, engine.SeverityWarn, "differs between"), ShouldBeFalse)
				So(hasIssue(issues, engine.SeverityWarn, "read skills directory"), ShouldBeFalse)
			})
		})
	})
}

func TestSkillShadowSkipsJunkTails(t *testing.T) {
	Convey("Given identical copies with a junk file in one", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		cursorHost(t, f)

		write(t, filepath.Join(f.home, ".cursor", "skills", "alpha", "SKILL.md"), "# one\n")
		write(t, f.sharedSkill("alpha"), "# one\n")
		write(t, filepath.Join(f.home, ".agents", "skills", "alpha", ".DS_Store"), "junk\n")

		Convey("When doctor runs", func() {
			Convey("Then junk does not create a shadow warning", func() {
				So(hasIssue(shadowIssues(t, f), engine.SeverityWarn, "differs between"), ShouldBeFalse)
			})
		})
	})
}

func TestSkillShadowPerAgent(t *testing.T) {
	Convey("Given two agents sharing a divergent directory pair", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		cursorHost(t, f)

		write(t, filepath.Join(f.home, ".claude", "skills", "alpha", "SKILL.md"), "# one\n")
		write(t, f.sharedSkill("alpha"), "# two\n")

		issues := shadowIssues(t, f)

		agents := map[string]int{}

		for _, issue := range issues {
			if strings.Contains(issue.Message, "skill alpha differs between") {
				agents[issue.Agent]++
			}
		}

		Convey("When doctor runs", func() {
			Convey("Then the fail-open agent warns and the verified one collapses", func() {
				// OpenCode v2.0.12 keeps one copy (the .agents one wins), so
				// the divergent .claude copy is invisible there (A-20).
				So(agents, ShouldResemble, map[string]int{agent.CursorID: 1})
				So(agents, ShouldNotContainKey, agent.OpenCodeID)
			})
		})
	})
}

func TestSkillShadowReadError(t *testing.T) {
	Convey("Given an unreadable skills directory", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		dir := filepath.Join(f.home, ".config", "opencode", "skills")
		write(t, filepath.Join(dir, "alpha", "SKILL.md"), "# one\n")

		So(os.Chmod(dir, 0o000), ShouldBeNil)
		t.Cleanup(func() { _ = os.Chmod(dir, 0o750) }) //nolint:gosec // G302: restoring the fixture directory mode

		Convey("When doctor runs", func() {
			Convey("Then a read warning is reported", func() {
				So(hasIssue(shadowIssues(t, f), engine.SeverityWarn, "read skills directory"), ShouldBeTrue)
			})
		})
	})
}
