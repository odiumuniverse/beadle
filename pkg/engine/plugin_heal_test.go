package engine_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/engine"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
)

func quarantineCurrent(f *fixture, marketplace, name string) string {
	return filepath.Join(f.vault.PluginsDir(), "quarantine", marketplace, name, "current")
}

func quarantinePlugin(t *testing.T, f *fixture) {
	t.Helper()

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")

	require.Len(t, f.sync(t).Farm, 2)

	removeFromRegistry(t, f.home, "acme", "tool")
	require.NoError(t, os.RemoveAll(plugin))

	f.sync(t)

	require.True(t, isStub(t, filepath.Join(claudeSkillsDir(f.home), "alpha")))
}

func TestHealRemovesQuarantine(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	quarantinePlugin(t, f)

	results, err := f.engine.Heal(t.Context(), false)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "acme/tool", results[0].Key)
	require.Equal(t, 2, results[0].Stubs)
	require.Empty(t, results[0].Note)

	require.NoDirExists(t, filepath.Join(claudeSkillsDir(f.home), "alpha"))
	require.NoDirExists(t, filepath.Join(openCodeSkillsDir(f.home), "alpha"))
	require.NoDirExists(t, filepath.Join(f.vault.PluginsDir(), "quarantine"))

	rec := ledgerRecord(t, f, "acme/tool")
	require.True(t, rec.QuarantinedAt.IsZero())
	require.False(t, rec.RetiredAt.IsZero())
	require.Empty(t, rec.Target)

	results, err = f.engine.Heal(t.Context(), false)
	require.NoError(t, err)
	require.Empty(t, results)

	before := read(t, f.vault.PluginsLedgerPath())

	report := f.sync(t)
	require.Empty(t, report.Farm)
	require.Empty(t, report.Plugins, "a retired record is silent")
	require.Equal(t, before, read(t, f.vault.PluginsLedgerPath()), "a retired record stays retired")
	require.NoDirExists(t, filepath.Join(f.vault.PluginsDir(), "quarantine"))
}

func TestHealDryRun(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	quarantinePlugin(t, f)

	before := read(t, f.vault.PluginsLedgerPath())

	results, err := f.engine.Heal(t.Context(), true)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, 2, results[0].Stubs)

	require.True(t, isStub(t, filepath.Join(claudeSkillsDir(f.home), "alpha")))
	require.True(t, isStub(t, filepath.Join(openCodeSkillsDir(f.home), "alpha")))

	link, err := os.Readlink(quarantineCurrent(f, "acme", "tool"))
	require.NoError(t, err)
	require.NotEmpty(t, link)
	require.Equal(t, before, read(t, f.vault.PluginsLedgerPath()), "a dry run writes nothing")
}

func TestHealKeepsForeign(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	quarantinePlugin(t, f)

	foreignNote := filepath.Join(f.vault.PluginsDir(), "quarantine", "acme", "tool", "README")
	write(t, foreignNote, "not ours\n")

	foreignSkill := filepath.Join(claudeSkillsDir(f.home), "Foreign")
	write(t, filepath.Join(foreignSkill, "SKILL.md"), "# foreign\n")

	results, err := f.engine.Heal(t.Context(), false)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Contains(t, results[0].Note, "is not empty")
	require.Equal(t, 2, results[0].Stubs)

	require.Equal(t, "not ours\n", read(t, foreignNote))
	require.NoFileExists(t, quarantineCurrent(f, "acme", "tool"))
	require.Equal(t, "# foreign\n", read(t, filepath.Join(foreignSkill, "SKILL.md")))
	require.False(t, isStub(t, filepath.Join(claudeSkillsDir(f.home), "alpha")))

	rec := ledgerRecord(t, f, "acme/tool")
	require.False(t, rec.RetiredAt.IsZero())
}

func TestHealKeepsOwnership(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")
	write(t, filepath.Join(plugin, ".mcp.json"), `{"mcpServers": {"plug": {"command": "plug"}}}`)

	report := f.sync(t)
	require.Empty(t, report.Kind(kind.MCP).Warnings)
	require.Contains(t, hostMCPServers(t, f.claudeConfig(), "mcpServers"), "plug")

	removeFromRegistry(t, f.home, "acme", "tool")
	require.NoError(t, os.RemoveAll(plugin))

	f.sync(t)

	_, err := f.engine.Heal(t.Context(), false)
	require.NoError(t, err)

	rec := ledgerRecord(t, f, "acme/tool")
	require.Equal(t, []string{"plug"}, rec.Servers, "heal keeps the U-10 ownership list")
	require.False(t, rec.RetiredAt.IsZero())

	write(t, f.claudeConfig(), `{"mcpServers": {"plug": {"type": "stdio", "command": "/tmp/evil"}}}`)

	f.sync(t)

	require.NoFileExists(t, f.vault.ServersPath(), "a retired plugin server must never be adopted")
	require.NotContains(t, hostMCPServers(t, f.claudeConfig(), "mcpServers"), "plug")
}

func TestPluginQuarantineDoctorErrors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	quarantinePlugin(t, f)

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityError, "is quarantined since"), "issues: %v", issues)
	require.True(t, hasIssue(issues, engine.SeverityError, "run agent-sync heal"), "issues: %v", issues)

	_, err = f.engine.Heal(t.Context(), false)
	require.NoError(t, err)

	issues, err = f.engine.Doctor(t.Context())
	require.NoError(t, err)

	for _, issue := range issues {
		require.NotContains(t, issue.Message, "acme/tool", "a healed plugin must be silent: %v", issue)
	}
}
