package engine_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/agent"
	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/engine"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
	"github.com/odiumuniverse/agents-sync/pkg/lock"
	"github.com/odiumuniverse/agents-sync/pkg/mcp"
	"github.com/odiumuniverse/agents-sync/pkg/permission"
	"github.com/odiumuniverse/agents-sync/pkg/secret"
	"github.com/odiumuniverse/agents-sync/pkg/state"
	"github.com/odiumuniverse/agents-sync/pkg/vault"
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

	require.NoError(t, v.Init())

	cfg, err := config.Load(v.ConfigPath())
	require.NoError(t, err)

	cfg.Enable(agent.ClaudeCodeID)
	cfg.Enable(agent.OpenCodeID)
	require.NoError(t, cfg.Save(v.ConfigPath()))

	e, err := engine.New(v, cfg, agent.All(home), engine.WithHome(home))
	require.NoError(t, err)

	return &fixture{home: home, vault: v, config: cfg, engine: e}
}

func (f *fixture) run(t *testing.T, opts engine.SyncOptions) *engine.Report {
	t.Helper()

	report, err := f.engine.Sync(t.Context(), opts)
	require.NoError(t, err)
	require.Empty(t, report.Errors())

	return report
}

func (f *fixture) sync(t *testing.T) *engine.Report {
	t.Helper()

	return f.run(t, engine.SyncOptions{})
}

func (f *fixture) conflicts(t *testing.T, k kind.ID, agentID string) []state.Conflict {
	t.Helper()

	all, err := f.engine.Conflicts()
	require.NoError(t, err)

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
	require.Len(t, conflicts, 1)

	return conflicts[0]
}

func (f *fixture) resolve(t *testing.T, k kind.ID, agentID string, take engine.Take) {
	t.Helper()

	c := f.conflict(t, k, agentID)

	_, err := f.engine.Resolve(t.Context(), []string{c.ID()}, engine.Resolution{Take: take})
	require.NoError(t, err)
}

func (f *fixture) servers(t *testing.T) mcp.Servers {
	t.Helper()

	servers, err := mcp.ParseCanonical([]byte(read(t, f.vault.ServersPath())))
	require.NoError(t, err)

	return servers
}

func (f *fixture) rules(t *testing.T) permission.Rules {
	t.Helper()

	rules, err := permission.Parse([]byte(read(t, f.vault.PermissionsPath())))
	require.NoError(t, err)

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

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func read(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: tests read their own temp files
	require.NoError(t, err)

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

func TestSyncUnionAndIdempotency(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	write(t, f.claudeConfig(), `{"numStartups": 7, "mcpServers": {"alpha": {"type": "stdio", "command": "cmd", "args": ["a"]}}}`)
	write(t, f.claudeRules(), "# shared\n")
	write(t, f.openCodeConfig(), "{\n  // keep\n  \"$schema\": \"https://opencode.ai/config.json\",\n  \"mcp\": {\"beta\": {\"type\": \"local\", \"command\": [\"b\"]}}\n}")
	write(t, f.openCodeRules(), "# shared\n")

	report := f.sync(t)
	require.Empty(t, report.Conflicts)
	require.Equal(t, "# shared\n", read(t, f.vault.RulesPath()))

	servers := f.servers(t)
	require.Len(t, servers, 2)
	require.Contains(t, servers, "alpha")
	require.Contains(t, servers, "beta")

	claude := read(t, f.claudeConfig())
	require.Contains(t, claude, "beta")
	require.Contains(t, claude, "numStartups")

	openCode := read(t, f.openCodeConfig())
	require.Contains(t, openCode, "alpha")
	require.Contains(t, openCode, "// keep")

	report = f.sync(t)
	require.False(t, report.VaultChanged())
	require.False(t, report.Pushed())
	require.Equal(t, engine.ActionNoop, report.Action(kind.Rules, agent.ClaudeCodeID))
	require.Equal(t, engine.ActionNoop, report.Action(kind.MCP, agent.ClaudeCodeID))
	require.Equal(t, engine.ActionNoop, report.Action(kind.MCP, agent.OpenCodeID))
}

func TestRulesConflictResolvedInEditor(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	f.sync(t)

	write(t, f.claudeRules(), "# claude\n")
	write(t, f.openCodeRules(), "# opencode\n")

	report := f.sync(t)
	require.Len(t, report.ConflictsOf(kind.Rules), 1)

	require.Equal(t, "# claude\n", read(t, f.vault.RulesPath()))
	require.Equal(t, "# claude\n", read(t, f.claudeRules()))
	require.Equal(t, "# opencode\n", read(t, f.openCodeRules()))

	c := f.conflict(t, kind.Rules, agent.OpenCodeID)

	file, err := f.engine.ConflictFile(c)
	require.NoError(t, err)
	require.Contains(t, read(t, file), "<<<<<<< vault")
	require.Contains(t, read(t, file), ">>>>>>> agent:opencode")

	write(t, file, "# resolved\n")

	_, err = f.engine.Resolve(t.Context(), []string{c.ID()}, engine.Resolution{Take: engine.TakeFile})
	require.NoError(t, err)

	require.Equal(t, "# resolved\n", read(t, f.claudeRules()))
	require.Equal(t, "# resolved\n", read(t, f.openCodeRules()))
	require.NoFileExists(t, file)
	require.Empty(t, f.conflicts(t, kind.Rules, agent.OpenCodeID))
}

func TestResolveRejectsLeftoverMarkers(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	f.sync(t)

	write(t, f.claudeRules(), "# claude\n")
	write(t, f.openCodeRules(), "# opencode\n")
	f.sync(t)

	c := f.conflict(t, kind.Rules, agent.OpenCodeID)

	_, err := f.engine.Resolve(t.Context(), []string{c.ID()}, engine.Resolution{Take: engine.TakeFile})
	require.ErrorContains(t, err, "conflict markers")
	require.Len(t, f.conflicts(t, kind.Rules, agent.OpenCodeID), 1)
}

func TestMCPConflictTakeAgent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	write(t, f.claudeConfig(), `{"mcpServers": {"gamma": {"type": "stdio", "command": "v1"}}}`)
	write(t, f.openCodeConfig(), `{"mcp": {}}`)

	f.sync(t)

	write(t, f.claudeConfig(), `{"mcpServers": {"gamma": {"type": "stdio", "command": "v2"}}}`)
	write(t, f.openCodeConfig(), `{"mcp": {"gamma": {"type": "local", "command": ["v3"]}}}`)

	report := f.sync(t)
	require.NotEmpty(t, report.ConflictsOf(kind.MCP))
	require.Equal(t, []string{"v2"}, f.servers(t)["gamma"].Command)

	f.resolve(t, kind.MCP, agent.OpenCodeID, engine.TakeAgent)

	require.Equal(t, []string{"v3"}, f.servers(t)["gamma"].Command)
	require.Contains(t, read(t, f.claudeConfig()), "v3")
	require.Contains(t, read(t, f.openCodeConfig()), "v3")
}

func TestMCPConflictTakeVault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	write(t, f.claudeConfig(), `{"mcpServers": {"gamma": {"type": "stdio", "command": "v1"}}}`)
	write(t, f.openCodeConfig(), `{"mcp": {}}`)
	f.sync(t)

	write(t, f.claudeConfig(), `{"mcpServers": {"gamma": {"type": "stdio", "command": "v2"}}}`)
	write(t, f.openCodeConfig(), `{"mcp": {"gamma": {"type": "local", "command": ["v3"]}}}`)
	f.sync(t)

	f.resolve(t, kind.MCP, agent.OpenCodeID, engine.TakeVault)

	require.Equal(t, []string{"v2"}, f.servers(t)["gamma"].Command)
	require.Contains(t, read(t, f.openCodeConfig()), `"v2"`)
	require.NotContains(t, read(t, f.openCodeConfig()), "v3")
}

func TestMCPConflictDoesNotFreezeOtherServers(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	write(t, f.claudeConfig(), `{"mcpServers": {"gamma": {"type": "stdio", "command": "v1"}, "alpha": {"type": "stdio", "command": "a"}}}`)
	write(t, f.openCodeConfig(), `{"mcp": {}}`)
	f.sync(t)

	write(t, f.claudeConfig(), `{"mcpServers": {"gamma": {"type": "stdio", "command": "v2"}, "alpha": {"type": "stdio", "command": "a"}}}`)
	write(t, f.openCodeConfig(), `{"mcp": {"alpha": {"type": "local", "command": ["a"]}, "gamma": {"type": "local", "command": ["v3"]}, "beta": {"type": "local", "command": ["b"]}}}`)

	report := f.sync(t)
	require.NotEmpty(t, report.ConflictsOf(kind.MCP), "gamma is in conflict")

	require.Contains(t, f.servers(t), "beta")
	require.Contains(t, read(t, f.claudeConfig()), "beta", "an unrelated server propagates despite the conflict")
	require.Contains(t, read(t, f.claudeConfig()), `"v2"`)
	require.Contains(t, read(t, f.openCodeConfig()), `"v3"`)

	for range 2 {
		report = f.sync(t)
		require.NotEmpty(t, report.ConflictsOf(kind.MCP), "an unresolved conflict survives repeated syncs")
	}

	require.Contains(t, read(t, f.claudeConfig()), `"v2"`)
	require.Contains(t, read(t, f.openCodeConfig()), `"v3"`)

	f.resolve(t, kind.MCP, agent.OpenCodeID, engine.TakeAgent)

	require.Contains(t, read(t, f.claudeConfig()), `"v3"`)
	require.Contains(t, read(t, f.openCodeConfig()), `"v3"`)

	report = f.sync(t)
	require.Empty(t, report.Conflicts)
	require.False(t, report.VaultChanged())
	require.Equal(t, engine.ActionNoop, report.Action(kind.MCP, agent.ClaudeCodeID))
	require.Equal(t, engine.ActionNoop, report.Action(kind.MCP, agent.OpenCodeID))
}

func TestFirstSyncDisagreementIsAConflict(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	write(t, f.claudeConfig(), `{"mcpServers": {"gamma": {"type": "stdio", "command": "v1"}}}`)
	write(t, f.openCodeConfig(), `{"mcp": {"gamma": {"type": "local", "command": ["v9"]}}}`)

	f.sync(t)

	c := f.conflict(t, kind.MCP, agent.OpenCodeID)
	require.Equal(t, state.ReasonAdded, c.Reason)
	require.Contains(t, read(t, f.openCodeConfig()), "v9", "nothing is overwritten before a human decides")
	require.Contains(t, read(t, f.claudeConfig()), "v1")
}

func TestSkillsSync(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.config.Enable(agent.SharedID)
	f.emptyConfigs(t)

	write(t, f.claudeSkill("alpha"), "alpha\n")
	write(t, f.claudeSkill("shared"), "v1\n")
	write(t, f.openCodeSkill("beta"), "beta\n")
	write(t, f.openCodeSkill("shared"), "v1\n")

	report := f.sync(t)
	require.Empty(t, report.Conflicts)
	require.True(t, report.Kind(kind.Skills).VaultChanged)

	for _, name := range []string{"alpha", "beta", "shared"} {
		require.FileExists(t, f.vaultSkill(name))
		require.FileExists(t, f.sharedSkill(name), "the shared directory receives every skill")
	}

	require.FileExists(t, f.claudeSkill("beta"), "Claude receives the OpenCode-only skill")
	require.NoFileExists(t, f.openCodeSkill("alpha"), "OpenCode reads Claude's directory natively: its own is never written")

	report = f.sync(t)
	require.False(t, report.Kind(kind.Skills).VaultChanged)
	require.Equal(t, engine.ActionNoop, report.Action(kind.Skills, agent.SharedID))
}

func TestSkillsConflictTakeAgent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, f.claudeSkill("shared"), "v1\n")
	write(t, f.openCodeSkill("shared"), "v1\n")
	f.sync(t)

	write(t, f.claudeSkill("shared"), "claude-edit\n")
	write(t, f.openCodeSkill("shared"), "opencode-edit\n")

	report := f.sync(t)
	require.NotEmpty(t, report.ConflictsOf(kind.Skills))
	require.Equal(t, "claude-edit\n", read(t, f.vaultSkill("shared")))

	f.resolve(t, kind.Skills, agent.OpenCodeID, engine.TakeAgent)

	require.Equal(t, "opencode-edit\n", read(t, f.vaultSkill("shared")))
	require.Equal(t, "opencode-edit\n", read(t, f.claudeSkill("shared")))
}

func TestSkillsConflictDoesNotFreezeOtherSkills(t *testing.T) {
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
	require.NotEmpty(t, report.ConflictsOf(kind.Skills))

	require.Equal(t, "beta\n", read(t, f.vaultSkill("beta")))
	require.Equal(t, "beta\n", read(t, f.claudeSkill("beta")))
	require.Equal(t, "beta\n", read(t, f.sharedSkill("beta")))

	require.Equal(t, "claude-edit\n", read(t, f.claudeSkill("shared")))
	require.Equal(t, "opencode-edit\n", read(t, f.openCodeSkill("shared")))

	for range 2 {
		report = f.sync(t)
		require.NotEmpty(t, report.ConflictsOf(kind.Skills), "an unresolved conflict survives repeated syncs")
	}

	f.resolve(t, kind.Skills, agent.OpenCodeID, engine.TakeAgent)

	require.Equal(t, "opencode-edit\n", read(t, f.vaultSkill("shared")))
	require.Equal(t, "opencode-edit\n", read(t, f.claudeSkill("shared")))
	require.Equal(t, "opencode-edit\n", read(t, f.sharedSkill("shared")))

	report = f.sync(t)
	require.Empty(t, report.Conflicts)
	require.False(t, report.Kind(kind.Skills).VaultChanged)
}

func TestDeletedSkillStaysDeleted(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, f.claudeSkill("old"), "old\n")
	f.sync(t)

	require.NoError(t, os.RemoveAll(filepath.Dir(f.claudeSkill("old"))))
	f.sync(t)

	require.NoFileExists(t, f.claudeSkill("old"), "a skill deleted by the user does not come back")
	require.NoFileExists(t, f.vaultSkill("old"))
}

func TestPermissionsSync(t *testing.T) {
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
	require.Empty(t, report.Conflicts)

	rules := f.rules(t)
	require.Equal(t, permission.EffectAllow, rules["tool:read"])
	require.Equal(t, permission.EffectAsk, rules["bash:git commit*"])
	require.Equal(t, permission.EffectAllow, rules["mcp:codegraph:*"])

	claude := read(t, f.claudeSettings())
	require.Contains(t, claude, "Edit(~/topscan/**)", "a path rule stays in Claude only")
	require.Contains(t, claude, "mcp__codegraph__*", "Claude receives the OpenCode MCP rule")
	require.Contains(t, read(t, f.openCodeConfig()), `"*": "allow"`, "the OpenCode default stays in OpenCode only")
}

func TestPermissionsConflictTakeAgent(t *testing.T) {
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
	require.NotEmpty(t, report.ConflictsOf(kind.Permissions))

	f.resolve(t, kind.Permissions, agent.OpenCodeID, engine.TakeAgent)

	require.Equal(t, permission.EffectAllow, f.rules(t)["bash:git commit*"])
	require.Contains(t, read(t, f.openCodeConfig()), `"git commit*": "allow"`)
	require.Regexp(t, `"allow":\s*\["Bash\(git commit:\*\)"\]`, read(t, f.claudeSettings()))
	require.Regexp(t, `"deny":\s*\[\]`, read(t, f.claudeSettings()))
}

func TestPermissionsForeignKindsSurviveLimitedAgents(t *testing.T) {
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

	for i := range 3 {
		report := f.sync(t)
		require.Empty(t, report.Conflicts, "run %d", i)

		rules := f.rules(t)
		require.Equal(t, permission.EffectAllow, rules["tool:webfetch"], "run %d: a tool rule survives agents that cannot express it", i)
		require.Equal(t, permission.EffectAllow, rules["mcp:codegraph:*"], "run %d: an MCP rule survives Gemini", i)
		require.Equal(t, permission.EffectAllow, rules["bash:git status"], "run %d", i)
	}

	require.Contains(t, read(t, f.claudeSettings()), "WebFetch")
	require.Contains(t, read(t, f.claudeSettings()), "mcp__codegraph__*")

	report := f.sync(t)
	require.Equal(t, engine.ActionNoop, report.Action(kind.Permissions, agent.GeminiCLIID), "no perpetual push")
	require.Equal(t, engine.ActionNoop, report.Action(kind.Permissions, agent.CursorID), "no perpetual push")
}

func TestPermissionsPushedTogetherWithMCP(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.config.Permissions = permission.ModeSync

	write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	write(t, f.claudeSettings(), `{"permissions": {"allow": ["Bash(git status)"], "ask": [], "deny": []}}`)
	write(t, f.openCodeConfig(), `{"mcp": {}, "permission": {"bash": {"git status": "allow"}}}`)
	f.sync(t)

	write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "a"}}}`)
	write(t, f.claudeSettings(), `{"permissions": {"allow": ["Bash(git status)"], "ask": [], "deny": ["Bash(rm -rf:*)"]}}`)
	f.sync(t)

	openCode := read(t, f.openCodeConfig())
	require.Contains(t, openCode, "alpha")
	require.Contains(t, openCode, "rm -rf*", "the deny rule reaches OpenCode together with the server")

	f.sync(t)
	require.Equal(t, permission.EffectDeny, f.rules(t)["bash:rm -rf*"], "the deny rule survives the next sync")
}

func TestBashPatternIsNotRewritten(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.config.Permissions = permission.ModeSync

	write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	write(t, f.claudeSettings(), `{"permissions": {"allow": [], "ask": [], "deny": []}}`)
	write(t, f.openCodeConfig(), `{"mcp": {}, "permission": {"bash": {"git commit *": "ask"}}}`)

	for range 3 {
		f.sync(t)
	}

	require.Contains(t, read(t, f.openCodeConfig()), `"git commit *"`, "the user's own pattern is never rewritten")
	require.Equal(t, permission.EffectAsk, f.rules(t)["bash:git commit *"])
	require.Contains(t, read(t, f.claudeSettings()), "Bash(git commit:*)")
}

func TestNewAgentWithEmptyMCPDoesNotWipeVault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "a"}, "beta": {"type": "stdio", "command": "b"}}}`)
	write(t, f.openCodeConfig(), `{"mcp": {}}`)
	f.sync(t)
	require.Len(t, f.servers(t), 2)

	f.config.Enable(agent.GeminiCLIID)
	write(t, f.geminiSettings(), `{"general": {"vimMode": false}}`)
	f.sync(t)

	require.Len(t, f.servers(t), 2, "a newly detected agent only adds, it never deletes")
	require.Contains(t, read(t, f.claudeConfig()), "alpha")
	require.Contains(t, read(t, f.geminiSettings()), "alpha")
	require.Contains(t, read(t, f.geminiSettings()), "vimMode")
}

func TestReenabledStaleAgentDoesNotRollBack(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "v1"}}}`)
	write(t, f.openCodeConfig(), `{"mcp": {}}`)
	f.sync(t)

	f.config.Disable(agent.OpenCodeID)
	write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "v2"}, "gamma": {"type": "stdio", "command": "g"}}}`)
	f.sync(t)

	f.config.Enable(agent.OpenCodeID)
	f.sync(t)

	servers := f.servers(t)
	require.Contains(t, servers, "gamma", "a stale agent does not delete newer servers")
	require.Equal(t, []string{"v2"}, servers["alpha"].Command, "a stale agent does not roll newer values back")
	require.Contains(t, read(t, f.openCodeConfig()), `"v2"`, "the stale agent is updated instead")
}

func TestAgentSpecificFieldsSurvive(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "a", "timeout": 5000}}}`)
	write(t, f.openCodeConfig(), `{"mcp": {"alpha": {"type": "local", "command": ["a"], "enabled": false}}}`)

	for range 3 {
		f.sync(t)
	}

	require.Contains(t, read(t, f.claudeConfig()), "5000", "a Claude-only timeout survives")
	require.Regexp(t, `"enabled":\s*false`, read(t, f.openCodeConfig()), "an OpenCode-only enabled=false survives")

	report := f.sync(t)
	require.Equal(t, engine.ActionNoop, report.Action(kind.MCP, agent.ClaudeCodeID), "a stable state is not pushed again")
	require.Equal(t, engine.ActionNoop, report.Action(kind.MCP, agent.OpenCodeID), "a stable state is not pushed again")
}

func TestSSETransportSurvivesLossyAgents(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.config.Enable(agent.GeminiCLIID)

	write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	write(t, f.openCodeConfig(), `{"mcp": {}}`)
	write(t, f.geminiSettings(), `{"mcpServers": {"legacy": {"url": "https://example.com/sse"}}}`)

	for range 3 {
		f.sync(t)
	}

	require.Equal(t, mcp.TransportSSE, f.servers(t)["legacy"].Transport)
	require.NotContains(t, read(t, f.geminiSettings()), "httpUrl", "an SSE server stays SSE in Gemini")
	require.Contains(t, read(t, f.claudeConfig()), `"sse"`, "Claude can express SSE and receives it")

	write(t, f.openCodeConfig(), `{"mcp": {"legacy": {"type": "remote", "url": "https://example.com/sse2"}}}`)
	f.sync(t)

	legacy := f.servers(t)["legacy"]
	require.Equal(t, "https://example.com/sse2", legacy.URL)
	require.Equal(t, mcp.TransportSSE, legacy.Transport)
}

func TestDeletedRulesFileKeepsOtherAgents(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	write(t, f.claudeRules(), "# important\n")
	write(t, f.openCodeRules(), "# important\n")
	f.sync(t)

	require.NoError(t, os.Remove(f.openCodeRules()))

	for range 2 {
		f.sync(t)
	}

	require.Equal(t, "# important\n", read(t, f.claudeRules()))
	require.Equal(t, "# important\n", read(t, f.vault.RulesPath()))
}

func TestResolveKeepsUnpulledAgentChanges(t *testing.T) {
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

	f.resolve(t, kind.Rules, agent.OpenCodeID, engine.TakeVault)

	require.Contains(t, read(t, f.openCodeConfig()), "fresh", "resolving rules never clobbers an unpulled MCP change")
	require.Contains(t, read(t, f.claudeConfig()), "fresh", "the unpulled change propagates instead")
	require.Equal(t, "# claude\n", read(t, f.openCodeRules()))
}

func TestGeminiSettingsWithComments(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.config.Enable(agent.GeminiCLIID)

	write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "a"}}}`)
	write(t, f.openCodeConfig(), `{"mcp": {}}`)
	write(t, f.geminiSettings(), "{\n  // personal settings\n  \"mcpServers\": {}\n}\n")

	f.sync(t)

	require.Contains(t, read(t, f.geminiSettings()), "personal settings")
	require.Contains(t, read(t, f.geminiSettings()), "alpha")
}

func TestSymlinkedRulesFileIsAnAlias(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	write(t, f.openCodeConfig(), `{"mcp": {}}`)
	write(t, f.claudeRules(), "# v1\n")
	require.NoError(t, os.Symlink(f.claudeRules(), f.openCodeRules()))

	report := f.sync(t)
	require.Equal(t, engine.ActionAlias, report.Action(kind.Rules, agent.OpenCodeID))

	write(t, f.vault.RulesPath(), "# v2\n")
	f.sync(t)

	info, err := os.Lstat(f.openCodeRules())
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink, "a user symlink is never replaced by a copy")
	require.Equal(t, "# v2\n", read(t, f.claudeRules()))
	require.Equal(t, "# v2\n", read(t, f.openCodeRules()))
}

func TestMassDeletionWaitsForConfirmation(t *testing.T) {
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
	f.sync(t)

	require.Len(t, f.servers(t), 5, "a mass deletion is not applied on its own")

	conflicts := f.conflicts(t, kind.MCP, agent.ClaudeCodeID)
	require.Len(t, conflicts, 5)
	require.Equal(t, state.ReasonMassDelete, conflicts[0].Reason)
	require.Contains(t, read(t, f.openCodeConfig()), "s4")

	ids := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		ids = append(ids, c.ID())
	}

	_, err := f.engine.Resolve(t.Context(), ids, engine.Resolution{Take: engine.TakeAgent})
	require.NoError(t, err)

	require.Empty(t, f.servers(t))
	require.NotContains(t, read(t, f.openCodeConfig()), "s4", "a confirmed deletion reaches every agent")
}

func TestModeOffIgnoresAgentKind(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.config.SetMode(agent.OpenCodeID, kind.MCP, config.ModeOff)

	write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "a"}}}`)
	write(t, f.openCodeConfig(), `{"mcp": {"beta": {"type": "local", "command": ["b"]}}}`)

	f.sync(t)

	require.NotContains(t, f.servers(t), "beta")
	require.NotContains(t, read(t, f.openCodeConfig()), "alpha")
}

func TestPushAndPullDirections(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "v1"}}}`)
	write(t, f.openCodeConfig(), `{"mcp": {}}`)
	f.sync(t)

	write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "v2"}}}`)
	f.run(t, engine.SyncOptions{Direction: config.ModePush})

	require.Contains(t, read(t, f.claudeConfig()), `"v1"`)
	require.Equal(t, []string{"v1"}, f.servers(t)["alpha"].Command)

	write(t, f.openCodeConfig(), `{"mcp": {"alpha": {"type": "local", "command": ["v1"]}, "fresh": {"type": "local", "command": ["f"]}}}`)
	f.run(t, engine.SyncOptions{Direction: config.ModePull})

	require.Contains(t, f.servers(t), "fresh")
	require.NotContains(t, read(t, f.claudeConfig()), "fresh")
}

func TestDryRunWritesNothing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	original := `{"mcp": {}}`

	write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "a"}}}`)
	write(t, f.openCodeConfig(), original)

	report := f.run(t, engine.SyncOptions{DryRun: true})
	require.True(t, report.DryRun)
	require.Equal(t, engine.ActionWouldPush, report.Action(kind.MCP, agent.OpenCodeID))
	require.True(t, report.VaultChanged())

	require.Equal(t, original, read(t, f.openCodeConfig()), "a dry run writes nothing")
	require.NoFileExists(t, f.vault.ServersPath())
	require.NoFileExists(t, f.vault.StatePath())
}

func TestLockSerializesProcesses(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	release, err := lock.Acquire(t.Context(), f.vault.LockPath())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	_, err = f.engine.Sync(ctx, engine.SyncOptions{})
	require.ErrorIs(t, err, lock.ErrBusy)

	require.NoError(t, release())
	f.sync(t)
}

func TestDoctor(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	write(t, f.claudeSkill("alpha"), "a\n")
	write(t, f.openCodeSkill("beta"), "b\n")
	f.sync(t)

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)

	for _, issue := range issues {
		require.NotEqual(t, engine.SeverityError, issue.Severity, "unexpected: %s", issue.Message)
		require.NotEqual(t, engine.SeverityWarn, issue.Severity, "unexpected: %s", issue.Message)
	}

	require.NoError(t, os.Chmod(f.vault.ConfigPath(), 0o644)) //nolint:gosec // G302: loosened on purpose to test the warning
	require.NoError(t, os.Symlink(filepath.Join(f.home, "missing"), filepath.Join(f.home, ".claude", "skills", "broken")))
	require.NoError(t, os.MkdirAll(filepath.Join(f.home, ".claude", "skills", "Foo"), 0o750))
	write(t, f.vaultSkill("foo"), "x\n")
	write(t, f.claudeRules(), "# drifted\n")

	issues, err = f.engine.Doctor(t.Context())
	require.NoError(t, err)

	require.True(t, hasIssue(issues, engine.SeverityWarn, "expected 0600"), "missing permission warning: %v", issues)
	require.True(t, hasIssue(issues, engine.SeverityError, "broken symlink"), "missing broken symlink: %v", issues)
	require.True(t, hasIssue(issues, engine.SeverityError, "collision"), "missing collision: %v", issues)
	require.True(t, hasIssue(issues, engine.SeverityWarn, "not in the vault yet"), "missing pending change: %v", issues)
}

func TestDoctorNoFalseDriftForLimitedAgents(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.config.Permissions = permission.ModeSync
	f.config.Enable(agent.GeminiCLIID)
	f.emptyConfigs(t)

	write(t, f.claudeSettings(), `{"permissions": {"allow": ["WebFetch", "Bash(git status)"], "ask": [], "deny": []}}`)
	write(t, f.geminiSettings(), `{"mcpServers": {}, "tools": {"allowed": ["run_shell_command(git status)"]}}`)

	for range 2 {
		f.sync(t)
	}

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)

	for _, issue := range issues {
		require.NotEqual(t, engine.SeverityWarn, issue.Severity, "structural gaps are not drift: %v", issue)
	}
}

func TestDoctorProjectScope(t *testing.T) {
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
	require.NoError(t, err)

	write(t, filepath.Join(repo, ".mcp.json"), `{"mcpServers": {"proj-srv": {"type": "stdio", "command": "repo-cmd"}, "repo-only": {"type": "stdio", "command": "repo"}}}`)
	write(t, f.claudeConfig(), fmt.Sprintf(`{"mcpServers": {"proj-srv": {"type": "stdio", "command": "vault-cmd"}},
		"projects": {%q: {"mcpServers": {"local-srv": {"type": "stdio", "command": "local"}}}}}`, dir))

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)

	require.True(t, hasIssue(issues, engine.SeverityWarn, "proj-srv"), "missing collision warning: %v", issues)
	require.True(t, hasIssue(issues, engine.SeverityInfo, "repo-only"), "missing repo-only info: %v", issues)
	require.True(t, hasIssue(issues, engine.SeverityInfo, "local-srv"), "missing local-srv info: %v", issues)
}

func TestSecretsExtractedFromLiteralAndPushedBack(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	write(t, f.claudeConfig(), `{"mcpServers": {"ctx7": {"type": "http", "url": "https://mcp.example.com",
	  "headers": {"Authorization": "Bearer abc123", "Accept": "application/json"}}}}`)
	write(t, f.openCodeConfig(), `{"mcp": {}}`)

	report := f.sync(t)
	require.Empty(t, report.Conflicts)

	vaultRaw := read(t, f.vault.ServersPath())
	require.NotContains(t, vaultRaw, "abc123", "the vault never holds the literal")
	require.Contains(t, vaultRaw, "{secret:AUTHORIZATION}")

	value, ok := f.engine.Secrets().Get("AUTHORIZATION")
	require.True(t, ok)
	require.Equal(t, "Bearer abc123", value)

	info, err := os.Stat(f.vault.SecretsPath())
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	openCode := read(t, f.openCodeConfig())
	require.Contains(t, openCode, "abc123", "OpenCode receives a working literal")
	require.NotContains(t, openCode, "{secret:")
	require.Contains(t, openCode, "application/json")

	report = f.sync(t)
	require.False(t, report.VaultChanged())
	require.Equal(t, engine.ActionNoop, report.Action(kind.MCP, agent.ClaudeCodeID))
	require.Equal(t, engine.ActionNoop, report.Action(kind.MCP, agent.OpenCodeID))
}

func TestSecretsMissingValueSkipsPushWithoutCorruption(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	write(t, f.vault.ServersPath(), `{"ctx7":{"transport":"http","url":"https://mcp.example.com","headers":{"Authorization":"{secret:AUTHORIZATION}"}}}`)
	write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	write(t, f.openCodeConfig(), `{"mcp": {}}`)

	report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
	require.NoError(t, err)

	result, ok := report.Kind(kind.MCP).Agent(agent.OpenCodeID)
	require.True(t, ok)
	require.Equal(t, engine.ActionSkipped, result.Action)
	require.Contains(t, result.Note, "AUTHORIZATION")
	require.JSONEq(t, `{"mcp": {}}`, read(t, f.openCodeConfig()), "never wiped, never given a broken reference")

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityError, "AUTHORIZATION"), "a missing secret surfaces in doctor: %v", issues)
}

func TestSecretsEnvModeRendersReference(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.config.Secrets = secret.ModeEnv

	write(t, f.claudeConfig(), `{"mcpServers": {"ctx7": {"type": "http", "url": "https://mcp.example.com", "headers": {"Authorization": "Bearer abc123"}}}}`)
	write(t, f.openCodeConfig(), `{"mcp": {}}`)
	f.sync(t)

	openCode := read(t, f.openCodeConfig())
	require.Contains(t, openCode, "{env:AUTHORIZATION}")
	require.NotContains(t, openCode, "abc123")
}

func TestSecretsPrune(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	write(t, f.claudeConfig(), `{"mcpServers": {"ctx7": {"type": "http", "url": "https://mcp.example.com", "headers": {"Authorization": "Bearer abc123"}}}}`)
	write(t, f.openCodeConfig(), `{"mcp": {}}`)
	f.sync(t)

	write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	f.sync(t)

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityInfo, "AUTHORIZATION"), "an orphan secret is reported: %v", issues)

	removed, err := f.engine.PruneSecrets(t.Context())
	require.NoError(t, err)
	require.Equal(t, []string{"AUTHORIZATION"}, removed)
	require.False(t, f.engine.Secrets().Has("AUTHORIZATION"))
}

func TestSecretOfAnOpenConflictIsInUse(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	write(t, f.claudeConfig(), `{"mcpServers": {"ctx7": {"type": "http", "url": "https://mcp.example.com", "headers": {"API_KEY": "key-one"}}}}`)
	write(t, f.openCodeConfig(), `{"mcp": {"ctx7": {"type": "remote", "url": "https://mcp.example.com", "headers": {"API_KEY": "key-two"}}}}`)
	f.sync(t)

	require.Len(t, f.conflicts(t, kind.MCP, agent.OpenCodeID), 1)

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.False(t, hasIssue(issues, engine.SeverityInfo, "is unused"), "the second key belongs to an open conflict: %v", issues)

	removed, err := f.engine.PruneSecrets(t.Context())
	require.NoError(t, err)
	require.Empty(t, removed)

	f.resolve(t, kind.MCP, agent.OpenCodeID, engine.TakeAgent)
	require.Contains(t, read(t, f.claudeConfig()), "key-two", "the agent's key reaches every agent after resolution")
}

func TestEnvRefsCrossAgent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	write(t, f.claudeConfig(), `{"mcpServers": {"s": {"type": "http", "url": "https://example.com", "headers": {"Authorization": "Bearer ${TOKEN}"}}}}`)
	write(t, f.openCodeConfig(), `{"mcp": {}}`)
	f.sync(t)

	require.Contains(t, read(t, f.openCodeConfig()), "{env:TOKEN}", "Claude's ${TOKEN} arrives in OpenCode syntax")
	require.Contains(t, read(t, f.vault.ServersPath()), "{env:TOKEN}")
}

func TestGeminiSync(t *testing.T) {
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

	report := f.sync(t)
	require.Empty(t, report.Conflicts)

	require.Contains(t, read(t, f.geminiSettings()), "alpha")
	require.Contains(t, read(t, f.claudeConfig()), "g-srv")
	require.FileExists(t, f.vaultSkill("g-skill"))
	require.FileExists(t, f.claudeSkill("g-skill"))
}

func TestRestore(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	write(t, f.claudeRules(), "# base\n")
	write(t, f.openCodeRules(), "# base\n")
	write(t, f.claudeSkill("alpha"), "a\n")
	f.sync(t)

	write(t, f.vault.RulesPath(), "# edited\n")
	f.sync(t)
	require.Equal(t, "# edited\n", read(t, f.claudeRules()))

	_, err := f.engine.Restore(t.Context(), kind.Rules, -2)
	require.NoError(t, err)
	require.Equal(t, "# base\n", read(t, f.vault.RulesPath()))
	require.Equal(t, "# base\n", read(t, f.claudeRules()))
	require.Equal(t, "# base\n", read(t, f.openCodeRules()))

	write(t, f.vaultSkill("extra"), "x\n")
	f.sync(t)
	require.FileExists(t, f.claudeSkill("extra"))

	_, err = f.engine.Restore(t.Context(), kind.Skills, -2)
	require.NoError(t, err)
	require.NoFileExists(t, f.vaultSkill("extra"))
	require.NoFileExists(t, f.claudeSkill("extra"))
	require.FileExists(t, f.vaultSkill("alpha"))
}

func TestGitHistory(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	t.Setenv("GIT_AUTHOR_NAME", "agent-sync test")
	t.Setenv("GIT_AUTHOR_EMAIL", "agent-sync@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "agent-sync test")
	t.Setenv("GIT_COMMITTER_EMAIL", "agent-sync@example.invalid")

	f := newFixture(t)
	f.config.History = config.HistoryGit
	f.emptyConfigs(t)
	write(t, f.claudeRules(), "# v1\n")
	write(t, f.openCodeRules(), "# v1\n")

	f.sync(t)

	first := gitLog(t, f.vault.Root())
	require.Contains(t, first, "agentsync: sync")

	f.sync(t)
	require.Equal(t, first, gitLog(t, f.vault.Root()), "a run without changes creates no commit")

	write(t, f.claudeRules(), "# v2\n")
	f.sync(t)
	require.NotEqual(t, first, gitLog(t, f.vault.Root()))
}

func gitLog(t *testing.T, dir string) string {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "git", "-C", dir, "log", "--oneline").Output() //nolint:gosec // G204: fixed git subcommand in a test
	require.NoError(t, err)

	return string(out)
}
