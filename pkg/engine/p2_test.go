package engine_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
)

func pinSkillPath(f *fixture, version, skill string) string {
	return filepath.Join(f.vault.PluginsDir(), "acme", "tool", "at-"+version, "skills", skill)
}

func currentSkillPath(f *fixture, skill string) string {
	return filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current", "skills", skill)
}

func pinPath(f *fixture, version string) string {
	return filepath.Join(f.vault.PluginsDir(), "acme", "tool", "at-"+version)
}

func pinVersion(t *testing.T, f *fixture, agentID, version string) {
	t.Helper()

	require.NoError(t, f.config.SetPluginPin(agentID, "acme/tool", version))
	require.NoError(t, f.config.Save(f.vault.ConfigPath()))
}

func TestPluginPinFarmUsesPinnedPivot(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	v1 := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, v1, "alpha", "# v1\n")

	v2 := pluginTree(t, f.home, "acme", "tool", "2.0.0")
	writeSkill(t, v2, "alpha", "# v2\n")

	pinVersion(t, f, agent.OpenCodeID, "1.0.0")

	f.sync(t)

	claudeDir := claudeSkillsDir(f.home)
	openCodeDir := openCodeSkillsDir(f.home)

	require.Equal(t, pinSkillPath(f, "1.0.0", "alpha"), farmLink(t, openCodeDir, "alpha"))
	require.Equal(t, currentSkillPath(f, "alpha"), farmLink(t, claudeDir, "alpha"))
	require.Equal(t, "# v1\n", read(t, filepath.Join(openCodeDir, "alpha", "SKILL.md")))
	require.Equal(t, "# v2\n", read(t, filepath.Join(claudeDir, "alpha", "SKILL.md")))
	require.Equal(t, v1, readLink(t, pinPath(f, "1.0.0")))

	v3 := pluginTree(t, f.home, "acme", "tool", "3.0.0")
	writeSkill(t, v3, "alpha", "# v3\n")

	f.sync(t)

	require.Equal(t, pinSkillPath(f, "1.0.0", "alpha"), farmLink(t, openCodeDir, "alpha"))
	require.Equal(t, currentSkillPath(f, "alpha"), farmLink(t, claudeDir, "alpha"))
	require.Equal(t, "# v1\n", read(t, filepath.Join(openCodeDir, "alpha", "SKILL.md")))
	require.Equal(t, "# v3\n", read(t, filepath.Join(claudeDir, "alpha", "SKILL.md")))
	require.Equal(t, v1, readLink(t, pinPath(f, "1.0.0")))
	require.Equal(t, v3, readLink(t, filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current")))
}

func TestPluginPinMissingVersionIsSkipped(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# v1\n")

	pinVersion(t, f, agent.OpenCodeID, "9.9.9")

	report := f.sync(t)

	want := "pinned version 9.9.9 of acme/tool is not in the plugin cache; keeping the pin (no silent upgrade)"
	require.Contains(t, strings.Join(report.Warnings, " "), want)

	require.Equal(t, currentSkillPath(f, "alpha"), farmLink(t, claudeSkillsDir(f.home), "alpha"))

	entries, err := os.ReadDir(openCodeSkillsDir(f.home))
	require.NoError(t, err)
	require.Empty(t, entries, "a missing pinned version must not be replaced by current")

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityError, "9.9.9"), "issues: %v", issues)
	require.True(t, hasIssue(issues, engine.SeverityError, agent.OpenCodeID), "issues: %v", issues)
}

func TestPluginPinUnknownPluginWarns(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# v1\n")

	require.NoError(t, f.config.SetPluginPin(agent.OpenCodeID, "ghost/tool", "1.0.0"))
	require.NoError(t, f.config.Save(f.vault.ConfigPath()))

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityWarn, "ghost/tool is not installed"), "issues: %v", issues)
}

func TestPluginPinRepointOnChange(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	v1 := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, v1, "alpha", "# v1\n")

	v2 := pluginTree(t, f.home, "acme", "tool", "2.0.0")
	writeSkill(t, v2, "alpha", "# v2\n")

	pinVersion(t, f, agent.OpenCodeID, "1.0.0")
	f.sync(t)

	openCodeDir := openCodeSkillsDir(f.home)
	require.Equal(t, pinSkillPath(f, "1.0.0", "alpha"), farmLink(t, openCodeDir, "alpha"))

	pinVersion(t, f, agent.OpenCodeID, "2.0.0")
	f.sync(t)

	require.Equal(t, pinSkillPath(f, "2.0.0", "alpha"), farmLink(t, openCodeDir, "alpha"))
	require.Equal(t, "# v2\n", read(t, filepath.Join(openCodeDir, "alpha", "SKILL.md")))

	_, err := os.Lstat(pinPath(f, "1.0.0"))
	require.ErrorIs(t, err, fs.ErrNotExist, "a pin that is no longer active must be pruned")
}

func TestPluginPinUnpinReturnsToCurrent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# v1\n")

	pinVersion(t, f, agent.OpenCodeID, "1.0.0")
	f.sync(t)

	openCodeDir := openCodeSkillsDir(f.home)
	require.Equal(t, pinSkillPath(f, "1.0.0", "alpha"), farmLink(t, openCodeDir, "alpha"))

	f.config.UnsetPluginPin(agent.OpenCodeID, "acme/tool")
	require.NoError(t, f.config.Save(f.vault.ConfigPath()))

	f.sync(t)

	require.Equal(t, currentSkillPath(f, "alpha"), farmLink(t, openCodeDir, "alpha"))

	_, err := os.Lstat(pinPath(f, "1.0.0"))
	require.ErrorIs(t, err, fs.ErrNotExist, "unset pins must be pruned")
}

func TestPluginPinMCPRoot(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	v1 := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeMCPServers(t, v1, `{"mcpServers": {"plug": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug", "args": ["serve"]}}}`)

	v2 := pluginTree(t, f.home, "acme", "tool", "2.0.0")
	writeMCPServers(t, v2, `{"mcpServers": {"plug": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug"}}}`)

	pinVersion(t, f, agent.OpenCodeID, "1.0.0")

	f.sync(t)

	claude := hostMCPServers(t, f.claudeConfig(), "mcpServers")
	openCode := hostMCPServers(t, f.openCodeConfig(), "mcp")

	require.Equal(t, filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current", "bin", "plug"), claude["plug"]["command"])
	require.Equal(t, []any{filepath.Join(f.vault.PluginsDir(), "acme", "tool", "at-1.0.0", "bin", "plug"), "serve"}, openCode["plug"]["command"])
}

func TestPluginPinDifferentSkillSet(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	v1 := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, v1, "alpha", "# v1 alpha\n")

	v2 := pluginTree(t, f.home, "acme", "tool", "2.0.0")
	writeSkill(t, v2, "alpha", "# v2 alpha\n")
	writeSkill(t, v2, "beta", "# v2 beta\n")

	pinVersion(t, f, agent.OpenCodeID, "1.0.0")

	report := f.sync(t)

	claudeDir := claudeSkillsDir(f.home)
	openCodeDir := openCodeSkillsDir(f.home)

	require.Equal(t, pinSkillPath(f, "1.0.0", "alpha"), farmLink(t, openCodeDir, "alpha"))
	require.Equal(t, currentSkillPath(f, "alpha"), farmLink(t, claudeDir, "alpha"))
	require.Equal(t, currentSkillPath(f, "beta"), farmLink(t, claudeDir, "beta"))

	_, err := os.Lstat(filepath.Join(openCodeDir, "beta"))
	require.ErrorIs(t, err, fs.ErrNotExist, "a skill absent from the pinned version must not be linked")
	require.Contains(t, strings.Join(report.Warnings, " "), "skill beta is missing in acme/tool@1.0.0; skipped")

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)

	for _, issue := range issues {
		require.NotContains(t, issue.Message, "beta", "no dangling pinned links: %v", issue)
	}
}

func TestPluginPinDropsSkillMissingInPinnedVersion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	v1 := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, v1, "alpha", "# v1 alpha\n")

	v2 := pluginTree(t, f.home, "acme", "tool", "2.0.0")
	writeSkill(t, v2, "alpha", "# v2 alpha\n")
	writeSkill(t, v2, "beta", "# v2 beta\n")

	f.sync(t)

	openCodeDir := openCodeSkillsDir(f.home)
	require.Equal(t, currentSkillPath(f, "beta"), farmLink(t, openCodeDir, "beta"))

	pinVersion(t, f, agent.OpenCodeID, "1.0.0")

	f.sync(t)

	require.Equal(t, pinSkillPath(f, "1.0.0", "alpha"), farmLink(t, openCodeDir, "alpha"))

	_, err := os.Lstat(filepath.Join(openCodeDir, "beta"))
	require.ErrorIs(t, err, fs.ErrNotExist, "a stale link to a skill absent from the pinned version must be pruned")

	require.Equal(t, currentSkillPath(f, "beta"), farmLink(t, claudeSkillsDir(f.home), "beta"))
}

func TestPluginPinMissingVersionDropsStaleLinks(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# v1\n")
	writeMCPServers(t, plugin, `{"mcpServers": {"plug": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug"}}}`)

	f.sync(t)

	openCodeDir := openCodeSkillsDir(f.home)
	require.Equal(t, currentSkillPath(f, "alpha"), farmLink(t, openCodeDir, "alpha"))
	require.Contains(t, hostMCPServers(t, f.openCodeConfig(), "mcp"), "plug")

	pinVersion(t, f, agent.OpenCodeID, "9.9.9")

	f.sync(t)

	entries, err := os.ReadDir(openCodeDir)
	require.NoError(t, err)
	require.Empty(t, entries, "links of an unresolvable pin must not survive as silent upgrades")
	require.NotContains(t, hostMCPServers(t, f.openCodeConfig(), "mcp"), "plug")

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityError, "9.9.9"), "issues: %v", issues)
}

func TestPluginPinHealRepoints(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	v1 := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, v1, "alpha", "# v1\n")

	pinVersion(t, f, agent.OpenCodeID, "1.0.0")
	f.sync(t)

	openCodeDir := openCodeSkillsDir(f.home)
	require.NoError(t, os.Remove(filepath.Join(openCodeDir, "alpha")))

	versionedLink(t, openCodeDir, "alpha", filepath.Join(v1, "skills", "alpha"))

	results, err := f.engine.Heal(t.Context(), false)
	require.NoError(t, err)
	require.NotEmpty(t, results)

	require.Equal(t, pinSkillPath(f, "1.0.0", "alpha"), farmLink(t, openCodeDir, "alpha"))
}

func readLink(t *testing.T, path string) string {
	t.Helper()

	link, err := os.Readlink(path)
	require.NoError(t, err)

	return link
}
