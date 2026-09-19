package fsutil_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
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

func TestWriteFileAtomicChecked(t *testing.T) {
	t.Parallel()

	t.Run("nil check keeps the old behavior", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "config.json")

		require.NoError(t, fsutil.WriteFileAtomicChecked(path, []byte("v1"), 0o600, nil))

		got, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
		require.NoError(t, err)
		require.Equal(t, "v1", string(got))
	})

	t.Run("check passes", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "config.json")

		require.NoError(t, os.WriteFile(path, []byte("original"), 0o600))

		err := fsutil.WriteFileAtomicChecked(path, []byte("replacement"), 0o600, func() error { return nil })
		require.NoError(t, err)

		got, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
		require.NoError(t, err)
		require.Equal(t, "replacement", string(got))
	})

	t.Run("check fails keeps the file", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "config.json")

		require.NoError(t, os.WriteFile(path, []byte("original"), 0o600))

		sentinel := errors.New("changed concurrently")

		err := fsutil.WriteFileAtomicChecked(path, []byte("replacement"), 0o600, func() error { return sentinel })
		require.ErrorIs(t, err, sentinel)

		got, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
		require.NoError(t, err)
		require.Equal(t, "original", string(got))

		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		require.Len(t, entries, 1, "no temp files must remain")
	})

	t.Run("check fails on a new file", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "config.json")

		sentinel := errors.New("appeared concurrently")

		err := fsutil.WriteFileAtomicChecked(path, []byte("v1"), 0o600, func() error { return sentinel })
		require.ErrorIs(t, err, sentinel)

		_, err = os.Stat(path)
		require.ErrorIs(t, err, fs.ErrNotExist)

		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		require.Empty(t, entries, "no temp files must remain")
	})
}

func TestReplaceSymlink(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "current")

	require.NoError(t, fsutil.ReplaceSymlink(path, "/targets/v1"))

	link, err := os.Readlink(path)
	require.NoError(t, err)
	require.Equal(t, "/targets/v1", link)

	require.NoError(t, fsutil.ReplaceSymlink(path, "/targets/v2"))

	link, err = os.Readlink(path)
	require.NoError(t, err)
	require.Equal(t, "/targets/v2", link)

	require.NoError(t, fsutil.ReplaceSymlink(path, "/targets/v2"))

	link, err = os.Readlink(path)
	require.NoError(t, err)
	require.Equal(t, "/targets/v2", link)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temp files must remain")
	require.Equal(t, "current", entries[0].Name())
}

func TestReplaceSymlinkReplacesFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "current")

	require.NoError(t, os.WriteFile(path, []byte("plain file"), 0o600))

	require.NoError(t, fsutil.ReplaceSymlink(path, "/targets/v1"))

	link, err := os.Readlink(path)
	require.NoError(t, err)
	require.Equal(t, "/targets/v1", link)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temp files must remain")
}
