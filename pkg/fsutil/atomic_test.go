package fsutil_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
)

func TestWriteFileAtomic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	require.NoError(t, fsutil.WriteFileAtomic(path, []byte("v1"), 0o600))

	got, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
	require.NoError(t, err)
	require.Equal(t, "v1", string(got))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	require.NoError(t, fsutil.WriteFileAtomic(path, []byte("v2"), 0o644))

	got, err = os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
	require.NoError(t, err)
	require.Equal(t, "v2", string(got))

	info, err = os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm())

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temp files must remain")
}
