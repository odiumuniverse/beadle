package engine_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func presentationFixture(t *testing.T, hosts ...string) *fixture {
	t.Helper()

	f := newFixture(t)
	f.emptyConfigs(t)

	for _, id := range hosts {
		f.config.Enable(id)
	}

	if slices.Contains(hosts, agent.GeminiCLIID) {
		write(t, f.geminiSettings(), `{"mcpServers": {}}`)
	}

	v2 := pluginTree(t, f.home, "acme", "tool", "2.0.0")
	writeSkill(t, v2, "alpha", "# alpha in the cache\n")

	v1 := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, v1, "plugged", "# plugged\n")

	f.sync(t)

	return f
}

func cacheSkillPath(home, version, skillName string) string {
	return filepath.Join(claudePluginsDir(home), "cache", "acme", "tool", version, "skills", skillName)
}

func pivotSkillPath(f *fixture, skillName string) string {
	return filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current", "skills", skillName)
}

func TestPluginPresentationDirectCacheLinkIsInvisible(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := presentationFixture(t)

	claudeLink := filepath.Join(claudeSkillsDir(f.home), "plugged")
	require.NoError(t, os.Remove(claudeLink))
	versionedLink(t, claudeSkillsDir(f.home), "plugged", cacheSkillPath(f.home, "1.0.0", "plugged"))

	report := f.sync(t)

	require.False(t, report.Kind(kind.Skills).VaultChanged)

	for _, change := range report.Kind(kind.Skills).Pulled {
		require.NotContains(t, change.Key, "plugged")
	}

	entries, err := os.ReadDir(f.vault.SkillsDir())
	require.NoError(t, err)
	require.Empty(t, entries, "a cache link never reaches the canon")

	link, err := os.Readlink(claudeLink)
	require.NoError(t, err)
	require.Equal(t, cacheSkillPath(f.home, "1.0.0", "plugged"), link, "the foreign link is never re-pointed")

	require.Equal(t, pivotSkillPath(f, "plugged"), farmLink(t, openCodeSkillsDir(f.home), "plugged"))

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.False(t, hasIssue(issues, engine.SeverityError, "plugged"), "issues: %v", issues)
}

func TestPluginPresentationExternalChainIsInvisible(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := presentationFixture(t)

	dock := filepath.Join(f.home, "dock", "alpha")
	require.NoError(t, os.MkdirAll(filepath.Dir(dock), 0o750))
	require.NoError(t, os.Symlink(cacheSkillPath(f.home, "2.0.0", "alpha"), dock))
	versionedLink(t, claudeSkillsDir(f.home), "alpha", dock)

	report := f.sync(t)

	require.False(t, report.Kind(kind.Skills).VaultChanged)
	require.Empty(t, report.Kind(kind.Skills).Pulled)
	require.Equal(t, engine.ActionNoop, report.Action(kind.Skills, agent.ClaudeCodeID))

	entries, err := os.ReadDir(f.vault.SkillsDir())
	require.NoError(t, err)
	require.Empty(t, entries, "a chain that ends in the plugin cache never reaches the canon")
}

func TestPluginPresentationDockRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := presentationFixture(t, agent.GeminiCLIID, agent.SharedID)

	dock := filepath.Join(sharedSkillsDir(f.home), "alpha")
	write(t, filepath.Join(dock, "SKILL.md"), "# alpha v1\n")
	versionedLink(t, claudeSkillsDir(f.home), "alpha", dock)

	report := f.sync(t)
	require.True(t, report.Kind(kind.Skills).VaultChanged)
	require.Equal(t, "# alpha v1\n", read(t, f.vaultSkill("alpha")))
	requireGeminiCopy(t, f, "# alpha v1\n")

	write(t, filepath.Join(claudeSkillsDir(f.home), "alpha", "SKILL.md"), "# alpha v2\n")

	f.sync(t)
	require.Equal(t, "# alpha v2\n", read(t, f.vaultSkill("alpha")), "an edit through the dock link is pulled")
	requireGeminiCopy(t, f, "# alpha v2\n")

	require.NoError(t, os.RemoveAll(dock))

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityError, "broken symlink: "+filepath.Join(claudeSkillsDir(f.home), "alpha")), "issues: %v", issues)
	require.Equal(t, "# alpha v2\n", read(t, f.vaultSkill("alpha")), "the canon stays intact")
	requireGeminiCopy(t, f, "# alpha v2\n")
}

func requireGeminiCopy(t *testing.T, f *fixture, content string) {
	t.Helper()

	path := filepath.Join(f.home, ".gemini", "skills", "alpha", "SKILL.md")

	info, err := os.Lstat(path)
	require.NoError(t, err)
	require.Zero(t, info.Mode()&fs.ModeSymlink, "Gemini holds a real copy, not a symlink")
	require.Equal(t, content, read(t, path))
}
