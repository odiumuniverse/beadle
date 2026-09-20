package engine_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/lock"
	"github.com/odiumuniverse/beadle/pkg/mcp"
	"github.com/odiumuniverse/beadle/pkg/permission"
	"github.com/odiumuniverse/beadle/pkg/secret"
	"github.com/odiumuniverse/beadle/pkg/state"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

type fixture struct {
	home   string
	vault  *vault.Vault
	config *config.Config
	engine *engine.Engine
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	home := t.TempDir()
	v := vault.New(filepath.Join(t.TempDir(), "vault"))

	if err := v.Init(); err != nil {
		t.Fatalf("init vault: %v", err)
	}

	cfg, err := config.Load(v.ConfigPath())
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	cfg.Enable(agent.ClaudeCodeID)
	cfg.Enable(agent.OpenCodeID)

	if err := cfg.Save(v.ConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	e, err := engine.New(v, cfg, agent.All(home, t.TempDir()), engine.WithHome(home))
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	return &fixture{home: home, vault: v, config: cfg, engine: e}
}

func (f *fixture) run(t *testing.T, opts engine.SyncOptions) *engine.Report {
	t.Helper()

	report, err := f.engine.Sync(t.Context(), opts)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if errs := report.Errors(); len(errs) != 0 {
		t.Fatalf("sync errors: %v", errs)
	}

	return report
}

func (f *fixture) sync(t *testing.T) *engine.Report {
	t.Helper()

	return f.run(t, engine.SyncOptions{})
}

func (f *fixture) conflicts(t *testing.T, k kind.ID, agentID string) []state.Conflict {
	t.Helper()

	all, err := f.engine.Conflicts()
	if err != nil {
		t.Fatalf("conflicts: %v", err)
	}

	var out []state.Conflict

	for _, c := range all {
		if c.Kind == k && c.Agent == agentID {
			out = append(out, c)
		}
	}

	return out
}

func (f *fixture) conflict(t *testing.T, k kind.ID, agentID string) state.Conflict {
	t.Helper()

	conflicts := f.conflicts(t, k, agentID)
	if len(conflicts) != 1 {
		t.Fatalf("expected one conflict, got %d", len(conflicts))
	}

	return conflicts[0]
}

func (f *fixture) resolve(t *testing.T, k kind.ID, agentID string, take engine.Take) *engine.Report {
	t.Helper()

	c := f.conflict(t, k, agentID)

	report, err := f.engine.Resolve(t.Context(), []string{c.ID()}, engine.Resolution{Take: take})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	return report
}

func (f *fixture) servers(t *testing.T) mcp.Servers {
	t.Helper()

	servers, err := mcp.ParseCanonical([]byte(read(t, f.vault.ServersPath())))
	if err != nil {
		t.Fatalf("parse servers: %v", err)
	}

	return servers
}

func (f *fixture) rules(t *testing.T) permission.Rules {
	t.Helper()

	rules, err := permission.Parse([]byte(read(t, f.vault.PermissionsPath())))
	if err != nil {
		t.Fatalf("parse rules: %v", err)
	}

	return rules
}

func (f *fixture) claudeConfig() string   { return filepath.Join(f.home, ".claude.json") }
func (f *fixture) claudeRules() string    { return filepath.Join(f.home, ".claude", "CLAUDE.md") }
func (f *fixture) claudeSettings() string { return filepath.Join(f.home, ".claude", "settings.json") }

func (f *fixture) openCodeRules() string {
	return filepath.Join(f.home, ".config", "opencode", "AGENTS.md")
}

func (f *fixture) geminiSettings() string { return filepath.Join(f.home, ".gemini", "settings.json") }

func (f *fixture) cursorCLIConfig() string {
	return filepath.Join(f.home, ".cursor", "cli-config.json")
}

func (f *fixture) openCodeConfig() string {
	return filepath.Join(f.home, ".config", "opencode", "opencode.jsonc")
}

func (f *fixture) claudeSkill(name string) string {
	return filepath.Join(f.home, ".claude", "skills", name, "SKILL.md")
}

func (f *fixture) openCodeSkill(name string) string {
	return filepath.Join(f.home, ".config", "opencode", "skills", name, "SKILL.md")
}

func (f *fixture) sharedSkill(name string) string {
	return filepath.Join(f.home, ".agents", "skills", name, "SKILL.md")
}

func (f *fixture) vaultSkill(name string) string {
	return filepath.Join(f.vault.SkillsDir(), name, "SKILL.md")
}

func (f *fixture) emptyConfigs(t *testing.T) {
	t.Helper()

	write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	write(t, f.openCodeConfig(), `{"mcp": {}}`)
	write(t, f.claudeRules(), "# r\n")
	write(t, f.openCodeRules(), "# r\n")
}

func write(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}

	if err := fsutil.WriteFileAtomic(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: tests read their own temp files
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(data)
}

func hasIssue(issues []engine.Issue, severity, substr string) bool {
	for _, issue := range issues {
		if issue.Severity == severity && strings.Contains(issue.Message, substr) {
			return true
		}
	}

	return false
}

func reportChanges(t *testing.T, result engine.AgentResult) string {
	t.Helper()

	var out strings.Builder

	for _, change := range result.Changes {
		out.WriteString(string(change.Before))
		out.WriteString(string(change.After))
	}

	return out.String()
}

func gitLog(t *testing.T, dir string) string {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "git", "-C", dir, "log", "--oneline").Output() //nolint:gosec // G204: fixed git subcommand in a test
	if err != nil {
		t.Fatalf("git log: %v", err)
	}

	return string(out)
}

func TestSyncUnionAndIdempotency(t *testing.T) {
	Convey("Given two agents with disjoint configs", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, f.claudeConfig(), `{"numStartups": 7, "mcpServers": {"alpha": {"type": "stdio", "command": "cmd", "args": ["a"]}}}`)
		write(t, f.claudeRules(), "# shared\n")
		write(t, f.openCodeConfig(), "{\n  // keep\n  \"$schema\": \"https://opencode.ai/config.json\",\n  \"mcp\": {\"beta\": {\"type\": \"local\", \"command\": [\"b\"]}}\n}")
		write(t, f.openCodeRules(), "# shared\n")

		report := f.sync(t)
		So(report.Conflicts, ShouldBeEmpty)
		So(read(t, f.vault.RulesPath()), ShouldEqual, "# shared\n")

		servers := f.servers(t)
		So(servers, ShouldHaveLength, 2)
		So(servers, ShouldContainKey, "alpha")
		So(servers, ShouldContainKey, "beta")

		claude := read(t, f.claudeConfig())
		So(claude, ShouldContainSubstring, "beta")
		So(claude, ShouldContainSubstring, "numStartups")

		openCode := read(t, f.openCodeConfig())
		So(openCode, ShouldContainSubstring, "alpha")
		So(openCode, ShouldContainSubstring, "// keep")

		Convey("When it syncs again", func() {
			report = f.sync(t)

			Convey("Then the second sync is a noop", func() {
				So(report.VaultChanged(), ShouldBeFalse)
				So(report.Pushed(), ShouldBeFalse)
				So(report.Action(kind.Rules, agent.ClaudeCodeID), ShouldEqual, engine.ActionNoop)
				So(report.Action(kind.MCP, agent.ClaudeCodeID), ShouldEqual, engine.ActionNoop)
				So(report.Action(kind.MCP, agent.OpenCodeID), ShouldEqual, engine.ActionNoop)
			})
		})
	})
}

func TestRulesConflictResolvedInEditor(t *testing.T) {
	Convey("Given divergent rules files", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		f.sync(t)

		write(t, f.claudeRules(), "# claude\n")
		write(t, f.openCodeRules(), "# opencode\n")

		report := f.sync(t)
		So(report.ConflictsOf(kind.Rules), ShouldHaveLength, 1)
		So(read(t, f.vault.RulesPath()), ShouldEqual, "# claude\n")
		So(read(t, f.claudeRules()), ShouldEqual, "# claude\n")
		So(read(t, f.openCodeRules()), ShouldEqual, "# opencode\n")

		c := f.conflict(t, kind.Rules, agent.OpenCodeID)

		file, err := f.engine.ConflictFile(c)
		So(err, ShouldBeNil)
		So(read(t, file), ShouldContainSubstring, "<<<<<<< vault")
		So(read(t, file), ShouldContainSubstring, ">>>>>>> agent:opencode")

		write(t, file, "# resolved\n")

		Convey("When the edited file is taken", func() {
			_, err = f.engine.Resolve(t.Context(), []string{c.ID()}, engine.Resolution{Take: engine.TakeFile})
			So(err, ShouldBeNil)

			_, fileErr := os.Stat(file)

			Convey("Then it lands on both sides", func() {
				So(read(t, f.claudeRules()), ShouldEqual, "# resolved\n")
				So(read(t, f.openCodeRules()), ShouldEqual, "# resolved\n")
				So(errors.Is(fileErr, fs.ErrNotExist), ShouldBeTrue)
				So(f.conflicts(t, kind.Rules, agent.OpenCodeID), ShouldBeEmpty)
			})
		})
	})
}

func TestResolveRejectsLeftoverMarkers(t *testing.T) {
	Convey("Given a conflict file with markers left", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		f.sync(t)

		write(t, f.claudeRules(), "# claude\n")
		write(t, f.openCodeRules(), "# opencode\n")
		f.sync(t)

		c := f.conflict(t, kind.Rules, agent.OpenCodeID)

		Convey("When it is resolved from the file", func() {
			_, err := f.engine.Resolve(t.Context(), []string{c.ID()}, engine.Resolution{Take: engine.TakeFile})

			Convey("Then markers are rejected and the conflict stays", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "conflict markers")
				So(f.conflicts(t, kind.Rules, agent.OpenCodeID), ShouldHaveLength, 1)
			})
		})
	})
}

func TestMCPConflictTakeAgent(t *testing.T) {
	Convey("Given an MCP conflict", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"gamma": {"type": "stdio", "command": "v1"}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)

		f.sync(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"gamma": {"type": "stdio", "command": "v2"}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {"gamma": {"type": "local", "command": ["v3"]}}}`)

		report := f.sync(t)

		Convey("When it is resolved from the agent", func() {
			So(report.ConflictsOf(kind.MCP), ShouldNotBeEmpty)
			So(f.servers(t)["gamma"].Command, ShouldResemble, []string{"v2"})

			f.resolve(t, kind.MCP, agent.OpenCodeID, engine.TakeAgent)

			Convey("Then the agent value wins everywhere", func() {
				So(f.servers(t)["gamma"].Command, ShouldResemble, []string{"v3"})
				So(read(t, f.claudeConfig()), ShouldContainSubstring, "v3")
				So(read(t, f.openCodeConfig()), ShouldContainSubstring, "v3")
			})
		})
	})
}

func TestMCPConflictTakeVault(t *testing.T) {
	Convey("Given an MCP conflict", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"gamma": {"type": "stdio", "command": "v1"}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)
		f.sync(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"gamma": {"type": "stdio", "command": "v2"}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {"gamma": {"type": "local", "command": ["v3"]}}}`)
		f.sync(t)

		Convey("When it is resolved from the vault", func() {
			f.resolve(t, kind.MCP, agent.OpenCodeID, engine.TakeVault)

			Convey("Then the vault value wins", func() {
				So(f.servers(t)["gamma"].Command, ShouldResemble, []string{"v2"})
				So(read(t, f.openCodeConfig()), ShouldContainSubstring, `"v2"`)
				So(read(t, f.openCodeConfig()), ShouldNotContainSubstring, "v3")
			})
		})
	})
}

func TestMCPConflictDoesNotFreezeOtherServers(t *testing.T) {
	Convey("Given a conflict and an unrelated server", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"gamma": {"type": "stdio", "command": "v1"}, "alpha": {"type": "stdio", "command": "a"}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)
		f.sync(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"gamma": {"type": "stdio", "command": "v2"}, "alpha": {"type": "stdio", "command": "a"}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {"alpha": {"type": "local", "command": ["a"]}, "gamma": {"type": "local", "command": ["v3"]}, "beta": {"type": "local", "command": ["b"]}}}`)

		report := f.sync(t)
		So(report.ConflictsOf(kind.MCP), ShouldNotBeEmpty)
		So(f.servers(t), ShouldContainKey, "beta")
		So(read(t, f.claudeConfig()), ShouldContainSubstring, "beta")
		So(read(t, f.claudeConfig()), ShouldContainSubstring, `"v2"`)
		So(read(t, f.openCodeConfig()), ShouldContainSubstring, `"v3"`)

		Convey("When the conflict persists across syncs", func() {
			for range 2 {
				report = f.sync(t)
				So(report.ConflictsOf(kind.MCP), ShouldNotBeEmpty)
			}

			So(read(t, f.claudeConfig()), ShouldContainSubstring, `"v2"`)
			So(read(t, f.openCodeConfig()), ShouldContainSubstring, `"v3"`)

			f.resolve(t, kind.MCP, agent.OpenCodeID, engine.TakeAgent)

			report = f.sync(t)

			Convey("Then resolving it settles everything", func() {
				So(read(t, f.claudeConfig()), ShouldContainSubstring, `"v3"`)
				So(read(t, f.openCodeConfig()), ShouldContainSubstring, `"v3"`)
				So(report.Conflicts, ShouldBeEmpty)
				So(report.VaultChanged(), ShouldBeFalse)
				So(report.Action(kind.MCP, agent.ClaudeCodeID), ShouldEqual, engine.ActionNoop)
				So(report.Action(kind.MCP, agent.OpenCodeID), ShouldEqual, engine.ActionNoop)
			})
		})
	})
}

func TestFirstSyncDisagreementIsAConflict(t *testing.T) {
	Convey("Given a first sync with disagreeing servers", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"gamma": {"type": "stdio", "command": "v1"}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {"gamma": {"type": "local", "command": ["v9"]}}}`)

		f.sync(t)

		c := f.conflict(t, kind.MCP, agent.OpenCodeID)

		Convey("When it is diagnosed", func() {
			Convey("Then nothing is overwritten before a human decides", func() {
				So(c.Reason, ShouldEqual, state.ReasonAdded)
				So(read(t, f.openCodeConfig()), ShouldContainSubstring, "v9")
				So(read(t, f.claudeConfig()), ShouldContainSubstring, "v1")
			})
		})
	})
}

func TestSkillsSync(t *testing.T) {
	Convey("Given skills across agents", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.config.Enable(agent.SharedID)
		f.emptyConfigs(t)

		write(t, f.claudeSkill("alpha"), "alpha\n")
		write(t, f.claudeSkill("shared"), "v1\n")
		write(t, f.openCodeSkill("beta"), "beta\n")
		write(t, f.openCodeSkill("shared"), "v1\n")

		report := f.sync(t)

		Convey("When it syncs", func() {
			report = f.sync(t)

			Convey("Then skills fan out and OpenCode's own dir is never written", func() {
				So(report.Conflicts, ShouldBeEmpty)
				So(report.Kind(kind.Skills).VaultChanged, ShouldBeFalse)
				So(report.Action(kind.Skills, agent.SharedID), ShouldEqual, engine.ActionNoop)
			})
		})

		Convey("Then the shared dir receives every skill", func() {
			for _, name := range []string{"alpha", "beta", "shared"} {
				_, vaultErr := os.Stat(f.vaultSkill(name))
				_, sharedErr := os.Stat(f.sharedSkill(name))

				So(vaultErr, ShouldBeNil)
				So(sharedErr, ShouldBeNil)
			}

			_, betaErr := os.Stat(f.claudeSkill("beta"))
			_, alphaErr := os.Stat(f.openCodeSkill("alpha"))

			So(betaErr, ShouldBeNil)
			So(errors.Is(alphaErr, fs.ErrNotExist), ShouldBeTrue)
		})
	})
}

func TestSkillsConflictTakeAgent(t *testing.T) {
	Convey("Given a skills conflict", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, f.claudeSkill("shared"), "v1\n")
		write(t, f.openCodeSkill("shared"), "v1\n")
		f.sync(t)

		write(t, f.claudeSkill("shared"), "claude-edit\n")
		write(t, f.openCodeSkill("shared"), "opencode-edit\n")

		report := f.sync(t)

		Convey("When it is resolved from the agent", func() {
			So(report.ConflictsOf(kind.Skills), ShouldNotBeEmpty)
			So(read(t, f.vaultSkill("shared")), ShouldEqual, "claude-edit\n")

			f.resolve(t, kind.Skills, agent.OpenCodeID, engine.TakeAgent)

			Convey("Then the agent value wins everywhere", func() {
				So(read(t, f.vaultSkill("shared")), ShouldEqual, "opencode-edit\n")
				So(read(t, f.claudeSkill("shared")), ShouldEqual, "opencode-edit\n")
			})
		})
	})
}

func TestSkillsConflictDoesNotFreezeOtherSkills(t *testing.T) {
	Convey("Given a skills conflict and an unrelated skill", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.config.Enable(agent.SharedID)
		f.emptyConfigs(t)

		write(t, f.claudeSkill("shared"), "v1\n")
		write(t, f.openCodeSkill("shared"), "v1\n")
		f.sync(t)

		write(t, f.claudeSkill("shared"), "claude-edit\n")
		write(t, f.openCodeSkill("shared"), "opencode-edit\n")
		write(t, f.openCodeSkill("beta"), "beta\n")

		report := f.sync(t)

		Convey("When the conflict persists", func() {
			So(report.ConflictsOf(kind.Skills), ShouldNotBeEmpty)

			So(read(t, f.vaultSkill("beta")), ShouldEqual, "beta\n")
			So(read(t, f.claudeSkill("beta")), ShouldEqual, "beta\n")
			So(read(t, f.sharedSkill("beta")), ShouldEqual, "beta\n")

			for range 2 {
				report = f.sync(t)
				So(report.ConflictsOf(kind.Skills), ShouldNotBeEmpty)
			}

			f.resolve(t, kind.Skills, agent.OpenCodeID, engine.TakeAgent)

			report = f.sync(t)

			Convey("Then resolution settles all", func() {
				So(read(t, f.vaultSkill("shared")), ShouldEqual, "opencode-edit\n")
				So(read(t, f.claudeSkill("shared")), ShouldEqual, "opencode-edit\n")
				So(read(t, f.sharedSkill("shared")), ShouldEqual, "opencode-edit\n")

				So(report.Conflicts, ShouldBeEmpty)
				So(report.Kind(kind.Skills).VaultChanged, ShouldBeFalse)
			})
		})
	})
}

func TestDeletedSkillStaysDeleted(t *testing.T) {
	Convey("Given a deleted skill", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, f.claudeSkill("old"), "old\n")
		f.sync(t)

		So(os.RemoveAll(filepath.Dir(f.claudeSkill("old"))), ShouldBeNil)

		Convey("When it syncs again", func() {
			f.sync(t)

			_, claudeErr := os.Stat(f.claudeSkill("old"))
			_, vaultErr := os.Stat(f.vaultSkill("old"))

			Convey("Then it does not come back", func() {
				So(errors.Is(claudeErr, fs.ErrNotExist), ShouldBeTrue)
				So(errors.Is(vaultErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestSkillsBrokenSymlinkNeverDeletesCanon(t *testing.T) {
	Convey("Given a canonical skill and a broken symlink in its agent dir", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, f.claudeSkill("beta"), "# beta\n")
		write(t, f.openCodeSkill("beta"), "# beta\n")
		f.sync(t)

		So(read(t, f.vaultSkill("beta")), ShouldEqual, "# beta\n")

		So(os.RemoveAll(filepath.Dir(f.claudeSkill("beta"))), ShouldBeNil)
		So(os.Symlink(filepath.Join(f.home, "missing-beta"), filepath.Dir(f.claudeSkill("beta"))), ShouldBeNil)

		Convey("When sync runs", func() {
			report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then it skips the unreadable skill with a warning and keeps the canon", func() {
				So(report.Errors(), ShouldBeEmpty)
				So(strings.Join(report.Kind(kind.Skills).Warnings, "\n"), ShouldContainSubstring, "broken symlink")
				So(report.ConflictsOf(kind.Skills), ShouldBeEmpty)

				So(read(t, f.vaultSkill("beta")), ShouldEqual, "# beta\n")
				So(read(t, f.openCodeSkill("beta")), ShouldEqual, "# beta\n")

				info, statErr := os.Lstat(filepath.Dir(f.claudeSkill("beta")))
				So(statErr, ShouldBeNil)
				So(info.Mode()&os.ModeSymlink, ShouldNotBeZeroValue)
			})
		})
	})
}

func TestPermissionsSync(t *testing.T) {
	Convey("Given two agents with permissions", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.config.Permissions = permission.ModeSync
		f.emptyConfigs(t)

		write(t, f.claudeSettings(), `{
  "permissions": {
    "defaultMode": "auto",
    "allow": ["Read", "Edit(~/topscan/**)"],
    "ask": ["Bash(git commit:*)"],
    "deny": []
  }
}`)
		write(t, f.openCodeConfig(), `{
  "mcp": {},
  "permission": {
    "read": "allow",
    "bash": {"*": "allow", "git commit*": "ask"},
    "codegraph_*": "allow"
  }
}`)

		report := f.sync(t)

		rules := f.rules(t)

		Convey("When it syncs", func() {
			Convey("Then portable rules merge and agent-only rules stay local", func() {
				So(report.Conflicts, ShouldBeEmpty)

				So(rules["tool:read"], ShouldEqual, permission.EffectAllow)
				So(rules["bash:git commit*"], ShouldEqual, permission.EffectAsk)
				So(rules["mcp:codegraph:*"], ShouldEqual, permission.EffectAllow)

				So(read(t, f.claudeSettings()), ShouldContainSubstring, "Edit(~/topscan/**)")
				So(read(t, f.claudeSettings()), ShouldContainSubstring, "mcp__codegraph__*")
				So(read(t, f.openCodeConfig()), ShouldContainSubstring, `"*": "allow"`)
			})
		})
	})
}

func TestPermissionsConflictTakeAgent(t *testing.T) {
	Convey("Given a permissions conflict", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.config.Permissions = permission.ModeSync
		f.emptyConfigs(t)

		write(t, f.claudeSettings(), `{"permissions": {"allow": [], "ask": ["Bash(git commit:*)"], "deny": []}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}, "permission": {"bash": {"git commit*": "ask"}}}`)
		f.sync(t)

		write(t, f.claudeSettings(), `{"permissions": {"allow": [], "ask": [], "deny": ["Bash(git commit:*)"]}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}, "permission": {"bash": {"git commit*": "allow"}}}`)

		report := f.sync(t)

		Convey("When it is resolved from the agent", func() {
			So(report.ConflictsOf(kind.Permissions), ShouldNotBeEmpty)

			f.resolve(t, kind.Permissions, agent.OpenCodeID, engine.TakeAgent)

			claudeSettings := read(t, f.claudeSettings())

			matchedAllow, matchErr := regexp.MatchString(`"allow":\s*\["Bash\(git commit:\*\)"\]`, claudeSettings)
			So(matchErr, ShouldBeNil)

			matchedDeny, matchErr := regexp.MatchString(`"deny":\s*\[\]`, claudeSettings)
			So(matchErr, ShouldBeNil)

			Convey("Then the agent value wins everywhere", func() {
				So(f.rules(t)["bash:git commit*"], ShouldEqual, permission.EffectAllow)
				So(read(t, f.openCodeConfig()), ShouldContainSubstring, `"git commit*": "allow"`)
				So(matchedAllow, ShouldBeTrue)
				So(matchedDeny, ShouldBeTrue)
			})
		})
	})
}

func TestPermissionsForeignKindsSurviveLimitedAgents(t *testing.T) {
	Convey("Given agents with different permission expressiveness", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.config.Permissions = permission.ModeSync
		f.emptyConfigs(t)

		write(t, f.claudeSettings(), `{"permissions": {"allow": ["WebFetch", "Bash(git status)"], "ask": [], "deny": []}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}, "permission": {"bash": {"git status": "allow"}, "codegraph_*": "allow"}}`)
		f.sync(t)

		f.config.Enable(agent.GeminiCLIID)
		f.config.Enable(agent.CursorID)
		write(t, f.geminiSettings(), `{"mcpServers": {}, "tools": {"allowed": ["run_shell_command(git status)"], "confirmationRequired": [], "exclude": []}}`)
		write(t, f.cursorCLIConfig(), `{"permissions": {"allow": ["Shell(git status)", "Mcp(codegraph:*)"], "deny": []}}`)

		Convey("When it syncs repeatedly", func() {
			for range 3 {
				f.sync(t)

				rules := f.rules(t)

				So(f.conflicts(t, kind.Permissions, agent.GeminiCLIID), ShouldBeEmpty)
				So(rules["tool:webfetch"], ShouldEqual, permission.EffectAllow)
				So(rules["mcp:codegraph:*"], ShouldEqual, permission.EffectAllow)
				So(rules["bash:git status"], ShouldEqual, permission.EffectAllow)
			}

			So(read(t, f.claudeSettings()), ShouldContainSubstring, "WebFetch")
			So(read(t, f.claudeSettings()), ShouldContainSubstring, "mcp__codegraph__*")

			report := f.sync(t)

			Convey("Then there is no perpetual push", func() {
				So(report.Action(kind.Permissions, agent.GeminiCLIID), ShouldEqual, engine.ActionNoop)
				So(report.Action(kind.Permissions, agent.CursorID), ShouldEqual, engine.ActionNoop)
			})
		})
	})
}

func TestPermissionsPushedTogetherWithMCP(t *testing.T) {
	Convey("Given a permissions change and an MCP change", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.config.Permissions = permission.ModeSync

		write(t, f.claudeConfig(), `{"mcpServers": {}}`)
		write(t, f.claudeSettings(), `{"permissions": {"allow": ["Bash(git status)"], "ask": [], "deny": []}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}, "permission": {"bash": {"git status": "allow"}}}`)
		f.sync(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "a"}}}`)
		write(t, f.claudeSettings(), `{"permissions": {"allow": ["Bash(git status)"], "ask": [], "deny": ["Bash(rm -rf:*)"]}}`)

		Convey("When both change", func() {
			f.sync(t)

			openCode := read(t, f.openCodeConfig())

			f.sync(t)

			Convey("Then both reach every agent together", func() {
				So(openCode, ShouldContainSubstring, "alpha")
				So(openCode, ShouldContainSubstring, "rm -rf*")
				So(f.rules(t)["bash:rm -rf*"], ShouldEqual, permission.EffectDeny)
			})
		})
	})
}

func TestBashPatternIsNotRewritten(t *testing.T) {
	Convey("Given a user-authored bash pattern", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.config.Permissions = permission.ModeSync

		write(t, f.claudeConfig(), `{"mcpServers": {}}`)
		write(t, f.claudeSettings(), `{"permissions": {"allow": [], "ask": [], "deny": []}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}, "permission": {"bash": {"git commit *": "ask"}}}`)

		Convey("When it syncs repeatedly", func() {
			for range 3 {
				f.sync(t)
			}

			Convey("Then the pattern is never rewritten", func() {
				So(read(t, f.openCodeConfig()), ShouldContainSubstring, `"git commit *"`)
				So(f.rules(t)["bash:git commit *"], ShouldEqual, permission.EffectAsk)
				So(read(t, f.claudeSettings()), ShouldContainSubstring, "Bash(git commit:*)")
			})
		})
	})
}

func TestNewAgentWithEmptyMCPDoesNotWipeVault(t *testing.T) {
	Convey("Given a newly detected agent with an empty MCP config", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "a"}, "beta": {"type": "stdio", "command": "b"}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)
		f.sync(t)
		So(f.servers(t), ShouldHaveLength, 2)

		f.config.Enable(agent.GeminiCLIID)
		write(t, f.geminiSettings(), `{"general": {"vimMode": false}}`)

		Convey("When it syncs", func() {
			f.sync(t)

			Convey("Then it only adds and never deletes", func() {
				So(f.servers(t), ShouldHaveLength, 2)
				So(read(t, f.claudeConfig()), ShouldContainSubstring, "alpha")
				So(read(t, f.geminiSettings()), ShouldContainSubstring, "alpha")
				So(read(t, f.geminiSettings()), ShouldContainSubstring, "vimMode")
			})
		})
	})
}

func TestReenabledStaleAgentDoesNotRollBack(t *testing.T) {
	Convey("Given a stale agent that is re-enabled", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "v1"}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)
		f.sync(t)

		f.config.Disable(agent.OpenCodeID)
		write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "v2"}, "gamma": {"type": "stdio", "command": "g"}}}`)
		f.sync(t)

		f.config.Enable(agent.OpenCodeID)

		Convey("When it syncs", func() {
			f.sync(t)

			servers := f.servers(t)

			Convey("Then it is updated instead of rolling back", func() {
				So(servers, ShouldContainKey, "gamma")
				So(servers["alpha"].Command, ShouldResemble, []string{"v2"})
				So(read(t, f.openCodeConfig()), ShouldContainSubstring, `"v2"`)
			})
		})
	})
}

func TestAgentSpecificFieldsSurvive(t *testing.T) {
	Convey("Given agent-specific MCP fields", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "a", "timeout": 5000}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {"alpha": {"type": "local", "command": ["a"], "enabled": false}}}`)

		Convey("When it syncs repeatedly", func() {
			for range 3 {
				f.sync(t)
			}

			report := f.sync(t)

			matched, matchErr := regexp.MatchString(`"enabled":\s*false`, read(t, f.openCodeConfig()))
			So(matchErr, ShouldBeNil)

			Convey("Then agent-only fields survive and a stable state is not pushed", func() {
				So(read(t, f.claudeConfig()), ShouldContainSubstring, "5000")
				So(matched, ShouldBeTrue)

				So(report.Action(kind.MCP, agent.ClaudeCodeID), ShouldEqual, engine.ActionNoop)
				So(report.Action(kind.MCP, agent.OpenCodeID), ShouldEqual, engine.ActionNoop)
			})
		})
	})
}

func TestSSETransportSurvivesLossyAgents(t *testing.T) {
	Convey("Given an SSE server and a lossy agent", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.config.Enable(agent.GeminiCLIID)

		write(t, f.claudeConfig(), `{"mcpServers": {}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)
		write(t, f.geminiSettings(), `{"mcpServers": {"legacy": {"url": "https://example.com/sse"}}}`)

		Convey("When it syncs repeatedly", func() {
			for range 3 {
				f.sync(t)
			}

			So(f.servers(t)["legacy"].Transport, ShouldEqual, mcp.TransportSSE)
			So(read(t, f.geminiSettings()), ShouldNotContainSubstring, "httpUrl")
			So(read(t, f.claudeConfig()), ShouldContainSubstring, `"sse"`)

			write(t, f.openCodeConfig(), `{"mcp": {"legacy": {"type": "remote", "url": "https://example.com/sse2"}}}`)
			f.sync(t)

			legacy := f.servers(t)["legacy"]

			Convey("Then the SSE transport is preserved", func() {
				So(legacy.URL, ShouldEqual, "https://example.com/sse2")
				So(legacy.Transport, ShouldEqual, mcp.TransportSSE)
			})
		})
	})
}

func TestDeletedRulesFileKeepsOtherAgents(t *testing.T) {
	Convey("Given one deleted rules file", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		write(t, f.claudeRules(), "# important\n")
		write(t, f.openCodeRules(), "# important\n")
		f.sync(t)

		So(os.Remove(f.openCodeRules()), ShouldBeNil)

		Convey("When it syncs repeatedly", func() {
			for range 2 {
				f.sync(t)
			}

			Convey("Then the canon and the other agent keep the rules", func() {
				So(read(t, f.claudeRules()), ShouldEqual, "# important\n")
				So(read(t, f.vault.RulesPath()), ShouldEqual, "# important\n")
			})
		})
	})
}

func TestResolveKeepsUnpulledAgentChanges(t *testing.T) {
	Convey("Given an unpulled MCP change during a rules resolve", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "a"}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)
		write(t, f.claudeRules(), "# shared\n")
		write(t, f.openCodeRules(), "# shared\n")
		f.sync(t)

		write(t, f.claudeRules(), "# claude\n")
		write(t, f.openCodeRules(), "# opencode\n")
		f.sync(t)

		write(t, f.openCodeConfig(), `{"mcp": {"alpha": {"type": "local", "command": ["a"]}, "fresh": {"type": "local", "command": ["f"]}}}`)

		Convey("When the rules conflict is resolved", func() {
			f.resolve(t, kind.Rules, agent.OpenCodeID, engine.TakeVault)

			Convey("Then the unpulled MCP change still propagates", func() {
				So(read(t, f.openCodeConfig()), ShouldContainSubstring, "fresh")
				So(read(t, f.claudeConfig()), ShouldContainSubstring, "fresh")
				So(read(t, f.openCodeRules()), ShouldEqual, "# claude\n")
			})
		})
	})
}

func TestGeminiSettingsWithComments(t *testing.T) {
	Convey("Given a Gemini settings file with comments", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.config.Enable(agent.GeminiCLIID)

		write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "a"}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)
		write(t, f.geminiSettings(), "{\n  // personal settings\n  \"mcpServers\": {}\n}\n")

		Convey("When it syncs", func() {
			f.sync(t)

			Convey("Then comments survive", func() {
				So(read(t, f.geminiSettings()), ShouldContainSubstring, "personal settings")
				So(read(t, f.geminiSettings()), ShouldContainSubstring, "alpha")
			})
		})
	})
}

func TestSymlinkedRulesFileIsAnAlias(t *testing.T) {
	Convey("Given a symlinked rules file", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, f.claudeConfig(), `{"mcpServers": {}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)
		write(t, f.claudeRules(), "# v1\n")
		So(os.Symlink(f.claudeRules(), f.openCodeRules()), ShouldBeNil)

		report := f.sync(t)

		Convey("When the canon changes", func() {
			So(report.Action(kind.Rules, agent.OpenCodeID), ShouldEqual, engine.ActionAlias)

			write(t, f.vault.RulesPath(), "# v2\n")
			f.sync(t)

			info, err := os.Lstat(f.openCodeRules())

			Convey("Then the symlink is never replaced", func() {
				So(err, ShouldBeNil)
				So(info.Mode()&os.ModeSymlink, ShouldNotBeZeroValue)
				So(read(t, f.claudeRules()), ShouldEqual, "# v2\n")
				So(read(t, f.openCodeRules()), ShouldEqual, "# v2\n")
			})
		})
	})
}

func TestMassDeletionWaitsForConfirmation(t *testing.T) {
	Convey("Given a mass deletion of MCP servers", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		servers := make([]string, 0, 5)
		for i := range 5 {
			servers = append(servers, fmt.Sprintf(`"s%d": {"type": "stdio", "command": "c%d"}`, i, i))
		}

		write(t, f.claudeConfig(), `{"mcpServers": {`+strings.Join(servers, ",")+`}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)
		f.sync(t)

		write(t, f.claudeConfig(), `{"mcpServers": {}}`)

		Convey("When the mass deletion is synced", func() {
			f.sync(t)

			conflicts := f.conflicts(t, kind.MCP, agent.ClaudeCodeID)

			So(f.servers(t), ShouldHaveLength, 5)
			So(conflicts, ShouldHaveLength, 5)
			So(conflicts[0].Reason, ShouldEqual, state.ReasonMassDelete)
			So(read(t, f.openCodeConfig()), ShouldContainSubstring, "s4")

			ids := make([]string, 0, len(conflicts))
			for _, c := range conflicts {
				ids = append(ids, c.ID())
			}

			_, err := f.engine.Resolve(t.Context(), ids, engine.Resolution{Take: engine.TakeAgent})

			Convey("Then confirming applies the deletion everywhere", func() {
				So(err, ShouldBeNil)
				So(f.servers(t), ShouldBeEmpty)
				So(read(t, f.openCodeConfig()), ShouldNotContainSubstring, "s4")
			})
		})
	})
}

func TestModeOffIgnoresAgentKind(t *testing.T) {
	Convey("Given a mode-off agent kind", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.config.SetMode(agent.OpenCodeID, kind.MCP, config.ModeOff)

		write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "a"}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {"beta": {"type": "local", "command": ["b"]}}}`)

		Convey("When it syncs", func() {
			f.sync(t)

			Convey("Then the off agent is neither read nor written", func() {
				So(f.servers(t), ShouldNotContainKey, "beta")
				So(read(t, f.openCodeConfig()), ShouldNotContainSubstring, "alpha")
			})
		})
	})
}

func TestPushAndPullDirections(t *testing.T) {
	Convey("Given push and pull directions", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "v1"}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)
		f.sync(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "v2"}}}`)

		Convey("When a push then a pull runs", func() {
			f.run(t, engine.SyncOptions{Direction: config.ModePush})

			So(read(t, f.claudeConfig()), ShouldContainSubstring, `"v1"`)
			So(f.servers(t)["alpha"].Command, ShouldResemble, []string{"v1"})

			write(t, f.openCodeConfig(), `{"mcp": {"alpha": {"type": "local", "command": ["v1"]}, "fresh": {"type": "local", "command": ["f"]}}}`)
			f.run(t, engine.SyncOptions{Direction: config.ModePull})

			Convey("Then push keeps the canon and pull adopts agent changes", func() {
				So(f.servers(t), ShouldContainKey, "fresh")
				So(read(t, f.claudeConfig()), ShouldNotContainSubstring, "fresh")
			})
		})
	})
}

func TestDryRunWritesNothing(t *testing.T) {
	Convey("Given a dry run", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		original := `{"mcp": {}}`

		write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "a"}}}`)
		write(t, f.openCodeConfig(), original)

		report := f.run(t, engine.SyncOptions{DryRun: true})

		_, serversErr := os.Stat(f.vault.ServersPath())
		_, stateErr := os.Stat(f.vault.StatePath())

		Convey("When it previews", func() {
			Convey("Then nothing is written", func() {
				So(report.DryRun, ShouldBeTrue)
				So(report.Action(kind.MCP, agent.OpenCodeID), ShouldEqual, engine.ActionWouldPush)
				So(report.VaultChanged(), ShouldBeTrue)

				So(read(t, f.openCodeConfig()), ShouldEqual, original)
				So(errors.Is(serversErr, fs.ErrNotExist), ShouldBeTrue)
				So(errors.Is(stateErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestLockSerializesProcesses(t *testing.T) {
	Convey("Given a held lock", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		release, err := lock.Acquire(t.Context(), f.vault.LockPath())
		So(err, ShouldBeNil)

		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()

		Convey("When a sync tries to run", func() {
			_, err = f.engine.Sync(ctx, engine.SyncOptions{})

			Convey("Then it is busy, and releasing lets it through", func() {
				So(errors.Is(err, lock.ErrBusy), ShouldBeTrue)

				So(release(), ShouldBeNil)
				f.sync(t)
			})
		})
	})
}

func TestDoctorFlagsSecretLikeRules(t *testing.T) {
	Convey("Given a rules canon holding a token literal", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, f.vault.RulesPath(), "deploy token: ghp_abcdefghijklmnopqrstuvwxyz012345\n")

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the rules gap is reported", func() {
				So(hasIssue(issues, engine.SeverityWarn, "gate covers mcp, memory and project files only"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityWarn, f.vault.RulesPath()), ShouldBeTrue)
			})
		})
	})
}

func TestDoctor(t *testing.T) {
	Convey("Given a synced fixture", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		write(t, f.claudeSkill("alpha"), "a\n")
		write(t, f.openCodeSkill("beta"), "b\n")
		f.sync(t)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When issues are introduced", func() {
			for _, issue := range issues {
				So(issue.Severity, ShouldNotEqual, engine.SeverityError)
				So(issue.Severity, ShouldNotEqual, engine.SeverityWarn)
			}

			So(os.Chmod(f.vault.ConfigPath(), 0o644), ShouldBeNil) //nolint:gosec // G302: loosened on purpose to test the warning
			So(os.Symlink(filepath.Join(f.home, "missing"), filepath.Join(f.home, ".claude", "skills", "broken")), ShouldBeNil)
			So(os.MkdirAll(filepath.Join(f.home, ".claude", "skills", "Foo"), 0o750), ShouldBeNil)
			write(t, f.vaultSkill("foo"), "x\n")
			write(t, f.claudeRules(), "# drifted\n")

			issues, err = f.engine.Doctor(t.Context())

			Convey("Then every problem is reported", func() {
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityWarn, "expected 0600"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityError, "broken symlink"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityError, "collision"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityWarn, "not in the vault yet"), ShouldBeTrue)
			})
		})
	})
}

func TestDoctorNoFalseDriftForLimitedAgents(t *testing.T) {
	Convey("Given agents with structural gaps", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.config.Permissions = permission.ModeSync
		f.config.Enable(agent.GeminiCLIID)
		f.emptyConfigs(t)

		write(t, f.claudeSettings(), `{"permissions": {"allow": ["WebFetch", "Bash(git status)"], "ask": [], "deny": []}}`)
		write(t, f.geminiSettings(), `{"mcpServers": {}, "tools": {"allowed": ["run_shell_command(git status)"]}}`)

		Convey("When it syncs repeatedly", func() {
			for range 2 {
				f.sync(t)
			}

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then structural gaps are not reported as drift", func() {
				for _, issue := range issues {
					So(issue.Severity, ShouldNotEqual, engine.SeverityWarn)
				}
			})
		})
	})
}

func TestDoctorProjectScope(t *testing.T) {
	Convey("Given a project scope with collisions", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"proj-srv": {"type": "stdio", "command": "vault-cmd"}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)
		write(t, f.claudeRules(), "# r\n")
		write(t, f.openCodeRules(), "# r\n")
		f.sync(t)

		repo := t.TempDir()
		t.Chdir(repo)

		dir, err := os.Getwd()
		So(err, ShouldBeNil)

		write(t, filepath.Join(repo, ".mcp.json"), `{"mcpServers": {"proj-srv": {"type": "stdio", "command": "repo-cmd"}, "repo-only": {"type": "stdio", "command": "repo"}}}`)
		write(t, f.claudeConfig(), fmt.Sprintf(`{"mcpServers": {"proj-srv": {"type": "stdio", "command": "vault-cmd"}},
			"projects": {%q: {"mcpServers": {"local-srv": {"type": "stdio", "command": "local"}}}}}`, dir))

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When doctor runs", func() {
			Convey("Then collisions and project-only servers are reported", func() {
				So(hasIssue(issues, engine.SeverityWarn, "proj-srv"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityInfo, "repo-only"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityInfo, "local-srv"), ShouldBeTrue)
			})
		})
	})
}

func TestSecretsExtractedFromLiteralAndPushedBack(t *testing.T) {
	Convey("Given a literal secret in a Claude header", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"ctx7": {"type": "http", "url": "https://mcp.example.com",
		  "headers": {"Authorization": "Bearer abc123", "Accept": "application/json"}}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)

		report := f.sync(t)

		Convey("When the secret gate runs", func() {
			value, ok := f.engine.Secrets().Get("AUTHORIZATION")

			info, err := os.Stat(f.vault.SecretsPath())
			So(err, ShouldBeNil)

			report = f.sync(t)

			Convey("Then the canon holds a ref, the store holds the value and sync settles", func() {
				So(report.Conflicts, ShouldBeEmpty)

				So(read(t, f.vault.ServersPath()), ShouldNotContainSubstring, "abc123")
				So(read(t, f.vault.ServersPath()), ShouldContainSubstring, "{secret:AUTHORIZATION}")

				So(ok, ShouldBeTrue)
				So(value, ShouldEqual, "Bearer abc123")
				So(info.Mode().Perm(), ShouldEqual, os.FileMode(0o600))

				So(read(t, f.openCodeConfig()), ShouldContainSubstring, "abc123")
				So(read(t, f.openCodeConfig()), ShouldNotContainSubstring, "{secret:")
				So(read(t, f.openCodeConfig()), ShouldContainSubstring, "application/json")

				So(report.VaultChanged(), ShouldBeFalse)
				So(report.Action(kind.MCP, agent.ClaudeCodeID), ShouldEqual, engine.ActionNoop)
				So(report.Action(kind.MCP, agent.OpenCodeID), ShouldEqual, engine.ActionNoop)
			})
		})
	})
}

func TestMCPBrokenCanonServerIsIsolated(t *testing.T) {
	Convey("Given a canon with one undecodable and one good server", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.config.Enable(agent.CodexID)
		f.emptyConfigs(t)

		codexConfig := filepath.Join(f.home, ".codex", "config.toml")
		write(t, codexConfig, "model = \"gpt\"\n")
		write(t, f.vault.ServersPath(), `{"gh":{"type":"stdio","command":"gh-mcp"},"ok":{"transport":"stdio","command":["ok-mcp"]}}`)

		Convey("When sync runs", func() {
			report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then the good server is written and the broken one is reported once", func() {
				errs := report.Errors()
				So(errs, ShouldHaveLength, 1)
				So(errs[0], ShouldContainSubstring, "server gh")
				So(errs[0], ShouldContainSubstring, f.vault.ServersPath())
				So(errs[0], ShouldNotContainSubstring, codexConfig)

				result, ok := report.Kind(kind.MCP).Agent(agent.ClaudeCodeID)
				So(ok, ShouldBeTrue)
				So(result.Action, ShouldEqual, engine.ActionPushed)

				So(read(t, f.claudeConfig()), ShouldContainSubstring, "ok-mcp")
				So(read(t, f.claudeConfig()), ShouldNotContainSubstring, "gh-mcp")
				So(read(t, codexConfig), ShouldContainSubstring, "ok-mcp")
				So(read(t, f.vault.ServersPath()), ShouldContainSubstring, `"command":"gh-mcp"`)

				again, err := f.engine.Sync(t.Context(), engine.SyncOptions{DryRun: true})
				So(err, ShouldBeNil)
				So(again.Errors(), ShouldHaveLength, 1)
				So(again.Action(kind.MCP, agent.ClaudeCodeID), ShouldEqual, engine.ActionNoop)
			})
		})

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the canon server is an error naming the canon path", func() {
				So(hasIssue(issues, engine.SeverityError, "server gh"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityError, f.vault.ServersPath()), ShouldBeTrue)
			})
		})
	})
}

func TestNonCreatableSurfaceSkipNamesTheFile(t *testing.T) {
	Convey("Given a detected OpenCode without its config file", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		So(os.Remove(f.openCodeConfig()), ShouldBeNil)

		write(t, f.vault.ServersPath(), `{"demo":{"transport":"stdio","command":["demo-mcp"]}}`)

		Convey("When sync runs", func() {
			report := f.sync(t)

			result, ok := report.Kind(kind.MCP).Agent(agent.OpenCodeID)
			So(ok, ShouldBeTrue)

			Convey("Then the skip note names the missing file", func() {
				So(result.Action, ShouldEqual, engine.ActionSkipped)
				So(result.Note, ShouldContainSubstring, filepath.Join(f.home, ".config", "opencode", "opencode.json"))
			})
		})
	})
}

func TestMCPSecretEnvKeyMismatchConvergesToNoop(t *testing.T) {
	Convey("Given a canon ref whose name differs from the agent env key", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		f.engine.Secrets().Set("GH_TOKEN", "ghp_abcdefghijklmnopqrstuvwxyz012345")
		So(f.engine.Secrets().Save(), ShouldBeNil)

		write(t, f.vault.ServersPath(), `{"gh":{"transport":"stdio","command":["gh-mcp"],"env":{"GITHUB_TOKEN":"{secret:GH_TOKEN}"}}}`)

		f.sync(t)

		Convey("When it syncs again", func() {
			report := f.sync(t)

			Convey("Then it is a noop and the value keeps one name", func() {
				So(report.Kind(kind.MCP).VaultChanged, ShouldBeFalse)
				So(report.Action(kind.MCP, agent.ClaudeCodeID), ShouldEqual, engine.ActionNoop)
				So(report.Action(kind.MCP, agent.OpenCodeID), ShouldEqual, engine.ActionNoop)
				So(f.engine.Secrets().Names(), ShouldResemble, []string{"GH_TOKEN"})
			})
		})
	})
}

func TestMCPSecretHeaderConvergesToNoop(t *testing.T) {
	Convey("Given a remote canon server with a secret in headers", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		f.engine.Secrets().Set("X_API_KEY", "plain-secret-value-1234")
		So(f.engine.Secrets().Save(), ShouldBeNil)

		write(t, f.vault.ServersPath(), `{"docs":{"transport":"http","url":"https://d.example/mcp","headers":{"X-Api-Key":"{secret:X_API_KEY}"}}}`)

		f.sync(t)

		Convey("When it syncs again", func() {
			report := f.sync(t)

			Convey("Then it is a noop and the store is unchanged", func() {
				So(report.Kind(kind.MCP).VaultChanged, ShouldBeFalse)
				So(report.Action(kind.MCP, agent.ClaudeCodeID), ShouldEqual, engine.ActionNoop)
				So(f.engine.Secrets().Names(), ShouldResemble, []string{"X_API_KEY"})
			})
		})
	})
}

func TestSecretsMissingValueSkipsPushWithoutCorruption(t *testing.T) {
	Convey("Given a canon ref with no stored value", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, f.vault.ServersPath(), `{"ctx7":{"transport":"http","url":"https://mcp.example.com","headers":{"Authorization":"{secret:AUTHORIZATION}"}}}`)
		write(t, f.claudeConfig(), `{"mcpServers": {}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)

		report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
		So(err, ShouldBeNil)

		result, ok := report.Kind(kind.MCP).Agent(agent.OpenCodeID)
		So(ok, ShouldBeTrue)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When it syncs", func() {
			Convey("Then the push is skipped without corruption and doctor errors", func() {
				So(result.Action, ShouldEqual, engine.ActionSkipped)
				So(result.Note, ShouldContainSubstring, "AUTHORIZATION")
				So(read(t, f.openCodeConfig()), ShouldEqualJSON, `{"mcp": {}}`)
				So(hasIssue(issues, engine.SeverityError, "AUTHORIZATION"), ShouldBeTrue)
			})
		})
	})
}

func TestSecretsEnvModeRendersReference(t *testing.T) {
	Convey("Given env secret mode", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.config.Secrets = secret.ModeEnv

		write(t, f.claudeConfig(), `{"mcpServers": {"ctx7": {"type": "http", "url": "https://mcp.example.com", "headers": {"Authorization": "Bearer abc123"}}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)

		Convey("When it syncs", func() {
			f.sync(t)

			openCode := read(t, f.openCodeConfig())

			Convey("Then the env reference is rendered, never the literal", func() {
				So(openCode, ShouldContainSubstring, "{env:AUTHORIZATION}")
				So(openCode, ShouldNotContainSubstring, "abc123")
			})
		})
	})
}

func TestSecretsModeSwitchRewritesFiles(t *testing.T) {
	Convey("Given a secret mode switch", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"ctx7": {"type": "http", "url": "https://mcp.example.com", "headers": {"Authorization": "Bearer abc123"}}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)
		f.sync(t)

		So(read(t, f.openCodeConfig()), ShouldContainSubstring, "abc123")

		plan, err := f.engine.Sync(t.Context(), engine.SyncOptions{DryRun: true})
		So(err, ShouldBeNil)

		Convey("When the mode flips to env", func() {
			So(plan.Action(kind.MCP, agent.ClaudeCodeID), ShouldEqual, engine.ActionNoop)

			f.config.Secrets = secret.ModeEnv

			plan, err = f.engine.Sync(t.Context(), engine.SyncOptions{DryRun: true})
			So(err, ShouldBeNil)

			mcpPlan := plan.Kind(kind.MCP)
			So(mcpPlan, ShouldNotBeNil)

			result, ok := mcpPlan.Agent(agent.OpenCodeID)
			So(ok, ShouldBeTrue)

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			report := f.sync(t)

			openCode := read(t, f.openCodeConfig())

			settled := f.sync(t)

			So(settled.Action(kind.MCP, agent.ClaudeCodeID), ShouldEqual, engine.ActionNoop)
			So(settled.Action(kind.MCP, agent.OpenCodeID), ShouldEqual, engine.ActionNoop)

			f.config.Secrets = secret.ModeLiteral

			backToLiteral := f.sync(t)

			Convey("Then the switch rewrites files, leaks nothing and settles", func() {
				So(result.Action, ShouldEqual, engine.ActionWouldPush)
				So(result.Changes, ShouldNotBeEmpty)
				So(reportChanges(t, result), ShouldNotContainSubstring, "abc123")

				So(hasIssue(issues, engine.SeverityWarn, "differs from the vault"), ShouldBeTrue)

				So(report.Action(kind.MCP, agent.ClaudeCodeID), ShouldEqual, engine.ActionPushed)
				So(report.Action(kind.MCP, agent.OpenCodeID), ShouldEqual, engine.ActionPushed)

				So(openCode, ShouldContainSubstring, "{env:AUTHORIZATION}")
				So(openCode, ShouldNotContainSubstring, "abc123")

				So(report.Action(kind.MCP, agent.OpenCodeID), ShouldEqual, engine.ActionPushed)
				So(backToLiteral.Action(kind.MCP, agent.OpenCodeID), ShouldEqual, engine.ActionPushed)
				So(read(t, f.openCodeConfig()), ShouldContainSubstring, "abc123")
			})
		})
	})
}

func TestSecretsPrune(t *testing.T) {
	Convey("Given an orphan secret", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"ctx7": {"type": "http", "url": "https://mcp.example.com", "headers": {"Authorization": "Bearer abc123"}}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)
		f.sync(t)

		write(t, f.claudeConfig(), `{"mcpServers": {}}`)

		Convey("When the server is removed and pruned", func() {
			f.sync(t)

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			removed, err := f.engine.PruneSecrets(t.Context())

			Convey("Then the orphan is reported and pruned", func() {
				So(hasIssue(issues, engine.SeverityInfo, "AUTHORIZATION"), ShouldBeTrue)

				So(err, ShouldBeNil)
				So(removed, ShouldResemble, []string{"AUTHORIZATION"})
				So(f.engine.Secrets().Has("AUTHORIZATION"), ShouldBeFalse)
			})
		})
	})
}

func TestSecretOfAnOpenConflictIsInUse(t *testing.T) {
	Convey("Given a secret referenced by an open conflict", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"ctx7": {"type": "http", "url": "https://mcp.example.com", "headers": {"API_KEY": "key-one"}}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {"ctx7": {"type": "remote", "url": "https://mcp.example.com", "headers": {"API_KEY": "key-two"}}}}`)
		f.sync(t)

		Convey("When prune and resolve run", func() {
			So(f.conflicts(t, kind.MCP, agent.OpenCodeID), ShouldHaveLength, 1)

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			removed, err := f.engine.PruneSecrets(t.Context())
			So(err, ShouldBeNil)

			f.resolve(t, kind.MCP, agent.OpenCodeID, engine.TakeAgent)

			Convey("Then the conflict keeps the secret in use", func() {
				So(hasIssue(issues, engine.SeverityInfo, "is unused"), ShouldBeFalse)
				So(removed, ShouldBeEmpty)
				So(read(t, f.claudeConfig()), ShouldContainSubstring, "key-two")
			})
		})
	})
}

func TestEnvRefsCrossAgent(t *testing.T) {
	Convey("Given a Claude env ref", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"s": {"type": "http", "url": "https://example.com", "headers": {"Authorization": "Bearer ${TOKEN}"}}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)

		Convey("When it syncs", func() {
			f.sync(t)

			Convey("Then the ref is translated to the OpenCode syntax", func() {
				So(read(t, f.openCodeConfig()), ShouldContainSubstring, "{env:TOKEN}")
				So(read(t, f.vault.ServersPath()), ShouldContainSubstring, "{env:TOKEN}")
			})
		})
	})
}

func TestGeminiSync(t *testing.T) {
	Convey("Given a Gemini agent with servers, rules and skills", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.config.Enable(agent.GeminiCLIID)

		write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "a"}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)
		write(t, f.claudeRules(), "# shared\n")
		write(t, f.openCodeRules(), "# shared\n")
		write(t, f.geminiSettings(), `{"mcpServers": {"g-srv": {"command": "g"}}}`)
		write(t, filepath.Join(f.home, ".gemini", "GEMINI.md"), "# shared\n")
		write(t, filepath.Join(f.home, ".gemini", "skills", "g-skill", "SKILL.md"), "g\n")

		Convey("When it syncs", func() {
			report := f.sync(t)

			Convey("Then servers and skills fan out", func() {
				So(report.Conflicts, ShouldBeEmpty)

				So(read(t, f.geminiSettings()), ShouldContainSubstring, "alpha")
				So(read(t, f.claudeConfig()), ShouldContainSubstring, "g-srv")

				_, vaultErr := os.Stat(f.vaultSkill("g-skill"))
				_, claudeErr := os.Stat(f.claudeSkill("g-skill"))

				So(vaultErr, ShouldBeNil)
				So(claudeErr, ShouldBeNil)
			})
		})
	})
}

func TestRestore(t *testing.T) {
	Convey("Given restored versions of rules and skills", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		write(t, f.claudeRules(), "# base\n")
		write(t, f.openCodeRules(), "# base\n")
		write(t, f.claudeSkill("alpha"), "a\n")
		f.sync(t)

		write(t, f.vault.RulesPath(), "# edited\n")

		Convey("When rules are restored", func() {
			f.sync(t)
			So(read(t, f.claudeRules()), ShouldEqual, "# edited\n")

			_, err := f.engine.Restore(t.Context(), kind.Rules, -2)
			So(err, ShouldBeNil)

			write(t, f.vaultSkill("extra"), "x\n")
			f.sync(t)
			So(read(t, f.claudeSkill("extra")), ShouldEqual, "x\n")

			_, err = f.engine.Restore(t.Context(), kind.Skills, -2)
			So(err, ShouldBeNil)

			Convey("Then rules and skills roll back", func() {
				So(read(t, f.vault.RulesPath()), ShouldEqual, "# base\n")
				So(read(t, f.claudeRules()), ShouldEqual, "# base\n")
				So(read(t, f.openCodeRules()), ShouldEqual, "# base\n")

				_, extraVaultErr := os.Stat(f.vaultSkill("extra"))
				_, extraClaudeErr := os.Stat(f.claudeSkill("extra"))
				_, alphaErr := os.Stat(f.vaultSkill("alpha"))

				So(errors.Is(extraVaultErr, fs.ErrNotExist), ShouldBeTrue)
				So(errors.Is(extraClaudeErr, fs.ErrNotExist), ShouldBeTrue)
				So(alphaErr, ShouldBeNil)
			})
		})
	})
}

func TestGitHistory(t *testing.T) {
	Convey("Given a vault in git history mode", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git is not installed")
		}

		t.Setenv("GIT_AUTHOR_NAME", "beadle test")
		t.Setenv("GIT_AUTHOR_EMAIL", "beadle@example.invalid")
		t.Setenv("GIT_COMMITTER_NAME", "beadle test")
		t.Setenv("GIT_COMMITTER_EMAIL", "beadle@example.invalid")

		f := newFixture(t)
		f.config.History = config.HistoryGit
		f.emptyConfigs(t)
		write(t, f.claudeRules(), "# v1\n")
		write(t, f.openCodeRules(), "# v1\n")

		f.sync(t)

		first := gitLog(t, f.vault.Root())

		Convey("When it syncs without and with changes", func() {
			f.sync(t)
			So(gitLog(t, f.vault.Root()), ShouldEqual, first)

			write(t, f.claudeRules(), "# v2\n")
			f.sync(t)

			Convey("Then only a real change creates a commit", func() {
				So(first, ShouldContainSubstring, "beadle: sync")
				So(gitLog(t, f.vault.Root()), ShouldNotEqual, first)
			})
		})
	})
}
