package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func stubSymlinkLinker(t *testing.T, link func(oldname, newname string) error) {
	t.Helper()

	previous := symlinkLinker
	symlinkLinker = link

	t.Cleanup(func() { symlinkLinker = previous })
}

func TestUnsupportedSymlinkFS(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		"exfat": true,
		"EXFAT": true,
		"msdos": true,
		"vfat":  true,
		"smb":   false,
		"cifs":  false,
		"apfs":  false,
		"ext4":  false,
	}

	for fsType, want := range tests {
		t.Run(fsType, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, want, UnsupportedSymlinkFS(fsType))
		})
	}
}

func TestClassifySymlinkErr(t *testing.T) {
	t.Parallel()

	unsupported := map[string]error{
		"EPERM":      syscall.EPERM,
		"EOPNOTSUPP": syscall.EOPNOTSUPP,
		"ENOTSUP":    syscall.ENOTSUP,
		"ENOSYS":     syscall.ENOSYS,
	}

	for name, err := range unsupported {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			wrapped := fmt.Errorf("symlink: %w", &os.PathError{Op: "symlink", Path: "x", Err: err})
			require.ErrorIs(t, classifySymlinkErr(wrapped), ErrSymlinksUnsupported)
		})
	}

	damage := map[string]error{
		"EACCES":  syscall.EACCES,
		"EEXIST":  syscall.EEXIST,
		"ENOENT":  syscall.ENOENT,
		"ENOTDIR": syscall.ENOTDIR,
	}

	for name, err := range damage {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			classified := classifySymlinkErr(&os.PathError{Op: "symlink", Path: "x", Err: err})
			require.NotErrorIs(t, classified, ErrSymlinksUnsupported)
			require.ErrorIs(t, classified, err)
		})
	}
}

//nolint:paralleltest // the test swaps the package-level linker seam
func TestReplaceSymlinkClassifiesUnsupported(t *testing.T) {
	dir := t.TempDir()

	for _, denied := range []error{syscall.EPERM, syscall.EOPNOTSUPP, syscall.ENOTSUP, syscall.ENOSYS} {
		stubSymlinkLinker(t, func(_, _ string) error { return denied })

		require.ErrorIs(t, ReplaceSymlink(filepath.Join(dir, "alpha"), "target"), ErrSymlinksUnsupported)
	}

	stubSymlinkLinker(t, func(_, _ string) error { return syscall.EACCES })

	err := ReplaceSymlink(filepath.Join(dir, "alpha"), "target")
	require.NotErrorIs(t, err, ErrSymlinksUnsupported)
	require.ErrorIs(t, err, syscall.EACCES)

	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	require.Empty(t, entries, "a failed link must leave no temp artifacts")
}

func TestReplaceSymlinkReal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "alpha")

	require.NoError(t, ReplaceSymlink(path, "target"))

	link, err := os.Readlink(path)
	require.NoError(t, err)
	require.Equal(t, "target", link)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "the temp link must not survive")
}
