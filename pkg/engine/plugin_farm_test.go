package engine_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/agent"
	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/engine"
	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
)

func claudeSkillsDir(home string) string {
	return filepath.Join(home, ".claude", "skills")
}

func openCodeSkillsDir(home string) string {
	return filepath.Join(home, ".config", "opencode", "skills")
}

func sharedSkillsDir(home string) string {
	return filepath.Join(home, ".agents", "skills")
}

func writeSkill(t *testing.T, pluginDir, name, content string) {
	t.Helper()

	write(t, filepath.Join(pluginDir, "skills", name, "SKILL.md"), content)
}

func farmLink(t *testing.T, dir, name string) string {
	t.Helper()

	link, err := os.Readlink(filepath.Join(dir, name))
	require.NoError(t, err)

	return link
}

func inode(t *testing.T, path string) uint64 {
	t.Helper()

	info, err := os.Lstat(path)
	require.NoError(t, err)

	stat, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok)

	return stat.Ino
}

func TestPluginFarmPresentsSkills(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")
	writeSkill(t, plugin, "beta", "# beta\n")

	report := f.sync(t)

	wantAlpha := filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current", "skills", "alpha")
	wantBeta := filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current", "skills", "beta")

	for _, dir := range []string{claudeSkillsDir(f.home), openCodeSkillsDir(f.home)} {
		require.Equal(t, wantAlpha, farmLink(t, dir, "alpha"))
		require.Equal(t, wantBeta, farmLink(t, dir, "beta"))

		content, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md")) //nolint:gosec // G304: test reads its own temp file
		require.NoError(t, err)
		require.Equal(t, "# alpha\n", string(content))
	}

	require.Len(t, report.Farm, 2)

	for _, result := range report.Farm {
		require.Equal(t, engine.FarmLinked, result.Action)
		require.Equal(t, "acme/tool", result.Plugin)
		require.Equal(t, 2, result.Count)
	}

	entries, err := os.ReadDir(f.vault.SkillsDir())
	require.NoError(t, err)
	require.Empty(t, entries, "the farm must not touch the vault canon")
}

func TestPluginFarmInvisibleToSync(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")

	f.sync(t)

	ledger := read(t, f.vault.PluginsLedgerPath())

	report := f.sync(t)
	require.Empty(t, report.Farm)
	require.Equal(t, engine.ActionNoop, report.Action(kind.Skills, agent.ClaudeCodeID))
	require.Equal(t, ledger, read(t, f.vault.PluginsLedgerPath()))
	require.NoFileExists(t, filepath.Join(f.vault.SkillsDir(), "alpha"))
}

func TestPluginFarmSurvivesUpgrade(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha v1\n")

	f.sync(t)

	linkBefore := farmLink(t, claudeSkillsDir(f.home), "alpha")

	upgraded := pluginTree(t, f.home, "acme", "tool", "2.0.0")
	writeSkill(t, upgraded, "alpha", "# alpha v2\n")

	report := f.sync(t)
	require.Empty(t, report.Farm)
	require.Equal(t, linkBefore, farmLink(t, claudeSkillsDir(f.home), "alpha"))
	require.Equal(t, linkBefore, farmLink(t, openCodeSkillsDir(f.home), "alpha"))
	require.Equal(t, "# alpha v2\n", read(t, filepath.Join(claudeSkillsDir(f.home), "alpha", "SKILL.md")))
}

func TestPluginFarmPrunesDroppedSkill(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")
	writeSkill(t, plugin, "beta", "# beta\n")

	f.sync(t)

	upgraded := pluginTree(t, f.home, "acme", "tool", "2.0.0")
	writeSkill(t, upgraded, "beta", "# beta\n")

	report := f.sync(t)

	require.NoFileExists(t, filepath.Join(claudeSkillsDir(f.home), "alpha"))
	require.NoFileExists(t, filepath.Join(openCodeSkillsDir(f.home), "alpha"))
	require.Equal(t, filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current", "skills", "beta"), farmLink(t, claudeSkillsDir(f.home), "beta"))

	require.Len(t, report.Farm, 2)

	for _, result := range report.Farm {
		require.Equal(t, engine.FarmPruned, result.Action)
		require.Equal(t, 1, result.Count)
	}
}

func TestPluginFarmCollisions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	t.Run("the first plugin by key wins", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		first := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, first, "alpha", "# first\n")

		second := pluginTree(t, f.home, "beta", "other", "1.0.0")
		writeSkill(t, second, "alpha", "# second\n")

		report := f.sync(t)

		require.Equal(t,
			filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current", "skills", "alpha"),
			farmLink(t, claudeSkillsDir(f.home), "alpha"))

		warned := containsWarning(report.Warnings, "already provided by acme/tool")
		require.True(t, warned, "warnings: %v", report.Warnings)
	})

	t.Run("the canon wins and lands in the same sync", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# plugin\n")

		f.sync(t)
		require.Equal(t,
			filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current", "skills", "alpha"),
			farmLink(t, claudeSkillsDir(f.home), "alpha"))

		write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# canon\n")

		report := f.sync(t)

		info, err := os.Lstat(filepath.Join(claudeSkillsDir(f.home), "alpha"))
		require.NoError(t, err)
		require.Zero(t, info.Mode()&fs.ModeSymlink, "the canon must land as a real directory")
		require.Equal(t, "# canon\n", read(t, filepath.Join(claudeSkillsDir(f.home), "alpha", "SKILL.md")))

		var pruned []engine.FarmResult

		for _, result := range report.Farm {
			if result.Action == engine.FarmPruned {
				pruned = append(pruned, result)
			}
		}

		require.Len(t, pruned, 2)
	})

	t.Run("the canon blocks the farm before the first sync", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# plugin\n")

		write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# canon\n")

		report := f.sync(t)

		info, err := os.Lstat(filepath.Join(claudeSkillsDir(f.home), "alpha"))
		require.NoError(t, err)
		require.Zero(t, info.Mode()&fs.ModeSymlink)
		require.Equal(t, "# canon\n", read(t, filepath.Join(claudeSkillsDir(f.home), "alpha", "SKILL.md")))
		require.Empty(t, report.Farm)

		require.True(t, containsWarning(report.Warnings, "shadowed by the vault canon"), "warnings: %v", report.Warnings)
	})

	t.Run("foreign entries are never touched", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# plugin alpha\n")
		writeSkill(t, plugin, "beta", "# plugin beta\n")

		claudeDir := claudeSkillsDir(f.home)
		require.NoError(t, os.MkdirAll(claudeDir, 0o700))

		foreignTarget := filepath.Join(f.home, "elsewhere", "alpha")
		write(t, filepath.Join(foreignTarget, "SKILL.md"), "# foreign\n")
		require.NoError(t, os.Symlink(foreignTarget, filepath.Join(claudeDir, "alpha")))

		realDir := filepath.Join(claudeDir, "beta")
		write(t, filepath.Join(realDir, "SKILL.md"), "# real\n")

		realInode := inode(t, realDir)

		report := f.sync(t)

		link, err := os.Readlink(filepath.Join(claudeDir, "alpha"))
		require.NoError(t, err)
		require.Equal(t, foreignTarget, link)
		require.Equal(t, realInode, inode(t, realDir))

		var skipped []engine.FarmResult

		for _, result := range report.Farm {
			if result.Action == engine.FarmSkipped {
				skipped = append(skipped, result)
			}
		}

		require.Len(t, skipped, 1)
		require.Equal(t, "alpha, beta", skipped[0].Note)
	})
}

func TestPluginFarmRemoval(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")

	f.sync(t)

	want := filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current", "skills", "alpha")

	removeFromRegistry(t, f.home, "acme", "tool")

	report := f.sync(t)
	require.Empty(t, report.Farm)
	require.Equal(t, want, farmLink(t, claudeSkillsDir(f.home), "alpha"))
	require.True(t, fsutil.Exists(filepath.Join(claudeSkillsDir(f.home), "alpha")), "the cached target still resolves")

	require.NoError(t, os.RemoveAll(plugin))

	report = f.sync(t)
	require.Empty(t, report.Farm)
	require.Equal(t, want, farmLink(t, claudeSkillsDir(f.home), "alpha"))
	require.False(t, fsutil.Exists(filepath.Join(claudeSkillsDir(f.home), "alpha")), "the link dangles now")

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityError, "broken symlink"), "issues: %v", issues)
}

func TestPluginFarmDryRun(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")

	report := f.run(t, engine.SyncOptions{DryRun: true})
	require.Empty(t, report.Farm)
	require.NoDirExists(t, claudeSkillsDir(f.home))
	require.NoDirExists(t, openCodeSkillsDir(f.home))
}

func TestPluginFarmSkipsOffAndDisabled(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	t.Run("a host with skills off is not farmed", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		f.config.SetMode(agent.OpenCodeID, kind.Skills, config.ModeOff)

		f.sync(t)

		require.Equal(t,
			filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current", "skills", "alpha"),
			farmLink(t, claudeSkillsDir(f.home), "alpha"))
		require.NoDirExists(t, openCodeSkillsDir(f.home))
	})

	t.Run("a disabled skills kind is not farmed", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		f.config.SetKind(kind.Skills, config.ModeOff)

		report := f.sync(t)
		require.Empty(t, report.Farm)
		require.NoDirExists(t, claudeSkillsDir(f.home))
		require.NoDirExists(t, openCodeSkillsDir(f.home))
	})

	t.Run("an opt-in host that is not enabled is not farmed", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		f.sync(t)

		require.NoDirExists(t, sharedSkillsDir(f.home))
	})
}

func TestPluginFarmNoPruneOnLedgerError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")

	f.sync(t)

	linkBefore := farmLink(t, claudeSkillsDir(f.home), "alpha")

	require.NoError(t, os.Remove(f.vault.PluginsLedgerPath()))
	require.NoError(t, os.MkdirAll(f.vault.PluginsLedgerPath(), 0o700))

	report := f.sync(t)

	require.Equal(t, linkBefore, farmLink(t, claudeSkillsDir(f.home), "alpha"))
	require.Equal(t, linkBefore, farmLink(t, openCodeSkillsDir(f.home), "alpha"))
	require.Empty(t, report.Farm)
	require.True(t, containsWarning(report.Warnings, "ledger"), "warnings: %v", report.Warnings)
}

func TestPluginFarmSkipsTargetOutsideCache(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	outside := filepath.Join(f.home, "elsewhere", "tool")
	write(t, filepath.Join(outside, "skills", "alpha", "SKILL.md"), "# outside\n")
	write(t, filepath.Join(outside, ".claude-plugin", "plugin.json"), `{"name": "tool", "version": "1.0.0"}`)

	writeRegistry(t, f.home, map[string][]map[string]any{"tool@acme": {{
		"scope": "user", "installPath": outside, "version": "1.0.0",
	}}})

	report := f.sync(t)

	require.NoDirExists(t, claudeSkillsDir(f.home))
	require.NoDirExists(t, openCodeSkillsDir(f.home))
	require.True(t, containsWarning(report.Warnings, "outside the plugin cache"), "warnings: %v", report.Warnings)

	entries, err := os.ReadDir(f.vault.SkillsDir())
	require.NoError(t, err)
	require.Empty(t, entries, "the plugin content must not leak into the canon")
}

func TestPluginFarmSkipsSkillSymlinkedOutsideCache(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")

	outside := filepath.Join(f.home, "notes", "helper")
	write(t, filepath.Join(outside, "SKILL.md"), "# outside\n")

	require.NoError(t, os.MkdirAll(filepath.Join(plugin, "skills"), 0o700))
	require.NoError(t, os.Symlink(outside, filepath.Join(plugin, "skills", "helper")))

	report := f.sync(t)

	require.Len(t, report.Farm, 2, "only the regular skill is farmed: %v", report.Farm)

	for _, result := range report.Farm {
		require.Equal(t, engine.FarmLinked, result.Action)
		require.Equal(t, 1, result.Count)
	}

	require.NoFileExists(t, filepath.Join(claudeSkillsDir(f.home), "helper"))
	require.True(t, containsWarning(report.Warnings, "resolves outside the plugin cache"), "warnings: %v", report.Warnings)

	entries, err := os.ReadDir(f.vault.SkillsDir())
	require.NoError(t, err)
	require.Empty(t, entries, "the linked-out content must not leak into the canon")
}

func TestPluginFarmNoPruneOnUnreadableSkills(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")

	f.sync(t)

	linkBefore := farmLink(t, claudeSkillsDir(f.home), "alpha")

	skillsPath := filepath.Join(plugin, "skills")
	require.NoError(t, os.Chmod(skillsPath, 0o000))

	t.Cleanup(func() {
		_ = os.Chmod(skillsPath, 0o700) //nolint:gosec // G302: restoring the directory mode needs the execute bit
	})

	report := f.sync(t)

	require.Empty(t, report.Farm)
	require.Equal(t, linkBefore, farmLink(t, claudeSkillsDir(f.home), "alpha"))
	require.Equal(t, linkBefore, farmLink(t, openCodeSkillsDir(f.home), "alpha"))
	require.True(t, containsWarning(report.Warnings, "skills cannot be read"), "warnings: %v", report.Warnings)
}

func TestPluginFarmGatesDirectionAndKinds(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")

	f.sync(t)

	linkBefore := farmLink(t, claudeSkillsDir(f.home), "alpha")

	pluginTree(t, f.home, "acme", "tool", "2.0.0")

	_, err := f.engine.Sync(t.Context(), engine.SyncOptions{Direction: config.ModePull})
	require.NoError(t, err)
	require.Equal(t, linkBefore, farmLink(t, claudeSkillsDir(f.home), "alpha"), "pull must not touch the farm")

	report := f.run(t, engine.SyncOptions{Kinds: []kind.ID{kind.MCP}})
	require.Empty(t, report.Farm)
	require.Equal(t, linkBefore, farmLink(t, claudeSkillsDir(f.home), "alpha"), "a narrowed kind set must not prune the farm")
}

func containsWarning(warnings []string, substr string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, substr) {
			return true
		}
	}

	return false
}
