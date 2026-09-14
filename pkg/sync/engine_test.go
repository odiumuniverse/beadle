package sync_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vmkteam/embedlog"

	"github.com/odiumuniverse/agents-sync/pkg/adapter"
	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/mcp"
	"github.com/odiumuniverse/agents-sync/pkg/permission"
	"github.com/odiumuniverse/agents-sync/pkg/secret"
	syncer "github.com/odiumuniverse/agents-sync/pkg/sync"
	"github.com/odiumuniverse/agents-sync/pkg/vault"
)

type fixture struct {
	home      string
	vaultRoot string
	engine    *syncer.Engine
	config    *config.Config
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	home := t.TempDir()
	vaultRoot := filepath.Join(t.TempDir(), "vault")

	v := vault.New(vaultRoot)

	require.NoError(t, v.Init())

	cfg, err := config.Load(v.ConfigPath())
	require.NoError(t, err)

	cfg.Enable("claude-code")
	cfg.Enable("opencode")
	require.NoError(t, cfg.Save(v.ConfigPath()))

	engine, err := syncer.New(v, cfg, adapter.All(home), embedlog.Logger{}, syncer.WithSharedSkillsDir(filepath.Join(home, ".agents", "skills")))
	require.NoError(t, err)

	return &fixture{home: home, vaultRoot: vaultRoot, engine: engine, config: cfg}
}

func TestSyncUnionAndIdempotency(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	f.write(t, f.claudeConfig(), `{
  "numStartups": 7,
  "mcpServers": {"alpha": {"type": "stdio", "command": "cmd", "args": ["a"]}}
}`)
	f.write(t, f.claudeRules(), "# shared\n")
	f.write(t, f.openCodeConfig(), `{
  // keep
  "$schema": "https://opencode.ai/config.json",
  "mcp": {"beta": {"type": "local", "command": ["b"]}}
}`)
	f.write(t, f.openCodeRules(), "# shared\n")

	report, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)
	require.Empty(t, report.Rules.Conflicts)
	require.Empty(t, report.MCP.Conflicts)

	require.Equal(t, "# shared\n", string(read(t, f.vaultRules())))

	servers := f.vaultServers(t)
	require.Len(t, servers, 2)
	require.Contains(t, servers, "alpha")
	require.Contains(t, servers, "beta")

	claude := string(read(t, f.claudeConfig()))
	require.Contains(t, claude, "beta")
	require.Contains(t, claude, "numStartups")

	openCode := string(read(t, f.openCodeConfig()))
	require.Contains(t, openCode, "alpha")
	require.Contains(t, openCode, "keep")

	report, err = f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)
	require.Equal(t, syncer.ActionNoop, report.Actions["claude-code"].Rules)
	require.Equal(t, syncer.ActionNoop, report.Actions["claude-code"].MCP)
	require.Equal(t, syncer.ActionNoop, report.Actions["opencode"].MCP)
}

func TestRulesConflictAndResolve(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	f.write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {}}`)
	f.write(t, f.claudeRules(), "# shared\n")
	f.write(t, f.openCodeRules(), "# shared\n")

	_, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)

	f.write(t, f.claudeRules(), "# claude\n")
	f.write(t, f.openCodeRules(), "# opencode\n")

	report, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)
	require.NotEmpty(t, report.Rules.Conflicts)

	require.Contains(t, string(read(t, f.vaultRules())), "<<<<<<<")
	require.Equal(t, "# claude\n", string(read(t, f.claudeRules())))
	require.Equal(t, "# opencode\n", string(read(t, f.openCodeRules())))

	f.write(t, f.vaultRules(), "# resolved\n")

	require.NoError(t, f.engine.Resolve(t.Context(), syncer.ResourceRules, syncer.ResolveOptions{}))

	require.Equal(t, "# resolved\n", string(read(t, f.claudeRules())))
	require.Equal(t, "# resolved\n", string(read(t, f.openCodeRules())))
}

func TestMCPConflictKeepAgent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	f.write(t, f.claudeConfig(), `{"mcpServers": {"gamma": {"type": "stdio", "command": "v1"}}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {}}`)
	f.write(t, f.claudeRules(), "# r\n")
	f.write(t, f.openCodeRules(), "# r\n")

	_, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)

	f.write(t, f.claudeConfig(), `{"mcpServers": {"gamma": {"type": "stdio", "command": "v2"}}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {"gamma": {"type": "local", "command": ["v3"]}}}`)

	report, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)
	require.NotEmpty(t, report.MCP.Conflicts)

	require.Equal(t, []string{"v2"}, f.vaultServers(t)["gamma"].Command)

	require.NoError(t, f.engine.Resolve(t.Context(), syncer.ResourceMCP, syncer.ResolveOptions{KeepAgent: true, Agent: "opencode"}))

	require.Equal(t, []string{"v3"}, f.vaultServers(t)["gamma"].Command)
	require.Contains(t, string(read(t, f.claudeConfig())), "v3")
	require.Contains(t, string(read(t, f.openCodeConfig())), "v3")
}

func TestMCPConflictDoesNotFreezeOtherServers(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	f.write(t, f.claudeConfig(), `{"mcpServers": {"gamma": {"type": "stdio", "command": "v1"}, "alpha": {"type": "stdio", "command": "a"}}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {}}`)

	_, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)

	f.write(t, f.claudeConfig(), `{"mcpServers": {"gamma": {"type": "stdio", "command": "v2"}, "alpha": {"type": "stdio", "command": "a"}}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {"gamma": {"type": "local", "command": ["v3"]}, "beta": {"type": "local", "command": ["b"]}}}`)

	report, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)
	require.NotEmpty(t, report.MCP.Conflicts, "gamma must still be reported as conflicted")

	require.Contains(t, f.vaultServers(t), "beta")
	require.Contains(t, string(read(t, f.claudeConfig())), "beta")

	require.Contains(t, string(read(t, f.claudeConfig())), `"command":"v2"`)
	require.Contains(t, string(read(t, f.openCodeConfig())), `"v3"`)

	for range 2 {
		report, err = f.engine.Run(t.Context(), syncer.ModeSync)
		require.NoError(t, err)
		require.NotEmpty(t, report.MCP.Conflicts, "an unresolved conflict must survive repeated syncs")
	}

	require.Contains(t, string(read(t, f.claudeConfig())), `"command":"v2"`)
	require.Contains(t, string(read(t, f.openCodeConfig())), `"v3"`)

	require.NoError(t, f.engine.Resolve(t.Context(), syncer.ResourceMCP, syncer.ResolveOptions{KeepAgent: true, Agent: "opencode"}))

	require.Contains(t, string(read(t, f.claudeConfig())), `"v3"`)
	require.Contains(t, string(read(t, f.openCodeConfig())), `"v3"`)

	report, err = f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)
	require.Empty(t, report.MCP.Conflicts)
	require.False(t, report.MCP.Changed)
	require.Equal(t, syncer.ActionNoop, report.Actions["claude-code"].MCP)
	require.Equal(t, syncer.ActionNoop, report.Actions["opencode"].MCP)
}

func TestSkillsSync(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	f.write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {}}`)
	f.write(t, f.claudeRules(), "# r\n")
	f.write(t, f.openCodeRules(), "# r\n")

	f.write(t, filepath.Join(f.claudeSkillsDir(), "alpha", "SKILL.md"), "alpha\n")
	f.write(t, filepath.Join(f.claudeSkillsDir(), "shared", "SKILL.md"), "v1\n")
	f.write(t, filepath.Join(f.openCodeSkillsDir(), "beta", "SKILL.md"), "beta\n")
	f.write(t, filepath.Join(f.openCodeSkillsDir(), "shared", "SKILL.md"), "v1\n")

	report, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)
	require.Empty(t, report.Skills.Conflicts)
	require.True(t, report.Skills.Changed)

	for _, name := range []string{"alpha", "beta", "shared"} {
		require.FileExists(t, filepath.Join(f.vaultRoot, "skills", name, "SKILL.md"))
	}

	require.FileExists(t, filepath.Join(f.claudeSkillsDir(), "beta", "SKILL.md"))

	require.NoFileExists(t, filepath.Join(f.openCodeSkillsDir(), "alpha", "SKILL.md"))
	require.FileExists(t, filepath.Join(f.openCodeSkillsDir(), "beta", "SKILL.md"))

	for _, name := range []string{"alpha", "beta", "shared"} {
		require.FileExists(t, filepath.Join(f.sharedSkillsDir(), name, "SKILL.md"))
	}

	report, err = f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)
	require.False(t, report.Skills.Changed)
	require.Equal(t, syncer.ActionNoop, report.Actions["shared"].Skills)
}

func TestSkillsConflictKeepAgent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	f.write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {}}`)
	f.write(t, f.claudeRules(), "# r\n")
	f.write(t, f.openCodeRules(), "# r\n")
	f.write(t, filepath.Join(f.claudeSkillsDir(), "shared", "SKILL.md"), "v1\n")
	f.write(t, filepath.Join(f.openCodeSkillsDir(), "shared", "SKILL.md"), "v1\n")

	_, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)

	f.write(t, filepath.Join(f.claudeSkillsDir(), "shared", "SKILL.md"), "claude-edit\n")
	f.write(t, filepath.Join(f.openCodeSkillsDir(), "shared", "SKILL.md"), "opencode-edit\n")

	report, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)
	require.NotEmpty(t, report.Skills.Conflicts)

	vaultSkill := filepath.Join(f.vaultRoot, "skills", "shared", "SKILL.md")
	require.Equal(t, "claude-edit\n", string(read(t, vaultSkill)))

	require.FileExists(t, filepath.Join(f.vaultRoot, "conflicts", "skills-opencode", "shared", "SKILL.md"))

	require.NoError(t, f.engine.Resolve(t.Context(), syncer.ResourceSkills, syncer.ResolveOptions{KeepAgent: true, Agent: "opencode"}))

	require.Equal(t, "opencode-edit\n", string(read(t, vaultSkill)))
	require.Equal(t, "opencode-edit\n", string(read(t, filepath.Join(f.claudeSkillsDir(), "shared", "SKILL.md"))))
}

func TestSkillsConflictDoesNotFreezeOtherSkills(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	f.write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {}}`)
	f.write(t, f.claudeRules(), "# r\n")
	f.write(t, f.openCodeRules(), "# r\n")
	f.write(t, filepath.Join(f.claudeSkillsDir(), "shared", "SKILL.md"), "v1\n")
	f.write(t, filepath.Join(f.openCodeSkillsDir(), "shared", "SKILL.md"), "v1\n")

	_, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)

	f.write(t, filepath.Join(f.claudeSkillsDir(), "shared", "SKILL.md"), "claude-edit\n")
	f.write(t, filepath.Join(f.openCodeSkillsDir(), "shared", "SKILL.md"), "opencode-edit\n")
	f.write(t, filepath.Join(f.openCodeSkillsDir(), "beta", "SKILL.md"), "beta\n")

	report, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)
	require.NotEmpty(t, report.Skills.Conflicts, "shared must still be reported as conflicted")

	require.Equal(t, "beta\n", string(read(t, filepath.Join(f.vaultRoot, "skills", "beta", "SKILL.md"))))
	require.Equal(t, "beta\n", string(read(t, filepath.Join(f.claudeSkillsDir(), "beta", "SKILL.md"))))
	require.Equal(t, "beta\n", string(read(t, filepath.Join(f.sharedSkillsDir(), "beta", "SKILL.md"))))

	require.Equal(t, "claude-edit\n", string(read(t, filepath.Join(f.vaultRoot, "skills", "shared", "SKILL.md"))))
	require.Equal(t, "claude-edit\n", string(read(t, filepath.Join(f.claudeSkillsDir(), "shared", "SKILL.md"))))
	require.Equal(t, "opencode-edit\n", string(read(t, filepath.Join(f.openCodeSkillsDir(), "shared", "SKILL.md"))))

	for range 2 {
		report, err = f.engine.Run(t.Context(), syncer.ModeSync)
		require.NoError(t, err)
		require.NotEmpty(t, report.Skills.Conflicts, "an unresolved conflict must survive repeated syncs")
	}

	require.Equal(t, "claude-edit\n", string(read(t, filepath.Join(f.claudeSkillsDir(), "shared", "SKILL.md"))))
	require.Equal(t, "opencode-edit\n", string(read(t, filepath.Join(f.openCodeSkillsDir(), "shared", "SKILL.md"))))
	require.Equal(t, "beta\n", string(read(t, filepath.Join(f.claudeSkillsDir(), "beta", "SKILL.md"))))

	require.NoError(t, f.engine.Resolve(t.Context(), syncer.ResourceSkills, syncer.ResolveOptions{KeepAgent: true, Agent: "opencode"}))

	require.Equal(t, "opencode-edit\n", string(read(t, filepath.Join(f.vaultRoot, "skills", "shared", "SKILL.md"))))
	require.Equal(t, "opencode-edit\n", string(read(t, filepath.Join(f.claudeSkillsDir(), "shared", "SKILL.md"))))
	require.Equal(t, "opencode-edit\n", string(read(t, filepath.Join(f.sharedSkillsDir(), "shared", "SKILL.md"))))

	report, err = f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)
	require.Empty(t, report.Skills.Conflicts)
	require.False(t, report.Skills.Changed)
}

func TestPermissionsSync(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.config.Permissions = permission.ModeSync

	f.write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {}}`)
	f.write(t, f.claudeRules(), "# r\n")
	f.write(t, f.openCodeRules(), "# r\n")

	f.write(t, filepath.Join(f.home, ".claude", "settings.json"), `{
  "permissions": {
    "defaultMode": "auto",
    "allow": ["Read", "Edit(~/topscan/**)"],
    "ask": ["Bash(git commit:*)"],
    "deny": []
  }
}`)
	f.write(t, f.openCodeConfig(), `{
  "mcp": {},
  "permission": {
    "read": "allow",
    "bash": {"*": "allow", "git commit*": "ask"},
    "codegraph_*": "allow"
  }
}`)

	report, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)
	require.Empty(t, report.Permissions.Conflicts)

	rules, err := permission.Parse(read(t, filepath.Join(f.vaultRoot, "permissions", "rules.json")))
	require.NoError(t, err)
	require.Equal(t, "allow", rules["tool:read"])
	require.Equal(t, "ask", rules["bash:git commit*"])
	require.Equal(t, "allow", rules["mcp:codegraph:*"])

	require.Contains(t, string(read(t, filepath.Join(f.home, ".claude", "settings.json"))), "Edit(~/topscan/**)")
	require.Contains(t, string(read(t, f.openCodeConfig())), `"*": "allow"`)

	require.Contains(t, string(read(t, filepath.Join(f.home, ".claude", "settings.json"))), "mcp__codegraph__*")
}

func TestPermissionsConflictKeepAgent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.config.Permissions = permission.ModeSync

	f.write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	f.write(t, f.claudeRules(), "# r\n")
	f.write(t, f.openCodeRules(), "# r\n")
	f.write(t, filepath.Join(f.home, ".claude", "settings.json"), `{"permissions": {"allow": [], "ask": ["Bash(git commit:*)"], "deny": []}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {}, "permission": {"bash": {"git commit*": "ask"}}}`)

	_, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)

	f.write(t, filepath.Join(f.home, ".claude", "settings.json"), `{"permissions": {"allow": [], "ask": [], "deny": ["Bash(git commit:*)"]}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {}, "permission": {"bash": {"git commit*": "allow"}}}`)

	report, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)
	require.NotEmpty(t, report.Permissions.Conflicts)

	require.NoError(t, f.engine.Resolve(t.Context(), syncer.ResourcePermissions, syncer.ResolveOptions{KeepAgent: true, Agent: "opencode"}))

	rules, err := permission.Parse(read(t, filepath.Join(f.vaultRoot, "permissions", "rules.json")))
	require.NoError(t, err)
	require.Equal(t, "allow", rules["bash:git commit*"])
	require.Contains(t, string(read(t, f.openCodeConfig())), `"git commit*": "allow"`)
	require.Contains(t, string(read(t, filepath.Join(f.home, ".claude", "settings.json"))), "Bash(git commit:*)")
}

func TestDoctor(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	f.write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {}}`)
	f.write(t, f.claudeRules(), "# r\n")
	f.write(t, f.openCodeRules(), "# r\n")
	f.write(t, filepath.Join(f.claudeSkillsDir(), "alpha", "SKILL.md"), "a\n")
	f.write(t, filepath.Join(f.openCodeSkillsDir(), "beta", "SKILL.md"), "b\n")

	_, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)

	for _, issue := range issues {
		require.NotEqual(t, syncer.SeverityError, issue.Severity, "unexpected: %s", issue.Message)
	}

	require.NoError(t, os.Chmod(filepath.Join(f.vaultRoot, "config.json"), 0o644)) //nolint:gosec // G302: intentionally loosened to test the doctor warning

	require.NoError(t, os.Symlink(filepath.Join(f.home, "missing-target"), filepath.Join(f.claudeSkillsDir(), "broken")))
	require.NoError(t, os.MkdirAll(filepath.Join(f.claudeSkillsDir(), "Foo"), 0o750))

	f.write(t, filepath.Join(f.vaultRoot, "skills", "foo", "SKILL.md"), "x\n")

	f.write(t, f.claudeRules(), "# drifted\n")

	issues, err = f.engine.Doctor(t.Context())
	require.NoError(t, err)

	require.True(t, hasIssue(issues, syncer.SeverityWarn, "expected 0600"), "missing permission warning: %v", issues)
	require.True(t, hasIssue(issues, syncer.SeverityError, "broken symlink"), "missing broken symlink: %v", issues)
	require.True(t, hasIssue(issues, syncer.SeverityError, "collision"), "missing collision: %v", issues)
	require.True(t, hasIssue(issues, syncer.SeverityWarn, "rules differ"), "missing drift: %v", issues)
}

func TestDoctorProjectScope(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	f.write(t, f.claudeConfig(), `{"mcpServers": {"proj-srv": {"type": "stdio", "command": "vault-cmd"}}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {}}`)
	f.write(t, f.claudeRules(), "# r\n")
	f.write(t, f.openCodeRules(), "# r\n")

	_, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)
	require.Contains(t, f.vaultServers(t), "proj-srv")

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)

	for _, issue := range issues {
		require.NotContains(t, issue.Message, ".mcp.json")
		require.NotContains(t, issue.Message, "projects[")
	}

	repo := t.TempDir()
	t.Chdir(repo)

	dir, err := os.Getwd()
	require.NoError(t, err)

	f.write(t, filepath.Join(repo, ".mcp.json"), `{"mcpServers": {
		"proj-srv": {"type": "stdio", "command": "repo-cmd"},
		"repo-only": {"type": "stdio", "command": "repo"}
	}}`)
	f.write(t, f.claudeConfig(), fmt.Sprintf(`{"mcpServers": {"proj-srv": {"type": "stdio", "command": "vault-cmd"}},
		"projects": {%q: {"mcpServers": {"local-srv": {"type": "stdio", "command": "local"}}}}}`, dir))

	issues, err = f.engine.Doctor(t.Context())
	require.NoError(t, err)

	require.True(t, hasIssue(issues, syncer.SeverityWarn, "proj-srv"), "missing collision warning: %v", issues)
	require.True(t, hasIssue(issues, syncer.SeverityInfo, "repo-only"), "missing repo-only info: %v", issues)
	require.True(t, hasIssue(issues, syncer.SeverityInfo, "local-srv"), "missing local-srv info: %v", issues)
}

func TestSecretsExtractedFromLiteralAndPushedBack(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	f.write(t, f.claudeConfig(), `{"mcpServers": {"ctx7": {"type": "http", "url": "https://mcp.example.com",
	  "headers": {"Authorization": "Bearer abc123", "Accept": "application/json"}}}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {}}`)

	report, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)
	require.Empty(t, report.MCP.Conflicts)
	require.Empty(t, report.MissingSecretNames())

	vaultRaw := string(read(t, filepath.Join(f.vaultRoot, "mcp", "servers.json")))
	require.NotContains(t, vaultRaw, "abc123")
	require.Contains(t, vaultRaw, "{secret:AUTHORIZATION}")

	value, ok := f.engine.Secrets().Get("AUTHORIZATION")
	require.True(t, ok)
	require.Equal(t, "Bearer abc123", value)

	info, err := os.Stat(filepath.Join(f.vaultRoot, "mcp", "secrets.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	require.Contains(t, string(read(t, filepath.Join(f.vaultRoot, ".gitignore"))), "mcp/secrets.json")

	openCode := string(read(t, f.openCodeConfig()))
	require.Contains(t, openCode, "abc123")
	require.NotContains(t, openCode, "{secret:")

	require.Contains(t, openCode, "application/json")

	report, err = f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)
	require.False(t, report.MCP.Changed)
	require.Equal(t, syncer.ActionNoop, report.Actions["claude-code"].MCP)
	require.Equal(t, syncer.ActionNoop, report.Actions["opencode"].MCP)
}

func TestSecretsMissingValueSkipsPushWithoutCorruption(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	f.write(t, filepath.Join(f.vaultRoot, "mcp", "servers.json"),
		`{"ctx7":{"transport":"http","url":"https://mcp.example.com","headers":{"Authorization":"{secret:AUTHORIZATION}"}}}`)
	f.write(t, f.claudeConfig(), `{"mcpServers": {"ctx7": {"type": "http", "url": "https://mcp.example.com",
	  "headers": {"Authorization": "{secret:AUTHORIZATION}"}}}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {}}`)

	require.False(t, f.engine.Secrets().Has("AUTHORIZATION"))

	report, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)

	require.Equal(t, []string{"AUTHORIZATION"}, report.MissingSecretNames())
	require.Equal(t, syncer.ActionSkipped, report.Actions["opencode"].MCP)

	require.JSONEq(t, `{"mcp": {}}`, string(read(t, f.openCodeConfig())))

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, syncer.SeverityError, "AUTHORIZATION"), "missing secret should surface in doctor: %v", issues)
}

func TestSecretsEnvModeRendersReferenceNotLiteral(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.config.Secrets = secret.ModeEnv

	f.write(t, f.claudeConfig(), `{"mcpServers": {"ctx7": {"type": "http", "url": "https://mcp.example.com",
	  "headers": {"Authorization": "Bearer abc123"}}}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {}}`)

	_, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)

	openCode := string(read(t, f.openCodeConfig()))
	require.Contains(t, openCode, "{env:AUTHORIZATION}")
	require.NotContains(t, openCode, "abc123")

	value, ok := f.engine.Secrets().Get("AUTHORIZATION")
	require.True(t, ok)
	require.Equal(t, "Bearer abc123", value)
}

func TestSecretsPrune(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	f.write(t, f.claudeConfig(), `{"mcpServers": {"ctx7": {"type": "http", "url": "https://mcp.example.com",
	  "headers": {"Authorization": "Bearer abc123"}}}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {}}`)

	_, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)
	require.True(t, f.engine.Secrets().Has("AUTHORIZATION"))

	f.write(t, f.claudeConfig(), `{"mcpServers": {}}`)

	_, err = f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, syncer.SeverityInfo, "AUTHORIZATION"), "orphan secret should be reported: %v", issues)

	removed, err := f.engine.PruneSecrets()
	require.NoError(t, err)
	require.Equal(t, []string{"AUTHORIZATION"}, removed)
	require.False(t, f.engine.Secrets().Has("AUTHORIZATION"))
}

func TestRestore(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	f.write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {}}`)
	f.write(t, f.claudeRules(), "# base\n")
	f.write(t, f.openCodeRules(), "# base\n")
	f.write(t, filepath.Join(f.claudeSkillsDir(), "alpha", "SKILL.md"), "a\n")
	f.write(t, filepath.Join(f.openCodeSkillsDir(), "alpha", "SKILL.md"), "a\n")

	_, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)

	f.write(t, f.vaultRules(), "# edited\n")

	require.NoError(t, f.engine.Restore(t.Context(), syncer.ResourceRules, syncer.RestoreOptions{}))
	require.Equal(t, "# base\n", string(read(t, f.vaultRules())))

	f.write(t, filepath.Join(f.vaultRoot, "skills", "extra", "SKILL.md"), "x\n")

	require.NoError(t, f.engine.Restore(t.Context(), syncer.ResourceSkills, syncer.RestoreOptions{}))
	require.NoDirExists(t, filepath.Join(f.vaultRoot, "skills", "extra"))
	require.FileExists(t, filepath.Join(f.vaultRoot, "skills", "alpha", "SKILL.md"))
}

func TestPrune(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	f.write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {}}`)
	f.write(t, f.claudeRules(), "# r\n")
	f.write(t, f.openCodeRules(), "# r\n")
	f.write(t, filepath.Join(f.claudeSkillsDir(), "alpha", "SKILL.md"), "a\n")

	_, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)

	f.write(t, filepath.Join(f.claudeSkillsDir(), "stale", "SKILL.md"), "s\n")

	_, err = f.engineWith(t).Run(t.Context(), syncer.ModePush)
	require.NoError(t, err)
	require.DirExists(t, filepath.Join(f.claudeSkillsDir(), "stale"))

	_, err = f.engineWith(t, syncer.WithPrune()).Run(t.Context(), syncer.ModePush)
	require.NoError(t, err)
	require.NoDirExists(t, filepath.Join(f.claudeSkillsDir(), "stale"))
	require.DirExists(t, filepath.Join(f.claudeSkillsDir(), "alpha"))
}

func (f *fixture) engineWith(t *testing.T, opts ...syncer.Option) *syncer.Engine {
	t.Helper()

	engine, err := syncer.New(vault.New(f.vaultRoot), f.config, adapter.All(f.home), embedlog.Logger{}, opts...)
	require.NoError(t, err)

	return engine
}

func hasIssue(issues []syncer.Issue, severity, substr string) bool {
	for _, issue := range issues {
		if issue.Severity == severity && strings.Contains(issue.Message, substr) {
			return true
		}
	}

	return false
}

func TestGeminiSync(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.config.Enable("gemini-cli")

	f.write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "a"}}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {}}`)
	f.write(t, f.claudeRules(), "# shared\n")
	f.write(t, f.openCodeRules(), "# shared\n")
	f.write(t, filepath.Join(f.home, ".gemini", "settings.json"), `{"mcpServers": {"g-srv": {"command": "g"}}}`)
	f.write(t, filepath.Join(f.home, ".gemini", "GEMINI.md"), "# shared\n")
	f.write(t, filepath.Join(f.home, ".gemini", "skills", "g-skill", "SKILL.md"), "g\n")

	report, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)
	require.Empty(t, report.MCP.Conflicts)
	require.Empty(t, report.Skills.Conflicts)

	require.Contains(t, string(read(t, filepath.Join(f.home, ".gemini", "settings.json"))), "alpha")
	require.Contains(t, string(read(t, f.claudeConfig())), "g-srv")

	require.FileExists(t, filepath.Join(f.vaultRoot, "skills", "g-skill", "SKILL.md"))
	require.FileExists(t, filepath.Join(f.claudeSkillsDir(), "g-skill", "SKILL.md"))
}

func TestEnvRefsCrossAgent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	f.write(t, f.claudeConfig(), `{"mcpServers": {"s": {"type": "http", "url": "https://example.com", "headers": {"Authorization": "Bearer ${TOKEN}"}}}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {}}`)
	f.write(t, f.claudeRules(), "# r\n")
	f.write(t, f.openCodeRules(), "# r\n")

	_, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)

	require.Contains(t, string(read(t, f.openCodeConfig())), "{env:TOKEN}")

	require.Contains(t, string(read(t, filepath.Join(f.vaultRoot, "mcp", "servers.json"))), "{env:TOKEN}")
}

func TestGitHistory(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	f := newFixture(t)
	f.config.History = config.HistoryGit

	f.write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {}}`)
	f.write(t, f.claudeRules(), "# v1\n")
	f.write(t, f.openCodeRules(), "# v1\n")

	_, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)

	first := gitLog(t, f.vaultRoot)
	require.Contains(t, first, "agentsync: sync")

	_, err = f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)
	require.Equal(t, first, gitLog(t, f.vaultRoot))

	f.write(t, f.claudeRules(), "# v2\n")

	_, err = f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)
	require.NotEqual(t, first, gitLog(t, f.vaultRoot))
}

func gitLog(t *testing.T, dir string) string {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "git", "-C", dir, "log", "--oneline").Output() //nolint:gosec // G204: fixed git subcommand in a test
	require.NoError(t, err)

	return string(out)
}

func TestDiff(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	f.write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "v1"}}}`)
	f.write(t, f.openCodeConfig(), `{"mcp": {}}`)
	f.write(t, f.claudeRules(), "# shared\n")
	f.write(t, f.openCodeRules(), "# shared\n")

	_, err := f.engine.Run(t.Context(), syncer.ModeSync)
	require.NoError(t, err)

	f.write(t, f.claudeRules(), "# changed\n")

	report, err := f.engine.Diff(t.Context())
	require.NoError(t, err)
	require.Contains(t, report.Rules["claude-code"], "-# changed")
	require.Empty(t, report.Rules["opencode"])
	require.Empty(t, report.MCP["claude-code"])
}

func (f *fixture) write(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func (f *fixture) claudeConfig() string {
	return filepath.Join(f.home, ".claude.json")
}

func (f *fixture) claudeRules() string {
	return filepath.Join(f.home, ".claude", "CLAUDE.md")
}

func (f *fixture) claudeSkillsDir() string {
	return filepath.Join(f.home, ".claude", "skills")
}

func (f *fixture) openCodeSkillsDir() string {
	return filepath.Join(f.home, ".config", "opencode", "skills")
}

func (f *fixture) sharedSkillsDir() string {
	return filepath.Join(f.home, ".agents", "skills")
}

func (f *fixture) openCodeDir() string {
	return filepath.Join(f.home, ".config", "opencode")
}

func (f *fixture) openCodeConfig() string {
	return filepath.Join(f.openCodeDir(), "opencode.jsonc")
}

func (f *fixture) openCodeRules() string {
	return filepath.Join(f.openCodeDir(), "AGENTS.md")
}

func (f *fixture) vaultRules() string {
	return filepath.Join(f.vaultRoot, "rules", "base.md")
}

func (f *fixture) vaultServers(t *testing.T) mcp.Servers {
	t.Helper()

	servers, err := mcp.ParseCanonical(read(t, filepath.Join(f.vaultRoot, "mcp", "servers.json")))
	require.NoError(t, err)

	return servers
}

func read(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: tests read their own temp files
	require.NoError(t, err)

	return data
}
