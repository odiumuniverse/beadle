package vault_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/vault"
)

func TestResolveRoot(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	require.NoError(t, err)

	tests := map[string]struct {
		flag string
		env  string
		want string
	}{
		"flag wins":             {flag: "/tmp/explicit", env: "/tmp/from-env", want: "/tmp/explicit"},
		"env wins over default": {env: "/tmp/from-env", want: "/tmp/from-env"},
		"default is home based": {want: filepath.Join(home, vault.DefaultDirName)},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := vault.ResolveRoot(tt.flag, tt.env)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestInit(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "vault")
	v := vault.New(root)

	require.False(t, v.Initialized())
	require.NoError(t, v.Init())
	require.True(t, v.Initialized())

	require.FileExists(t, v.ConfigPath())
	require.DirExists(t, v.ObjectsDir())
	require.DirExists(t, v.SkillsDir())
	require.DirExists(t, v.MemoryDir())
	require.DirExists(t, v.ConflictsDir())
	require.DirExists(t, filepath.Join(root, "rules"))
	require.DirExists(t, filepath.Join(root, "mcp"))
	require.DirExists(t, filepath.Join(root, "state"))

	info, err := os.Stat(root)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "the vault holds credentials: owner only")

	ignore, err := os.ReadFile(filepath.Join(root, ".gitignore")) //nolint:gosec // G304: test reads its own temp file
	require.NoError(t, err)
	require.Contains(t, string(ignore), "mcp/secrets.json")
	require.Contains(t, string(ignore), "objects/")
	require.Contains(t, string(ignore), "memory/")
	require.Contains(t, string(ignore), "until the U-12 secret gate lands")

	require.NoError(t, v.Init(), "init must be idempotent")

	require.NoError(t, v.EnsureGitIgnore(), "gitignore must be idempotent")
	require.Equal(t, string(ignore), readIgnore(t, root), "a second pass must not duplicate lines")
}

func readIgnore(t *testing.T, root string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(root, ".gitignore")) //nolint:gosec // G304: test reads its own temp file
	require.NoError(t, err)

	return string(data)
}

func TestEnsureGitIgnoreAddsMemoryToOldVault(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "vault")
	require.NoError(t, os.MkdirAll(root, 0o700))

	old := "# AgentSync: credentials and machine-local state stay on this machine.\nmcp/secrets.json\nstate/\nstate.json\nobjects/\nconflicts/\nplugins/\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(old), 0o600))

	require.NoError(t, vault.New(root).EnsureGitIgnore())

	updated := readIgnore(t, root)
	require.Contains(t, updated, "memory/\n")
	require.Equal(t, 1, strings.Count(updated, "plugins/"), "existing lines stay put")
}
