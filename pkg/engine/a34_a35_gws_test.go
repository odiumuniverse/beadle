package engine_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func piSettingsPath(f *fixture) string { return filepath.Join(piDir(f), "settings.json") }

func kiloCanonSkillsDir(f *fixture) string { return filepath.Join(kiloDir(f), "skills") }

// hasIssueFor reports one issue with the exact severity, agent, kind and
// every given message substring.
func hasIssueFor(issues []engine.Issue, severity, agentID string, k kind.ID, substrings ...string) bool {
	for _, issue := range issues {
		if issue.Severity != severity || issue.Agent != agentID || issue.Kind != k {
			continue
		}

		if slices.ContainsFunc(substrings, func(s string) bool { return !strings.Contains(issue.Message, s) }) {
			continue
		}

		return true
	}

	return false
}

// hasKiloSkillIssue reports any Kilo skills issue whose message contains the
// substring, whatever its severity.
func hasKiloSkillIssue(issues []engine.Issue, substr string) bool {
	for _, issue := range issues {
		if issue.Agent == agent.KiloID && issue.Kind == kind.Skills && strings.Contains(issue.Message, substr) {
			return true
		}
	}

	return false
}

func TestPiMCPAdapterGate(t *testing.T) {
	Convey("Given Pi with a managed MCP server and no adapter markers", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := piFixture(t, true)
		stubDaemonUnit(t, f)

		write(t, piMCPPath(f), `{"mcpServers": {"demo": {"transport": "stdio", "command": ["demo-mcp"]}}}`)
		write(t, f.vault.ServersPath(), `{"demo":{"transport":"stdio","command":["demo-mcp"]}}`)

		f.sync(t)

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the adapter dependency is a warning naming the server", func() {
				So(hasIssueFor(issues, engine.SeverityWarn, agent.PiID, kind.MCP, "pi-mcp-adapter", "demo"), ShouldBeTrue)
			})
		})

		Convey("When the adapter is registered in the Pi settings", func() {
			write(t, piSettingsPath(f), `{"packages": ["npm:pi-mcp-adapter"]}`)

			Convey("Then the warning is gone", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityWarn, "pi-mcp-adapter"), ShouldBeFalse)
			})
		})

		Convey("When the adapter sits in the managed npm catalog", func() {
			write(t, filepath.Join(piDir(f), "npm", "node_modules", "pi-mcp-adapter", "package.json"), `{"name":"pi-mcp-adapter"}`)

			Convey("Then the warning is gone", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityWarn, "pi-mcp-adapter"), ShouldBeFalse)
			})
		})

		Convey("When the adapter is registered in the project scope", func() {
			project := t.TempDir()
			write(t, filepath.Join(project, ".pi", "settings.json"), `{"packages": ["npm:pi-mcp-adapter"]}`)
			f.useCwd(t, project)

			Convey("Then the warning is gone", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityWarn, "pi-mcp-adapter"), ShouldBeFalse)
			})
		})
	})

	Convey("Given Pi with the MCP surface present but no managed server", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := piFixture(t, true)
		stubDaemonUnit(t, f)

		f.sync(t)

		Convey("Then doctor says nothing about the adapter", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)
			So(hasIssue(issues, engine.SeverityWarn, "pi-mcp-adapter"), ShouldBeFalse)
			So(hasIssue(issues, engine.SeverityInfo, "pi-mcp-adapter"), ShouldBeFalse)
		})
	})
}

func TestKiloDualReadSkills(t *testing.T) {
	Convey("Given Kilo with a skill only in the docs path", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := kiloFixture(t)
		stubDaemonUnit(t, f)

		write(t, filepath.Join(kiloSkillsDir(f), "legacy", "SKILL.md"), "# legacy\n")

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the docs path is an informational note, not a duplication", func() {
				So(hasIssueFor(issues, engine.SeverityInfo, agent.KiloID, kind.Skills, "~/.kilo/skills"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityWarn, "duplicate the canon"), ShouldBeFalse)
			})
		})
	})

	Convey("Given Kilo with a docs-path copy of a canon skill", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := kiloFixture(t)
		stubDaemonUnit(t, f)

		write(t, f.vaultSkill("alpha"), "# alpha\n")
		write(t, filepath.Join(kiloSkillsDir(f), "alpha", "SKILL.md"), "# alpha\n")

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the duplicate warns to move the copies, not to remove them", func() {
				So(hasIssueFor(issues, engine.SeverityWarn, agent.KiloID, kind.Skills,
					"duplicate the canon", "move them to"), ShouldBeTrue)
				So(hasKiloSkillIssue(issues, "remove"), ShouldBeFalse)
			})
		})

		Convey("When beadle writes the skills canon", func() {
			f.config.SetMode(agent.KiloID, kind.Skills, config.ModeSync)
			So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)
			So(os.MkdirAll(kiloCanonSkillsDir(f), 0o750), ShouldBeNil)

			Convey("Then the duplicate warns with a removal hint", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssueFor(issues, engine.SeverityWarn, agent.KiloID, kind.Skills,
					"duplicate the canon", "remove the copies manually"), ShouldBeTrue)
			})
		})
	})

	Convey("Given Kilo with a foreign docs-path skill while beadle writes", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := kiloFixture(t)
		stubDaemonUnit(t, f)

		f.config.SetMode(agent.KiloID, kind.Skills, config.ModeSync)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)
		So(os.MkdirAll(kiloCanonSkillsDir(f), 0o750), ShouldBeNil)

		write(t, filepath.Join(kiloSkillsDir(f), "foreign", "SKILL.md"), "# foreign\n")

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the foreign copy is only ever moved, never removed", func() {
				So(hasIssueFor(issues, engine.SeverityInfo, agent.KiloID, kind.Skills,
					"move them to", "pulls the canonical directory into the canon"), ShouldBeTrue)
				So(hasKiloSkillIssue(issues, "remove"), ShouldBeFalse)
			})
		})
	})
}

func TestKiloSkillShadowing(t *testing.T) {
	Convey("Given Kilo with a canon skill in each read path", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := kiloFixture(t)

		write(t, f.vaultSkill("alpha"), "# alpha\n")
		write(t, filepath.Join(kiloCanonSkillsDir(f), "alpha", "SKILL.md"), "# alpha\n")
		write(t, filepath.Join(kiloDir(f), "skill", "alpha", "SKILL.md"), "# alpha\n")
		write(t, filepath.Join(kiloSkillsDir(f), "alpha", "SKILL.md"), "# alpha\n")
		write(t, filepath.Join(f.home, ".kilo", "skill", "alpha", "SKILL.md"), "# alpha\n")
		write(t, f.sharedSkill("alpha"), "# alpha\n")

		Convey("Then the host shows one winner and shadows the rest", func() {
			So(explainHostRows(t, f, agent.KiloID), ShouldResemble, []engine.Explanation{
				{Host: agent.KiloID, Channel: "foreign", Path: "~/.config/kilo/skills/alpha", Digest: alphaDigest(), Role: "winner"},
				{Host: agent.KiloID, Channel: "foreign", Path: "~/.config/kilo/skill/alpha", Digest: alphaDigest(), Role: "loser"},
				{Host: agent.KiloID, Channel: "foreign", Path: "~/.kilo/skills/alpha", Digest: alphaDigest(), Role: "loser"},
				{Host: agent.KiloID, Channel: "foreign", Path: "~/.kilo/skill/alpha", Digest: alphaDigest(), Role: "loser"},
				{Host: agent.KiloID, Channel: "shared", Path: "~/.agents/skills/alpha", Digest: alphaDigest(), Role: "loser"},
			})
		})
	})

	Convey("Given Kilo with a symlinked docs-path skill", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := kiloFixture(t)
		stubDaemonUnit(t, f)

		target := filepath.Join(t.TempDir(), "linked")
		write(t, filepath.Join(target, "SKILL.md"), "# linked\n")
		So(os.Symlink(target, filepath.Join(kiloSkillsDir(f), "linked")), ShouldBeNil)

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the symlinked copy is counted", func() {
				So(hasIssueFor(issues, engine.SeverityInfo, agent.KiloID, kind.Skills, "1 skill(s)", "~/.kilo/skills"), ShouldBeTrue)
			})
		})
	})
}

func TestKiloLegacyHintVariants(t *testing.T) {
	Convey("Given Kilo in sync mode with a legacy duplicate and no canonical directory", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := kiloFixture(t)
		stubDaemonUnit(t, f)

		f.config.SetMode(agent.KiloID, kind.Skills, config.ModeSync)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		write(t, f.vaultSkill("alpha"), "# alpha\n")
		write(t, filepath.Join(kiloSkillsDir(f), "alpha", "SKILL.md"), "# alpha\n")

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the removal advice waits for the directory", func() {
				So(hasIssueFor(issues, engine.SeverityWarn, agent.KiloID, kind.Skills,
					"duplicate the canon", "create it and run beadle sync", "then remove the copies manually"), ShouldBeTrue)
			})
		})
	})

	Convey("Given Kilo in push mode with a foreign legacy skill", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := kiloFixture(t)
		stubDaemonUnit(t, f)

		f.config.SetMode(agent.KiloID, kind.Skills, config.ModePush)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		write(t, filepath.Join(kiloSkillsDir(f), "foreign", "SKILL.md"), "# foreign\n")

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the advice switches the mode first", func() {
				So(hasIssueFor(issues, engine.SeverityInfo, agent.KiloID, kind.Skills,
					"run beadle agents mode kilo skills sync", "move them to", "run beadle sync"), ShouldBeTrue)
				So(hasKiloSkillIssue(issues, "remove"), ShouldBeFalse)
			})
		})
	})
}

func TestKiloSkillsWriteTarget(t *testing.T) {
	Convey("Given Kilo with the skills kind switched to sync", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := kiloFixture(t)
		f.config.SetMode(agent.KiloID, kind.Skills, config.ModeSync)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		So(os.MkdirAll(kiloCanonSkillsDir(f), 0o750), ShouldBeNil)

		write(t, f.vaultSkill("alpha"), "# alpha\n")
		write(t, filepath.Join(kiloSkillsDir(f), "decoy", "SKILL.md"), "# decoy\n")

		Convey("When sync runs", func() {
			f.sync(t)

			canonSkill := filepath.Join(kiloCanonSkillsDir(f), "alpha", "SKILL.md")
			legacySkill := filepath.Join(kiloSkillsDir(f), "decoy", "SKILL.md")

			Convey("Then the canon lands in the canonical path and the docs path is untouched", func() {
				So(read(t, canonSkill), ShouldEqual, "# alpha\n")
				So(read(t, legacySkill), ShouldEqual, "# decoy\n")
				So(fsutil.Exists(filepath.Join(kiloSkillsDir(f), "alpha")), ShouldBeFalse)
			})

			Convey("Then a second sync is a no-op on both paths", func() {
				report, before := noopSync(t, f, canonSkill, legacySkill)
				So(report.Action(kind.Skills, agent.KiloID), ShouldEqual, engine.ActionNoop)
				So(read(t, canonSkill), ShouldEqual, before[canonSkill])
				So(read(t, legacySkill), ShouldEqual, before[legacySkill])
			})
		})
	})

	Convey("Given Kilo with the skills kind switched to sync but no canonical directory", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := kiloFixture(t)
		f.config.SetMode(agent.KiloID, kind.Skills, config.ModeSync)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		write(t, f.vaultSkill("alpha"), "# alpha\n")

		Convey("When sync runs", func() {
			report := f.sync(t)

			Convey("Then the push is skipped until the directory exists", func() {
				result, ok := report.Kind(kind.Skills).Agent(agent.KiloID)
				So(ok, ShouldBeTrue)
				So(result.Action, ShouldEqual, engine.ActionSkipped)
				So(result.Note, ShouldContainSubstring, "no config file to write into")
				So(fsutil.Exists(kiloCanonSkillsDir(f)), ShouldBeFalse)
			})
		})
	})
}
