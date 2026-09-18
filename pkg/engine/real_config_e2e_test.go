package engine_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/agent"
	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/engine"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
	"github.com/odiumuniverse/agents-sync/pkg/mcp"
	"github.com/odiumuniverse/agents-sync/pkg/state"
	"github.com/odiumuniverse/agents-sync/pkg/vault"
)

const (
	realSecretOne = "sk-" + "A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6"
	realSecretTwo = "tok_c3D4e5F6g7H8i9J0k1L2m3N4o5P6q7R" //nolint:gosec // G101: synthetic value injected into a temp copy only
	realSecretThr = "s3cret-d4E5f6G7h8I9j0K1l2M3n4"       //nolint:gosec // G101: synthetic value injected into a temp copy only
)

var fixtureSecretPattern = regexp.MustCompile(`(?i)(sk-|ctx7|glpat-|ghp_|xoxb-|AKIA|Bearer[[:space:]]+[A-Za-z0-9]{6,}|BEGIN[A-Z ]*PRIVATE KEY)`)

type realConfigFixture struct {
	home   string
	vault  *vault.Vault
	engine *engine.Engine
}

func realConfigRoot() string {
	return filepath.Join("testdata", "reale2e")
}

func TestRealConfigFixtureIsSanitized(t *testing.T) {
	t.Parallel()

	root := realConfigRoot()

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		require.NoError(t, err)

		rel, err := filepath.Rel(root, path)
		require.NoError(t, err)

		require.NotContains(t, strings.ToLower(entry.Name()), "sk-", "name of %s", rel)

		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			require.NoError(t, err)
			require.False(t, filepath.IsAbs(target), "symlink %s must be relative", rel)
			require.NotContains(t, target, "/Users/")
			require.NotContains(t, target, "/home/")
			require.False(t, fixtureSecretPattern.MatchString(target), "secret-like symlink target in %s", rel)

			cleaned := filepath.Clean(filepath.Join(filepath.Dir(rel), target))
			require.False(t, strings.HasPrefix(cleaned, ".."), "symlink %s escapes the fixture root", rel)
		case entry.IsDir():
			return nil
		default:
			data, err := os.ReadFile(path) //nolint:gosec // G304: the test reads its own fixtures
			require.NoError(t, err)
			require.False(t, fixtureSecretPattern.Match(data), "secret-like value in %s", rel)
			require.NotContains(t, string(data), "/Users/")
			require.NotContains(t, string(data), "/home/")
		}

		return nil
	})
	require.NoError(t, err)
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()

	err := filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		require.NoError(t, err)

		rel, err := filepath.Rel(src, path)
		require.NoError(t, err)

		target := filepath.Join(dst, rel)

		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			require.NoError(t, err)

			return os.Symlink(link, target) //nolint:gosec // G122: the path is built from the test's own fixture tree
		case entry.IsDir():
			return os.MkdirAll(target, 0o750)
		default:
			data, err := os.ReadFile(path) //nolint:gosec // G304: the test copies its own fixture
			require.NoError(t, err)

			return os.WriteFile(target, data, 0o600) //nolint:gosec // G703: the target lives under the test's temp home
		}
	})
	require.NoError(t, err)
}

func injectRealValues(t *testing.T, home string) {
	t.Helper()

	replacements := map[string]string{
		"__AGENTSYNC_SECRET_1__": realSecretOne,
		"__AGENTSYNC_SECRET_2__": realSecretTwo,
		"__AGENTSYNC_SECRET_3__": realSecretThr,
		"__AGENTSYNC_HOME__":     home,
	}

	err := filepath.WalkDir(home, func(path string, entry os.DirEntry, err error) error {
		require.NoError(t, err)

		if !entry.Type().IsRegular() {
			return nil
		}

		data, err := os.ReadFile(path) //nolint:gosec // G304: the test injects into its own temp home
		require.NoError(t, err)

		out := string(data)
		for from, to := range replacements {
			out = strings.ReplaceAll(out, from, to)
		}

		if out == string(data) {
			return nil
		}

		return os.WriteFile(path, []byte(out), 0o600) //nolint:gosec // G703: the path lives under the test's temp home
	})
	require.NoError(t, err)
}

func newRealConfigFixture(t *testing.T) (*realConfigFixture, *engine.Report) {
	t.Helper()

	home := t.TempDir()
	cwd := t.TempDir()
	v := vault.New(filepath.Join(t.TempDir(), "vault"))

	require.NoError(t, v.Init())

	copyTree(t, filepath.Join(realConfigRoot(), "home"), home)
	injectRealValues(t, home)

	cfg, err := config.Load(v.ConfigPath())
	require.NoError(t, err)

	for _, id := range []string{agent.ClaudeCodeID, agent.OpenCodeID, agent.GeminiCLIID, agent.CursorID} {
		cfg.Enable(id)
	}

	require.NoError(t, cfg.Save(v.ConfigPath()))

	e, err := engine.New(v, cfg, agent.All(home, cwd), engine.WithHome(home))
	require.NoError(t, err)

	report, err := e.Sync(t.Context(), engine.SyncOptions{})
	require.NoError(t, err)
	require.Empty(t, report.Errors())

	return &realConfigFixture{home: home, vault: v, engine: e}, report
}

func TestRealConfigE2E(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f, report := newRealConfigFixture(t)

	assertRealConfigSkills(t, f, report)
	assertRealConfigMCP(t, f, report)
	assertRealConfigSecrets(t, f)
	assertRealConfigAlias(t, f, report)

	homeBefore := hashTree(t, f.home)
	canonBefore := canonHash(t, f.vault)

	second, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
	require.NoError(t, err)
	require.Empty(t, second.Errors())
	assertSecondSyncNoop(t, second)

	require.Equal(t, homeBefore, hashTree(t, f.home), "the second sync must not touch the home tree")
	require.Equal(t, canonBefore, canonHash(t, f.vault), "the second sync must not touch the canon")
}

func assertRealConfigSkills(t *testing.T, f *realConfigFixture, report *engine.Report) {
	t.Helper()

	require.True(t, report.Kind(kind.Skills).VaultChanged)

	entries, err := os.ReadDir(f.vault.SkillsDir())
	require.NoError(t, err)
	require.Len(t, entries, 45)

	for _, entry := range entries {
		require.NotContains(t, entry.Name(), "plug-", "plugin cache skills stay invisible")
	}

	pivot := filepath.Join(f.vault.PluginsDir(), "vmkteam", "vmkteam-developer", "current", "skills", "plug-1")
	require.Equal(t, pivot, farmLink(t, claudeSkillsDir(f.home), "plug-1"))
}

func assertRealConfigMCP(t *testing.T, f *realConfigFixture, report *engine.Report) {
	t.Helper()

	servers, err := mcp.ParseCanonical([]byte(read(t, f.vault.ServersPath())))
	require.NoError(t, err)
	require.Len(t, servers, 7)

	require.Len(t, report.Conflicts, 3)

	for _, conflict := range report.Conflicts {
		require.Equal(t, state.ReasonAdded, conflict.Reason)
	}

	assertConflict(t, report, kind.Rules, agent.GeminiCLIID, "main")
	assertConflict(t, report, kind.MCP, agent.OpenCodeID, "codegraph")
	assertConflict(t, report, kind.MCP, agent.GeminiCLIID, "context7")

	files, err := os.ReadDir(f.vault.ConflictsDir())
	require.NoError(t, err)
	require.Len(t, files, 3)
}

func assertConflict(t *testing.T, report *engine.Report, k kind.ID, agentID, key string) {
	t.Helper()

	for _, conflict := range report.ConflictsOf(k) {
		if conflict.Agent == agentID {
			require.Equal(t, key, conflict.Key)

			return
		}
	}

	require.FailNow(t, "no conflict for "+string(k)+"/"+agentID)
}

func assertRealConfigSecrets(t *testing.T, f *realConfigFixture) {
	t.Helper()

	info, err := os.Stat(f.vault.SecretsPath())
	require.NoError(t, err)
	require.Equal(t, fs.FileMode(0o600), info.Mode().Perm())

	data := read(t, f.vault.SecretsPath())

	for _, value := range []string{realSecretOne, realSecretTwo, realSecretThr} {
		require.Contains(t, data, value)
	}

	for _, path := range []string{f.vault.RulesPath(), f.vault.SkillsDir(), f.vault.ServersPath(), f.vault.ObjectsDir()} {
		assertNoLiteral(t, path, realSecretOne, realSecretTwo, realSecretThr)
	}
}

func assertNoLiteral(t *testing.T, root string, values ...string) {
	t.Helper()

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		require.NoError(t, err)

		if !entry.Type().IsRegular() {
			return nil
		}

		data, err := os.ReadFile(path) //nolint:gosec // G304: the test reads its own vault
		require.NoError(t, err)

		for _, value := range values {
			require.NotContains(t, string(data), value, "%s leaks a secret literal", path)
		}

		return nil
	})
	require.NoError(t, err)
}

func assertRealConfigAlias(t *testing.T, f *realConfigFixture, report *engine.Report) {
	t.Helper()

	link := filepath.Join(f.home, ".config", "opencode", "AGENTS.md")

	target, err := os.Readlink(link)
	require.NoError(t, err)
	require.Equal(t, "../../.claude/CLAUDE.md", target, "the alias symlink is never replaced")
	require.Equal(t, engine.ActionAlias, report.Action(kind.Rules, agent.OpenCodeID))

	require.Equal(t, read(t, filepath.Join(f.home, ".claude", "CLAUDE.md")), read(t, f.vault.RulesPath()))
	require.NotContains(t, read(t, f.vault.RulesPath()), "gemini rules")

	require.Contains(t, read(t, filepath.Join(f.home, ".config", "opencode", "opencode.jsonc")), `"enabled": false`, "an unmanaged entry survives")
}

func canonHash(t *testing.T, v *vault.Vault) string {
	t.Helper()

	var out strings.Builder

	for _, path := range []string{v.RulesPath(), v.SkillsDir(), v.ServersPath(), v.SecretsPath(), filepath.Dir(v.PermissionsPath())} {
		out.WriteString(hashTree(t, path))
	}

	return out.String()
}

func assertSecondSyncNoop(t *testing.T, report *engine.Report) {
	t.Helper()

	require.False(t, report.VaultChanged())

	for _, kr := range report.Kinds {
		require.Empty(t, kr.Pulled, "%s pulled changes on a repeated sync", kr.Kind)

		for _, result := range kr.Agents {
			require.NotEqual(t, engine.ActionPushed, result.Action, "%s/%s", kr.Kind, result.Agent)
		}
	}
}
