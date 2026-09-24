package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// skillIssueFor finds one dangling-reference issue for an agent; it is local
// to this file so the U-15 tests do not depend on another task's helpers.
func skillIssueFor(issues []engine.Issue, severity, agentID, substr string) bool {
	for _, issue := range issues {
		if issue.Severity == severity && issue.Agent == agentID && strings.Contains(issue.Message, substr) {
			return true
		}
	}

	return false
}

// countWarnings counts the report warnings that contain substr.
func countWarnings(report *engine.Report, substr string) int {
	count := 0

	for _, warning := range report.Warnings {
		if strings.Contains(warning, substr) {
			count++
		}
	}

	return count
}

// countIssues counts the doctor issues whose message contains substr.
func countIssues(issues []engine.Issue, substr string) int {
	count := 0

	for _, issue := range issues {
		if strings.Contains(issue.Message, substr) {
			count++
		}
	}

	return count
}

// referenceSilenceFixture writes the given canon skills, syncs and returns
// the report and the doctor issues for the caller's assertions.
func referenceSilenceFixture(t *testing.T, skills map[string]string) (*engine.Report, []engine.Issue) {
	t.Helper()

	f := newFixture(t)
	f.emptyConfigs(t)
	f.enableAgent(t, agent.SharedID)

	for name, body := range skills {
		write(t, filepath.Join(f.vault.SkillsDir(), name, "SKILL.md"),
			"---\nname: "+name+"\ndescription: d\n---\n"+body)
	}

	report := f.sync(t)

	issues, err := f.engine.Doctor(t.Context())
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}

	return report, issues
}

// assertNoReferenceIssues fails when any reference diagnostic is present.
func assertNoReferenceIssues(t *testing.T, report *engine.Report, issues []engine.Issue) {
	t.Helper()

	if strings.Contains(strings.Join(report.Warnings, "\n"), "references skill") {
		t.Fatalf("unexpected reference warning: %v", report.Warnings)
	}

	for _, issue := range issues {
		if strings.Contains(issue.Message, "references skill") {
			t.Fatalf("unexpected reference issue: %s", issue.Message)
		}
	}
}

func TestSkillReferencesDangling(t *testing.T) {
	Convey("Given the incident skill that references a missing skill", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.SharedID)

		write(t, filepath.Join(f.vault.SkillsDir(), "grill-me", "SKILL.md"),
			"---\nname: grill-me\ndescription: d\ndisable-model-invocation: true\n---\n"+
				"Call the Skill tool with \"grilling\".\n")

		report := f.sync(t)

		Convey("When the full sync reports and the doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the missing name is one canon finding, not one line per host", func() {
				So(countWarnings(report, "not in the canon"), ShouldEqual, 1)
				So(strings.Join(report.Warnings, "\n"), ShouldContainSubstring, "add the skill or fix the reference")

				So(countIssues(issues, "not in the canon"), ShouldEqual, 1)
				So(skillIssueFor(issues, engine.SeverityWarn, "", "references skill grilling which is not in the canon"), ShouldBeTrue)
				So(skillIssueFor(issues, engine.SeverityWarn, agent.ClaudeCodeID, "references skill grilling"), ShouldBeFalse)
				So(skillIssueFor(issues, engine.SeverityWarn, agent.OpenCodeID, "references skill grilling"), ShouldBeFalse)
			})
		})
	})
}

func TestSkillReferencesPositive(t *testing.T) {
	Convey("Given both skills in the canon", t, func() {
		report, issues := referenceSilenceFixture(t, map[string]string{
			"grill-me": "Call the Skill tool with \"grilling\".\n",
			"grilling": "Body.\n",
		})

		Convey("When the reference resolves", func() {
			Convey("Then nothing is reported", func() {
				assertNoReferenceIssues(t, report, issues)
			})
		})
	})
}

func TestSkillReferencesCoveredBundle(t *testing.T) {
	Convey("Given a reference to a canon skill the claude bundle leaves out", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)

		write(t, filepath.Join(f.vault.SkillsDir(), "golang-x", "SKILL.md"), "# golang-x\n")
		write(t, filepath.Join(f.vault.SkillsDir(), "grill-me", "SKILL.md"),
			"---\nname: grill-me\ndescription: d\n---\nCall the Skill tool with \"golang-x\".\n")

		foreign := filepath.Join(f.home, "skills-src", "golang-x")
		write(t, filepath.Join(foreign, "SKILL.md"), "# golang-x\n")

		So(os.MkdirAll(filepath.Join(f.home, ".claude", "skills"), 0o750), ShouldBeNil)
		So(os.Symlink(foreign, filepath.Join(f.home, ".claude", "skills", "golang-x")), ShouldBeNil)

		enableClaude(t, f)

		Convey("When the doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then only claude warns, because a foreign copy is not a beadle channel", func() {
				So(skillIssueFor(issues, engine.SeverityWarn, agent.ClaudeCodeID,
					"references skill golang-x which is not delivered"), ShouldBeTrue)

				for _, issue := range issues {
					if strings.Contains(issue.Message, "references skill golang-x") {
						So(issue.Agent, ShouldEqual, agent.ClaudeCodeID)
					}
				}
			})
		})
	})
}

func TestSkillReferencesKeptOwnCopy(t *testing.T) {
	Convey("Given a canon skill delivered by a kept own copy next to a live bundle", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)

		write(t, filepath.Join(f.vault.SkillsDir(), "golang-x", "SKILL.md"), "# golang-x\n")
		write(t, filepath.Join(f.vault.SkillsDir(), "grill-me", "SKILL.md"),
			"---\nname: grill-me\ndescription: d\n---\nCall the Skill tool with \"golang-x\". Then load skill missing-one.\n")

		// Beadle writes its own copy first, so the agent base records it:
		// the bundle enable then keeps it (no shared copy covers the name).
		f.sync(t)

		enableClaude(t, f)

		Convey("When the doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the kept copy keeps the name delivered and other references still warn", func() {
				So(skillIssueFor(issues, engine.SeverityWarn, "", "references skill missing-one"), ShouldBeTrue)
				So(skillIssueFor(issues, engine.SeverityWarn, agent.ClaudeCodeID, "references skill golang-x"), ShouldBeFalse)
			})
		})
	})
}

func TestSkillReferencesSharedCoverage(t *testing.T) {
	Convey("Given a gemini bundle that carries no skills and a shared copy", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		f.enableAgent(t, agent.GeminiCLIID)
		f.enableAgent(t, agent.SharedID)

		So(os.MkdirAll(filepath.Join(f.home, ".gemini"), 0o750), ShouldBeNil)

		write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha\n")
		write(t, filepath.Join(f.vault.SkillsDir(), "grill-me", "SKILL.md"),
			"---\nname: grill-me\ndescription: d\n---\nCall the Skill tool with \"alpha\". Then load skill missing-one.\n")

		foundCLI(t)
		fakeRunner(t, &fakeCLI{respond: geminiExtensionsResponder(`[{"name": "beadle-canon"}]`)})

		_, err := f.engine.BundlesEnable(t.Context(), "gemini")
		So(err, ShouldBeNil)

		f.sync(t)

		Convey("When the doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the shared channel keeps the name delivered for gemini and other references warn", func() {
				So(skillIssueFor(issues, engine.SeverityWarn, "", "references skill missing-one"), ShouldBeTrue)
				So(skillIssueFor(issues, engine.SeverityWarn, agent.GeminiCLIID, "references skill alpha"), ShouldBeFalse)
				So(skillIssueFor(issues, engine.SeverityWarn, agent.ClaudeCodeID, "references skill alpha"), ShouldBeFalse)
			})
		})
	})
}

func TestSkillReferencesServedUnverifiedBundle(t *testing.T) {
	Convey("Given a registered claude bundle whose last probe failed while its file modes are off", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		enableClaude(t, f)

		st := loadState(t, f)
		entry := st.Bundles["claude"]
		entry.VerifyTier = state.VerifyFailed
		st.Bundles["claude"] = entry
		So(st.Save(f.vault.StatePath()), ShouldBeNil)

		write(t, filepath.Join(f.vault.SkillsDir(), "grill-me", "SKILL.md"),
			"---\nname: grill-me\ndescription: d\n---\n"+
				"Call the Skill tool with \"grilling\". Run /foo:bar. Use Skill(beadle-canon:alpha).\n")

		Convey("When the doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the name missing from the canon is one host-independent finding", func() {
				So(countIssues(issues, "not in the canon"), ShouldEqual, 1)
				So(skillIssueFor(issues, engine.SeverityWarn, "", "references skill grilling which is not in the canon"), ShouldBeTrue)
			})

			Convey("Then claude keeps its host findings, because the host still serves the registered bundle", func() {
				So(skillIssueFor(issues, engine.SeverityWarn, agent.ClaudeCodeID, "but plugin foo is not installed"), ShouldBeTrue)
				So(skillIssueFor(issues, engine.SeverityWarn, agent.ClaudeCodeID, "plugin beadle-canon is not installed"), ShouldBeFalse)
			})
		})
	})
}

func TestSkillReferencesPluginNamespace(t *testing.T) {
	Convey("Given a reference to a plugin skill", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.SharedID)

		write(t, filepath.Join(f.vault.SkillsDir(), "grill-me", "SKILL.md"),
			"---\nname: grill-me\ndescription: d\n---\nRun /foo:bar now.\n")

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When the plugin is not installed", func() {
			Convey("Then the plugin-qualified reference is a warning", func() {
				So(skillIssueFor(issues, engine.SeverityWarn, agent.ClaudeCodeID, "but plugin foo is not installed"), ShouldBeTrue)
			})
		})
	})
}

func TestSkillReferencesPluginInstalled(t *testing.T) {
	Convey("Given a reference to a skill of the installed canon bundle", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)

		write(t, filepath.Join(f.vault.SkillsDir(), "grill-me", "SKILL.md"),
			"---\nname: grill-me\ndescription: d\n---\nUse Skill(beadle-canon:alpha) now.\n")

		enableClaude(t, f)

		Convey("When the plugin is installed and the skill delivered", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then nothing is reported", func() {
				for _, issue := range issues {
					So(strings.Contains(issue.Message, "references skill"), ShouldBeFalse)
				}
			})
		})
	})
}

func TestSkillReferencesExplicitForm(t *testing.T) {
	Convey("Given the incident form written as Skill(grilling)", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.SharedID)

		write(t, filepath.Join(f.vault.SkillsDir(), "grill-me", "SKILL.md"),
			"---\nname: grill-me\ndescription: d\n---\nUse Skill(grilling) now.\n")

		report := f.sync(t)

		Convey("When the reference check runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the hyphen-less explicit reference is reported", func() {
				So(skillIssueFor(issues, engine.SeverityWarn, "", "references skill grilling"), ShouldBeTrue)
				So(strings.Join(report.Warnings, "\n"), ShouldContainSubstring, "references skill grilling")
			})
		})
	})

	Convey("Given an installed farm plugin whose skill is outside the canon", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.SharedID)

		write(t, filepath.Join(f.vault.SkillsDir(), "grill-me", "SKILL.md"),
			"---\nname: grill-me\ndescription: d\n---\nRun /caveman:whatever now.\n")

		write(t, f.vault.PluginsLedgerPath(), `{"version": 1, "plugins": {"acme/caveman": {"version": "1.0.0", "target": "/tmp/caveman", "updated_at": "2026-09-23T10:00:00Z"}}}`)

		Convey("When the reference check runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the installed namespace is enough (no canon noise)", func() {
				So(skillIssueFor(issues, engine.SeverityWarn, agent.ClaudeCodeID, "caveman"), ShouldBeFalse)
			})
		})
	})
}

func TestSkillReferencesPluginWithoutSkills(t *testing.T) {
	Convey("Given a gemini bundle that carries no skills", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		f.enableAgent(t, agent.GeminiCLIID)
		f.enableAgent(t, agent.SharedID)

		So(os.MkdirAll(filepath.Join(f.home, ".gemini"), 0o750), ShouldBeNil)

		write(t, filepath.Join(f.vault.SkillsDir(), "grill-me", "SKILL.md"),
			"---\nname: grill-me\ndescription: d\n---\nUse Skill(beadle-canon:alpha) now.\n")

		foundCLI(t)
		fakeRunner(t, &fakeCLI{respond: geminiExtensionsResponder(`[{"name": "beadle-canon"}]`)})

		_, err := f.engine.BundlesEnable(t.Context(), "gemini")
		So(err, ShouldBeNil)

		Convey("When the reference check runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the plugin is not installed for gemini, because its bundle has no skills", func() {
				So(skillIssueFor(issues, engine.SeverityWarn, agent.GeminiCLIID, "but plugin beadle-canon is not installed"), ShouldBeTrue)
			})
		})
	})
}

func TestSkillReferencesUnquotedProse(t *testing.T) {
	Convey("Given unquoted candidates that are prose or hyphen-less", t, func() {
		report, issues := referenceSilenceFixture(t, map[string]string{
			"writer":   "Use skill safely. Then load skill files.\n",
			"grill-me": "Call the skill grilling\n",
		})

		Convey("When the reference check runs", func() {
			Convey("Then neither prose nor the hyphen-less unquoted form is reported", func() {
				assertNoReferenceIssues(t, report, issues)
			})
		})
	})

	Convey("Given an unquoted candidate that looks like a skill", t, func() {
		report, _ := referenceSilenceFixture(t, map[string]string{
			"tdd": "load skill codebase-design.\n",
		})

		Convey("When the reference check runs", func() {
			Convey("Then the slug-shaped name is reported", func() {
				So(strings.Join(report.Warnings, "\n"), ShouldContainSubstring, "references skill codebase-design")
			})
		})
	})
}

func TestSkillReferencesNegativeControl(t *testing.T) {
	Convey("Given prose about skills without reference phrases", t, func() {
		report, issues := referenceSilenceFixture(t, map[string]string{
			"writer": "This skill loads skills from the skills directory and documents the skill file format.\n",
		})

		Convey("When the reference check runs", func() {
			Convey("Then nothing is reported", func() {
				assertNoReferenceIssues(t, report, issues)
			})
		})
	})
}

func TestSkillReferencesHostIndependentDedup(t *testing.T) {
	Convey("Given one missing skill referenced from three hosts", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.GeminiCLIID)
		f.enableAgent(t, agent.SharedID)

		So(os.MkdirAll(filepath.Join(f.home, ".gemini"), 0o750), ShouldBeNil)

		write(t, filepath.Join(f.vault.SkillsDir(), "grill-me", "SKILL.md"),
			"---\nname: grill-me\ndescription: d\n---\nCall the Skill tool with \"grilling\".\n")

		report := f.sync(t)

		Convey("When the sync reports", func() {
			Convey("Then the host-independent line appears once", func() {
				So(countWarnings(report, "not in the canon"), ShouldEqual, 1)
			})
		})
	})
}
