package engine_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/skill"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// foreignSkill writes a skill copy outside the beadle surfaces and links it
// into the claude read area, mimicking a user symlink.
func foreignSkill(t *testing.T, f *fixture, name, content string) string {
	t.Helper()

	dir := filepath.Join(f.home, "skills-src", name)
	write(t, filepath.Join(dir, "SKILL.md"), content)

	claudeDir := filepath.Join(f.home, ".claude", "skills")
	if err := os.MkdirAll(claudeDir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", claudeDir, err)
	}

	link := filepath.Join(claudeDir, name)
	if err := os.Symlink(dir, link); err != nil {
		t.Fatalf("symlink %s: %v", link, err)
	}

	return dir
}

func setSkillMode(t *testing.T, f *fixture, agentID string, mode config.Mode) {
	t.Helper()

	f.config.SetMode(agentID, kind.Skills, mode)

	if err := f.config.Save(f.vault.ConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

// cursorHost enables Cursor with its config file and skills directory on
// disk. Cursor is the fail-open multi-directory host: it reads
// ~/.claude/skills and ~/.agents/skills without collapsing them.
func cursorHost(t *testing.T, f *fixture) {
	t.Helper()

	f.config.Enable(agent.CursorID)

	if err := f.config.Save(f.vault.ConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	write(t, filepath.Join(f.home, ".cursor", "mcp.json"), `{"mcpServers": {}}`)

	if err := os.MkdirAll(filepath.Join(f.home, ".cursor", "skills"), 0o750); err != nil {
		t.Fatalf("mkdir cursor skills: %v", err)
	}
}

// cursorWritesSkills enables Cursor as a writing skills host.
func cursorWritesSkills(t *testing.T, f *fixture) {
	t.Helper()

	cursorHost(t, f)
	setSkillMode(t, f, agent.CursorID, config.ModeSync)
	setSkillMode(t, f, agent.ClaudeCodeID, config.ModeOff)
}

func TestSyncReleasesForeignCoveredOwnCopy(t *testing.T) {
	Convey("Given a cursor host that writes skills and a foreign copy of the canon name", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		cursorWritesSkills(t, f)

		write(t, f.vaultSkill("alpha"), "# alpha\n")

		f.sync(t)

		ownDir := filepath.Join(f.home, ".cursor", "skills", "alpha")
		So(fsutil.Exists(ownDir), ShouldBeTrue)

		foreignSkill(t, f, "alpha", "# alpha\n")

		Convey("When sync runs", func() {
			report := f.sync(t)
			kr := report.Kind(kind.Skills)

			Convey("Then the beadle copy is released and the foreign copy stays", func() {
				So(kr, ShouldNotBeNil)
				So(fsutil.Exists(ownDir), ShouldBeFalse)
				So(read(t, filepath.Join(f.home, "skills-src", "alpha", "SKILL.md")), ShouldEqual, "# alpha\n")
				So(read(t, f.vaultSkill("alpha")), ShouldEqual, "# alpha\n")
				So(strings.Join(kr.Warnings, " "), ShouldContainSubstring, "is also delivered by")
				So(f.conflicts(t, kind.Skills, agent.CursorID), ShouldBeEmpty)
			})

			Convey("Then a second sync is idempotent", func() {
				second := f.sync(t)

				So(fsutil.Exists(ownDir), ShouldBeFalse)
				So(strings.Join(second.Kind(kind.Skills).Warnings, " "), ShouldNotContainSubstring, "is also delivered by")
			})

			Convey("When the foreign copy disappears", func() {
				So(os.Remove(filepath.Join(f.home, ".claude", "skills", "alpha")), ShouldBeNil)

				f.sync(t)

				Convey("Then the beadle copy comes back", func() {
					So(fsutil.Exists(ownDir), ShouldBeTrue)
				})
			})
		})
	})
}

func TestSyncKeepsOwnCopyWhenForeignForked(t *testing.T) {
	Convey("Given a cursor host whose foreign copy drifted", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		cursorWritesSkills(t, f)

		write(t, f.vaultSkill("alpha"), "# alpha\n")

		f.sync(t)

		ownDir := filepath.Join(f.home, ".cursor", "skills", "alpha")
		So(fsutil.Exists(ownDir), ShouldBeTrue)

		foreignSkill(t, f, "alpha", "# alpha v2\n")

		Convey("When sync runs", func() {
			report := f.sync(t)

			Convey("Then the canon copy stays and the fork is reported", func() {
				So(fsutil.Exists(ownDir), ShouldBeTrue)
				So(read(t, f.vaultSkill("alpha")), ShouldEqual, "# alpha\n")
				So(strings.Join(report.Kind(kind.Skills).Warnings, " "), ShouldContainSubstring, "differs between the canon")
			})

			Convey("And doctor warns about the fork", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityWarn, "differs between the canon"), ShouldBeTrue)
			})
		})
	})
}

func TestDoctorVisibilityIssues(t *testing.T) {
	Convey("Given a file host with two drifting copies and one matching copy", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		setSkillMode(t, f, agent.ClaudeCodeID, config.ModeOff)

		write(t, f.vaultSkill("alpha"), "# alpha\n")
		write(t, f.vaultSkill("golang-x"), "# golang-x\n")

		write(t, f.sharedSkill("alpha"), "# alpha v3\n")

		foreignSkill(t, f, "alpha", "# alpha v2\n")
		foreignSkill(t, f, "golang-x", "# golang-x\n")

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the fork is reported once, on the shadowing winner", func() {
				var (
					forks    int
					forkWarn string
				)

				for _, issue := range issues {
					if issue.Agent == agent.OpenCodeID && issue.Severity == engine.SeverityWarn &&
						strings.Contains(issue.Message, "differs between the canon") {
						forks++
						forkWarn = issue.Message
					}
				}

				So(forks, ShouldEqual, 1)
				So(forkWarn, ShouldContainSubstring, filepath.Join(".agents", "skills", "alpha"))
				So(forkWarn, ShouldNotContainSubstring, filepath.Join(".claude", "skills", "alpha"))
			})

			Convey("And the foreign coverage is reported as info", func() {
				So(hasIssue(issues, engine.SeverityInfo, "1 skill(s) covered by other tools"), ShouldBeTrue)
			})
		})
	})
}

// claudeFileOwnedFixture syncs the canon onto the claude file surface and the
// shared surface, so the beadle base owns the alpha copy there and the
// withdrawal rule (keep for other agents) passes.
func claudeFileOwnedFixture(t *testing.T) *fixture {
	t.Helper()

	f := bundleFixture(t)

	f.config.Enable(agent.SharedID)

	if err := f.config.Save(f.vault.ConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	f.sync(t)

	if !fsutil.Exists(f.claudeSkill("alpha")) {
		t.Fatalf("the claude file surface did not get the canon skill")
	}

	if !fsutil.Exists(f.sharedSkill("alpha")) {
		t.Fatalf("the shared surface did not get the canon skill")
	}

	return f
}

func TestBundleSkipsBaseOwnedLeftover(t *testing.T) {
	Convey("Given a verified bundle and a beadle-owned copy on the file surface", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := claudeFileOwnedFixture(t)

		enableClaude(t, f)

		So(fsutil.Exists(f.claudeSkill("alpha")), ShouldBeFalse)

		// An account sync or a manual copy recreates the base-owned leftover.
		write(t, f.claudeSkill("alpha"), "# alpha\n")

		Convey("When sync runs", func() {
			report := f.sync(t)

			Convey("Then the bundle leaves the name to the file copy", func() {
				So(fsutil.Exists(filepath.Join(coveredBundleSkillsDir(t, f), "alpha", "SKILL.md")), ShouldBeFalse)
				So(fsutil.Exists(f.claudeSkill("alpha")), ShouldBeTrue)
				So(report.Bundles, ShouldNotBeEmpty)
			})
		})
	})
}

func TestBundleEnableWithdrawsOnlyAfterUpdate(t *testing.T) {
	Convey("Given a beadle-owned canon copy on the claude file surface", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := claudeFileOwnedFixture(t)

		cli, _ := claudeBundleCLI(t, f)
		fakeRunner(t, cli)
		foundCLI(t)

		Convey("When the registration fails", func() {
			cli.failOn = map[string]error{"plugin install beadle-canon@beadle": errors.New("registration boom")}

			report, err := f.engine.BundlesEnable(t.Context(), "claude")
			So(err, ShouldBeNil)

			Convey("Then the file copy is not withdrawn before the bundle works", func() {
				So(fsutil.Exists(f.claudeSkill("alpha")), ShouldBeTrue)
				So(report.Bundles, ShouldHaveLength, 1)
				So(report.Bundles[0].Action, ShouldEqual, "failed")
			})
		})

		Convey("When the registration succeeds", func() {
			report, err := f.engine.BundlesEnable(t.Context(), "claude")
			So(err, ShouldBeNil)

			Convey("Then the file copy is withdrawn after the bundle is verified", func() {
				So(report.Bundles, ShouldHaveLength, 1)
				So(report.Bundles[0].Action, ShouldEqual, "enabled")
				So(report.Bundles[0].Tier, ShouldEqual, state.VerifyExecuted)
				So(fsutil.Exists(f.claudeSkill("alpha")), ShouldBeFalse)
			})
		})
	})
}

func TestExplainRows(t *testing.T) {
	Convey("Given a canon skill delivered by a foreign and a shared copy", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		f.config.Enable(agent.SharedID)

		if err := f.config.Save(f.vault.ConfigPath()); err != nil {
			t.Fatalf("save config: %v", err)
		}

		write(t, f.vaultSkill("alpha"), "# alpha\n")
		write(t, f.sharedSkill("alpha"), "# alpha\n")

		foreignSkill(t, f, "alpha", "# alpha\n")

		digest := string(skill.TreeDigest(skill.Tree{"SKILL.md": []byte("# alpha\n")}))[:12]

		Convey("When explain runs", func() {
			rows, err := f.engine.Explain(t.Context(), "alpha")
			So(err, ShouldBeNil)

			Convey("Then every channel is reported with its role", func() {
				So(rows, ShouldResemble, []engine.Explanation{
					{Host: "canon", Channel: "canon", Path: filepath.Join(f.vault.SkillsDir(), "alpha"), Digest: digest, Role: "source"},
					{Host: agent.ClaudeCodeID, Channel: "foreign", Path: "~/.claude/skills/alpha", Digest: digest, Role: "winner"},
					{Host: agent.OpenCodeID, Channel: "shared", Path: "~/.agents/skills/alpha", Digest: digest, Role: "winner"},
					{Host: agent.OpenCodeID, Channel: "foreign", Path: "~/.claude/skills/alpha", Digest: digest, Role: "loser"},
					{Host: agent.SharedID, Channel: "shared", Path: "~/.agents/skills/alpha", Digest: digest, Role: "winner"},
				})
			})

			Convey("And an unknown skill is refused", func() {
				_, err := f.engine.Explain(t.Context(), "missing")
				So(err, ShouldNotBeNil)
			})
		})
	})
}
