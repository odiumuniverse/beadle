package vault_test

import (
	"os"
	"path/filepath"
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
	require.DirExists(t, filepath.Join(root, "rules", "override"))
	require.DirExists(t, filepath.Join(root, "mcp", "override"))
	require.DirExists(t, filepath.Join(root, "skills"))
	require.DirExists(t, filepath.Join(root, "conflicts"))
	require.DirExists(t, filepath.Join(root, "state"))

	require.NoError(t, v.Init(), "init must be idempotent")
}
