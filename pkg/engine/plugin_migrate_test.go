package engine_test

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func versionedLink(t *testing.T, dir, name, target string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, name)))
}

func hashTree(t *testing.T, root string) string {
	t.Helper()

	var out strings.Builder

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		require.NoError(t, err)

		rel, err := filepath.Rel(root, path)
		require.NoError(t, err)

		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			require.NoError(t, err)

			fmt.Fprintf(&out, "l %s -> %s\n", rel, link)
		case entry.IsDir():
			fmt.Fprintf(&out, "d %s\n", rel)
		default:
			data, err := os.ReadFile(path) //nolint:gosec // G304: the test reads its own temp files
			require.NoError(t, err)

			fmt.Fprintf(&out, "f %s %x\n", rel, sha256.Sum256(data))
		}

		return nil
	})
	require.NoError(t, err)

	return out.String()
}

func pivotSkillsDir(f *fixture, marketplace, name string) string {
	return filepath.Join(f.vault.PluginsDir(), marketplace, name, "current", "skills")
}

func TestMigrateVersionedLinks(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")
	writeSkill(t, plugin, "beta", "# beta\n")

	dir := claudeSkillsDir(f.home)
	versionedLink(t, dir, "alpha", plugin+"/skills/alpha/")
	versionedLink(t, dir, "beta", filepath.Join(claudePluginsDir(f.home), "cache", "acme", "tool", "2.0.0", "skills", "beta")+"/")

	brewTarget := filepath.Join(f.home, "brew", "tool")
	write(t, brewTarget, "brew\n")
	versionedLink(t, dir, "brew", brewTarget)

	f.sync(t)

	brewBefore := hashTree(t, filepath.Join(f.home, "brew"))

	results, err := f.engine.Heal(t.Context(), false)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "acme/tool", results[0].Key)
	require.Equal(t, 2, results[0].Migrated)

	pivot := pivotSkillsDir(f, "acme", "tool")

	link, err := os.Readlink(filepath.Join(dir, "alpha"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(pivot, "alpha"), link)
	require.Equal(t, "# alpha\n", read(t, filepath.Join(dir, "alpha", "SKILL.md")))

	link, err = os.Readlink(filepath.Join(dir, "beta"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(pivot, "beta"), link)
	require.Equal(t, "# beta\n", read(t, filepath.Join(dir, "beta", "SKILL.md")))

	link, err = os.Readlink(filepath.Join(dir, "brew"))
	require.NoError(t, err)
	require.Equal(t, brewTarget, link)
	require.Equal(t, brewBefore, hashTree(t, filepath.Join(f.home, "brew")), "a foreign link must stay untouched")

	results, err = f.engine.Heal(t.Context(), false)
	require.NoError(t, err)
	require.Empty(t, results, "a repeated heal is a noop")
}

func TestMigrateOwnerGate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	first := pluginTree(t, f.home, "aaa", "first", "1.0.0")
	writeSkill(t, first, "shared", "# first\n")

	second := pluginTree(t, f.home, "bbb", "second", "1.0.0")
	writeSkill(t, second, "shared", "# second\n")

	dir := claudeSkillsDir(f.home)
	versionedLink(t, dir, "shared-a", first+"/skills/shared/")
	versionedLink(t, dir, "shared-b", second+"/skills/shared/")

	f.sync(t)

	results, err := f.engine.Heal(t.Context(), false)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "aaa/first", results[0].Key)
	require.Equal(t, 1, results[0].Migrated)

	link, err := os.Readlink(filepath.Join(dir, "shared-b"))
	require.NoError(t, err)
	require.Equal(t, second+"/skills/shared/", link, "the loser's link is left alone")

	link, err = os.Readlink(filepath.Join(dir, "shared-a"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(pivotSkillsDir(f, "aaa", "first"), "shared"), link)
}

func TestMigrateKeepsUnresolvableLink(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	t.Run("the skill is gone from the lot", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		dir := claudeSkillsDir(f.home)
		versionedLink(t, dir, "ghost", plugin+"/skills/ghost/")

		f.sync(t)

		results, err := f.engine.Heal(t.Context(), false)
		require.NoError(t, err)
		require.Empty(t, results)

		link, err := os.Readlink(filepath.Join(dir, "ghost"))
		require.NoError(t, err)
		require.Equal(t, plugin+"/skills/ghost/", link)

		issues, err := f.engine.Doctor(t.Context())
		require.NoError(t, err)
		require.True(t, hasIssue(issues, engine.SeverityWarn, "skill ghost points into the plugin cache"), "issues: %v", issues)
	})

	t.Run("the plugin is not parked", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		f.sync(t)

		removeFromRegistry(t, f.home, "acme", "tool")
		require.NoError(t, os.RemoveAll(plugin))

		dir := claudeSkillsDir(f.home)
		versionedLink(t, dir, "ghost", plugin+"/skills/alpha/")

		results, err := f.engine.Heal(t.Context(), false)
		require.NoError(t, err)
		require.Empty(t, results)

		link, err := os.Readlink(filepath.Join(dir, "ghost"))
		require.NoError(t, err)
		require.Equal(t, plugin+"/skills/alpha/", link)

		issues, err := f.engine.Doctor(t.Context())
		require.NoError(t, err)
		require.True(t, hasIssue(issues, engine.SeverityWarn, "plugin acme/tool is not parked"), "issues: %v", issues)
		require.False(t, hasIssue(issues, engine.SeverityError, "broken symlink: "+filepath.Join(dir, "ghost")), "a plugin-cache link is not a broken-symlink error: %v", issues)
	})
}

func TestMigrateIdenticalFork(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")

	f.sync(t)

	dir := openCodeSkillsDir(f.home)
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "alpha")))
	write(t, filepath.Join(dir, "alpha", "SKILL.md"), "# alpha\n")

	results, err := f.engine.Heal(t.Context(), false)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "acme/tool", results[0].Key)
	require.Equal(t, 1, results[0].Migrated)
	require.Empty(t, results[0].Note)

	link, err := os.Readlink(filepath.Join(dir, "alpha"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(pivotSkillsDir(f, "acme", "tool"), "alpha"), link)
	require.Equal(t, "# alpha\n", read(t, filepath.Join(dir, "alpha", "SKILL.md")))

	entries, err := os.ReadDir(f.vault.SkillsDir())
	require.NoError(t, err)
	require.Empty(t, entries, "the canon must stay empty")

	links, err := filepath.Glob(filepath.Join(dir, "*.migrating"))
	require.NoError(t, err)
	require.Empty(t, links, "a successful swap leaves no aside copies")
}

func TestMigrateForkAsideConflict(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	// The symlink-failure branch of replaceFork (the fork comes back from the
	// aside name) is not portably reproducible: it needs the rename aside to
	// succeed while creating the symlink in the same directory fails.

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")
	writeSkill(t, plugin, "beta", "# beta\n")

	f.sync(t)

	dir := openCodeSkillsDir(f.home)

	require.NoError(t, os.RemoveAll(filepath.Join(dir, "alpha")))
	write(t, filepath.Join(dir, "alpha", "SKILL.md"), "# alpha\n")

	aside := filepath.Join(dir, "alpha.migrating")
	write(t, aside, "occupied\n")

	claudeDir := claudeSkillsDir(f.home)
	require.NoError(t, os.Remove(filepath.Join(claudeDir, "beta")))
	versionedLink(t, claudeDir, "beta", plugin+"/skills/beta/")

	results, err := f.engine.Heal(t.Context(), false)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, 1, results[0].Migrated)
	require.Contains(t, results[0].Note, "cannot replace "+filepath.Join(dir, "alpha"), "the aside conflict is noted: %v", results)
	require.NotContains(t, results[0].Note, "restored", "the rename must fail before any symlink attempt")

	fork := filepath.Join(dir, "alpha")
	info, err := os.Lstat(fork)
	require.NoError(t, err)
	require.Zero(t, info.Mode()&fs.ModeSymlink, "a failed swap keeps the fork a directory")
	require.Equal(t, "# alpha\n", read(t, filepath.Join(fork, "SKILL.md")))
	require.Equal(t, "occupied\n", read(t, aside), "the conflicting aside entry is not touched")
}

func TestMigrateIdenticalForkBeforeSync(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")

	dir := openCodeSkillsDir(f.home)
	write(t, filepath.Join(dir, "alpha", "SKILL.md"), "# alpha\n")

	f.sync(t)

	require.FileExists(t, f.vaultSkill("alpha"), "the pull adopts the fork into the canon")

	results, err := f.engine.Heal(t.Context(), false)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "acme/tool", results[0].Key)
	require.Equal(t, 1, results[0].Migrated)

	link, err := os.Readlink(filepath.Join(dir, "alpha"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(pivotSkillsDir(f, "acme", "tool"), "alpha"), link, "a canon copy identical to the lot must not block the migration")
	require.Equal(t, "# alpha\n", read(t, f.vaultSkill("alpha")))
}

func TestMigrateDriftedForkBeforeSyncKeepsAndWarns(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")

	dir := openCodeSkillsDir(f.home)
	write(t, filepath.Join(dir, "alpha", "SKILL.md"), "# alphA\n")

	f.sync(t)

	results, err := f.engine.Heal(t.Context(), false)
	require.NoError(t, err)
	require.Empty(t, results)

	info, err := os.Lstat(filepath.Join(dir, "alpha"))
	require.NoError(t, err)
	require.Zero(t, info.Mode()&fs.ModeSymlink, "a drifted fork stays a directory")
	require.Equal(t, "# alphA\n", read(t, filepath.Join(dir, "alpha", "SKILL.md")))

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityWarn, "skill alpha looks like a drifted copy of acme/tool"), "a drifted fork warns even with a canon shadow: %v", issues)
}

func TestMigrateIdenticalForkDivergedCanonKeepsBoth(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")
	writeSkill(t, plugin, "beta", "# beta\n")

	f.sync(t)

	write(t, f.vaultSkill("alpha"), "# canon edit\n")

	dir := openCodeSkillsDir(f.home)
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "alpha")))
	write(t, filepath.Join(dir, "alpha", "SKILL.md"), "# alpha\n")

	claudeDir := claudeSkillsDir(f.home)
	require.NoError(t, os.Remove(filepath.Join(claudeDir, "beta")))
	versionedLink(t, claudeDir, "beta", plugin+"/skills/beta/")

	results, err := f.engine.Heal(t.Context(), false)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, 1, results[0].Migrated)
	require.Contains(t, results[0].Note, "matches acme/tool but the vault copy differs; keeping both")

	fork := filepath.Join(dir, "alpha")
	info, err := os.Lstat(fork)
	require.NoError(t, err)
	require.Zero(t, info.Mode()&fs.ModeSymlink, "an identical fork under a diverged canon stays a directory")
	require.Equal(t, "# alpha\n", read(t, filepath.Join(fork, "SKILL.md")))

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityWarn, "skill alpha matches acme/tool but the vault copy differs; keeping both"), "issues: %v", issues)
	require.False(t, hasIssue(issues, engine.SeverityWarn, "skill alpha looks like a drifted copy of acme/tool"), "an identical copy is not drifted: %v", issues)
}

func TestMigrateForkExtrasKeep(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	cases := []struct {
		desc  string
		extra func(t *testing.T, fork string)
	}{
		{desc: "junk directory", extra: func(t *testing.T, fork string) {
			t.Helper()

			write(t, filepath.Join(fork, "node_modules", "pkg", "index.js"), "x\n")
		}},
		{desc: "symlink", extra: func(t *testing.T, fork string) {
			t.Helper()

			require.NoError(t, os.Symlink("/tmp", filepath.Join(fork, "inner")))
		}},
		{desc: "empty directory", extra: func(t *testing.T, fork string) {
			t.Helper()

			require.NoError(t, os.MkdirAll(filepath.Join(fork, "empty"), 0o700))
		}},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", "")

			f := newFixture(t)
			f.emptyConfigs(t)

			plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
			writeSkill(t, plugin, "alpha", "# alpha\n")

			f.sync(t)

			dir := openCodeSkillsDir(f.home)
			fork := filepath.Join(dir, "alpha")

			require.NoError(t, os.RemoveAll(fork))
			write(t, filepath.Join(fork, "SKILL.md"), "# alpha\n")
			tc.extra(t, fork)

			before := hashTree(t, fork)

			results, err := f.engine.Heal(t.Context(), false)
			require.NoError(t, err)
			require.Empty(t, results, "an extra entry keeps the whole fork")
			require.Equal(t, before, hashTree(t, fork))

			info, err := os.Lstat(fork)
			require.NoError(t, err)
			require.Zero(t, info.Mode()&fs.ModeSymlink, "the fork must stay a real directory")
		})
	}
}

func TestMigrateDriftedForkKept(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")

	f.sync(t)

	dir := openCodeSkillsDir(f.home)
	fork := filepath.Join(dir, "alpha")

	require.NoError(t, os.RemoveAll(fork))
	write(t, filepath.Join(fork, "SKILL.md"), "# alphA\n")

	before := hashTree(t, fork)

	results, err := f.engine.Heal(t.Context(), false)
	require.NoError(t, err)
	require.Empty(t, results)
	require.Equal(t, before, hashTree(t, fork), "a drifted fork must stay byte-identical")

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityWarn, "skill alpha looks like a drifted copy of acme/tool"), "issues: %v", issues)
}

func TestMigrateForeignDirUntouched(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")

	f.sync(t)

	foreign := filepath.Join(claudeSkillsDir(f.home), "notes")
	write(t, filepath.Join(foreign, "SKILL.md"), "# notes\n")

	before := hashTree(t, foreign)

	results, err := f.engine.Heal(t.Context(), false)
	require.NoError(t, err)
	require.Empty(t, results)
	require.Equal(t, before, hashTree(t, foreign), "a directory no plugin owns must stay untouched")
}

func TestMigrateDryRunAndNoDirs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")

	f.sync(t)

	dir := openCodeSkillsDir(f.home)
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "alpha")))
	write(t, filepath.Join(dir, "alpha", "SKILL.md"), "# alpha\n")

	before := hashTree(t, dir)

	results, err := f.engine.Heal(t.Context(), true)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, 1, results[0].Migrated)
	require.Equal(t, before, hashTree(t, dir), "a dry run changes nothing")

	require.NoError(t, os.RemoveAll(claudeSkillsDir(f.home)))

	results, err = f.engine.Heal(t.Context(), false)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, 1, results[0].Migrated)
	require.NoDirExists(t, claudeSkillsDir(f.home), "a missing host directory is never created")
}

func TestMigrateNoopAfterHeal(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")

	f.sync(t)

	dir := openCodeSkillsDir(f.home)
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "alpha")))
	write(t, filepath.Join(dir, "alpha", "SKILL.md"), "# alpha\n")

	results, err := f.engine.Heal(t.Context(), false)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, 1, results[0].Migrated)

	results, err = f.engine.Heal(t.Context(), false)
	require.NoError(t, err)
	require.Empty(t, results)

	report := f.sync(t)
	require.Empty(t, report.Farm)
	require.Equal(t, engine.ActionNoop, report.Action(kind.Skills, agent.ClaudeCodeID))

	entries, err := os.ReadDir(f.vault.SkillsDir())
	require.NoError(t, err)
	require.Empty(t, entries, "the canon must stay empty")
}

func TestHealReportsMigration(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")
	writeSkill(t, plugin, "beta", "# beta\n")

	f.sync(t)

	claudeDir := claudeSkillsDir(f.home)
	require.NoError(t, os.Remove(filepath.Join(claudeDir, "alpha")))
	versionedLink(t, claudeDir, "alpha", plugin+"/skills/alpha/")

	openCodeDir := openCodeSkillsDir(f.home)

	require.NoError(t, os.RemoveAll(filepath.Join(openCodeDir, "alpha")))
	write(t, filepath.Join(openCodeDir, "alpha", "SKILL.md"), "# alpha\n")

	require.NoError(t, os.RemoveAll(filepath.Join(openCodeDir, "beta")))
	write(t, filepath.Join(openCodeDir, "beta", "SKILL.md"), "# betA\n")

	results, err := f.engine.Heal(t.Context(), false)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "acme/tool", results[0].Key)
	require.Equal(t, 2, results[0].Migrated)
	require.Contains(t, results[0].Note, "drifted copy of acme/tool; keeping the local version")
}

func TestDoctorPluginCacheLinkModeOff(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	f.config.SetMode(agent.ClaudeCodeID, kind.Skills, config.ModeOff)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")

	f.sync(t)

	dir := claudeSkillsDir(f.home)
	versionedLink(t, dir, "alpha", filepath.Join(claudePluginsDir(f.home), "cache", "acme", "tool", "9.9.9", "skills", "alpha")+"/")

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityError, "broken symlink: "+filepath.Join(dir, "alpha")), "a mode-off surface is not in the migration warn set: %v", issues)
	require.False(t, hasIssue(issues, engine.SeverityWarn, "points into the plugin cache"), "migration never scans mode-off surfaces: %v", issues)
}

func TestDoctorPluginMigrationIssues(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")
	writeSkill(t, plugin, "beta", "# beta\n")

	dir := claudeSkillsDir(f.home)
	versionedLink(t, dir, "alpha", plugin+"/skills/alpha/")
	versionedLink(t, dir, "beta", filepath.Join(claudePluginsDir(f.home), "cache", "acme", "tool", "2.0.0", "skills", "beta")+"/")

	broken := filepath.Join(dir, "broken")
	require.NoError(t, os.Symlink(filepath.Join(f.home, "missing"), broken))

	f.sync(t)

	openCodeDir := openCodeSkillsDir(f.home)
	require.NoError(t, os.RemoveAll(filepath.Join(openCodeDir, "alpha")))
	write(t, filepath.Join(openCodeDir, "alpha", "SKILL.md"), "# alphA\n")

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityWarn, "skill alpha points into the plugin cache"), "issues: %v", issues)
	require.True(t, hasIssue(issues, engine.SeverityWarn, "run beadle heal"), "issues: %v", issues)
	require.True(t, hasIssue(issues, engine.SeverityWarn, "skill alpha looks like a drifted copy of acme/tool"), "issues: %v", issues)
	require.True(t, hasIssue(issues, engine.SeverityError, "broken symlink: "+broken), "issues: %v", issues)
	require.False(t, hasIssue(issues, engine.SeverityError, "broken symlink: "+filepath.Join(dir, "beta")), "a plugin-cache dangling link must not be an error: %v", issues)

	_, err = f.engine.Heal(t.Context(), false)
	require.NoError(t, err)

	issues, err = f.engine.Doctor(t.Context())
	require.NoError(t, err)

	for _, issue := range issues {
		require.NotContains(t, issue.Message, "points into the plugin cache", "a healed link must be silent: %v", issue)
	}

	require.True(t, hasIssue(issues, engine.SeverityWarn, "drifted copy of acme/tool"), "the drift warn stays: %v", issues)
	require.True(t, hasIssue(issues, engine.SeverityError, "broken symlink: "+broken), "a foreign broken symlink stays an error: %v", issues)
}

func TestMigrationReasonNotParked(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")

	f.sync(t)

	removeFromRegistry(t, f.home, "acme", "tool")
	require.NoError(t, os.RemoveAll(plugin))

	dir := claudeSkillsDir(f.home)
	versionedLink(t, dir, "ghost", plugin+"/skills/alpha/")

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityWarn, "skill ghost points into the plugin cache; plugin acme/tool is not parked"), "issues: %v", issues)

	results, err := f.engine.Heal(t.Context(), false)
	require.NoError(t, err)
	require.Empty(t, results, "an unmigratable note without real work stays silent")
}

func TestMigrationReasonOwnerTaken(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	first := pluginTree(t, f.home, "aaa", "first", "1.0.0")
	writeSkill(t, first, "shared", "# first\n")

	second := pluginTree(t, f.home, "bbb", "second", "1.0.0")
	writeSkill(t, second, "shared", "# second\n")
	writeSkill(t, second, "omega", "# omega\n")

	dir := claudeSkillsDir(f.home)
	versionedLink(t, dir, "shared-a", first+"/skills/shared/")
	versionedLink(t, dir, "shared-b", second+"/skills/shared/")
	versionedLink(t, dir, "omega", second+"/skills/omega/")

	f.sync(t)

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityWarn, "skill shared is provided by aaa/first"), "issues: %v", issues)
	require.False(t, hasIssue(issues, engine.SeverityWarn, "plugin bbb/second is not parked"), "an owner conflict is not a parking problem: %v", issues)

	results, err := f.engine.Heal(t.Context(), false)
	require.NoError(t, err)
	require.Len(t, results, 2)

	byKey := map[string]engine.HealResult{}
	for _, result := range results {
		byKey[result.Key] = result
	}

	require.Equal(t, 1, byKey["aaa/first"].Migrated)
	require.Equal(t, 1, byKey["bbb/second"].Migrated)
	require.Contains(t, byKey["bbb/second"].Note, "skill shared is provided by aaa/first; shared-b left in place")
}

func TestMigrationReasonSkillGone(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")

	dir := claudeSkillsDir(f.home)
	versionedLink(t, dir, "alpha", plugin+"/skills/alpha/")
	versionedLink(t, dir, "ghost", plugin+"/skills/ghost/")

	f.sync(t)

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityWarn, "skill ghost points into the plugin cache; plugin acme/tool no longer offers skill ghost"), "issues: %v", issues)
	require.False(t, hasIssue(issues, engine.SeverityWarn, "plugin acme/tool is not parked"), "a missing skill is not a parking problem: %v", issues)

	results, err := f.engine.Heal(t.Context(), false)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, 1, results[0].Migrated)
	require.Contains(t, results[0].Note, "plugin acme/tool no longer offers skill ghost; ghost left in place")
}
