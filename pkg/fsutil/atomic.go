package fsutil

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ErrSymlinksUnsupported reports that symlinks cannot be created in a directory.
var ErrSymlinksUnsupported = errors.New("symlinks are not supported here")

var symlinkLinker = os.Symlink

func WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	return WriteFileAtomicChecked(path, data, perm, nil)
}

func WriteFileAtomicChecked(path string, data []byte, perm fs.FileMode, check func() error) error {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	tmpName := tmp.Name()

	cleanup := func() {
		_ = os.Remove(tmpName)
	}

	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()

		cleanup()

		return fmt.Errorf("write temp file: %w", err)
	}

	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()

		cleanup()

		return fmt.Errorf("sync temp file: %w", err)
	}

	if err = tmp.Close(); err != nil {
		cleanup()

		return fmt.Errorf("close temp file: %w", err)
	}

	if err = os.Chmod(tmpName, perm); err != nil {
		cleanup()

		return fmt.Errorf("chmod temp file: %w", err)
	}

	if check != nil {
		if err = check(); err != nil {
			cleanup()

			return err
		}
	}

	if err = os.Rename(tmpName, path); err != nil {
		cleanup()

		return fmt.Errorf("rename temp file: %w", err)
	}

	if err = syncDir(dir); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}

	return nil
}

func ReplaceSymlink(path, target string) error {
	dir := filepath.Dir(path)

	tmpName, err := symlinkTempName(dir, filepath.Base(path))
	if err != nil {
		return err
	}

	cleanup := func() {
		_ = os.Remove(tmpName)
	}

	if err = symlinkLinker(target, tmpName); err != nil {
		return fmt.Errorf("create temp symlink: %w", classifySymlinkErr(err))
	}

	if err = os.Rename(tmpName, path); err != nil {
		cleanup()

		return fmt.Errorf("rename temp symlink: %w", err)
	}

	if err = syncDir(dir); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}

	return nil
}

func symlinkTempName(dir, base string) (string, error) {
	var suffix [4]byte

	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("generate temp name: %w", err)
	}

	return filepath.Join(dir, "."+base+".tmp-"+hex.EncodeToString(suffix[:])), nil
}

// UnsupportedSymlinkFS reports whether a filesystem type definitively cannot
// hold symlinks; network filesystems are excluded — the farm link decides.
func UnsupportedSymlinkFS(fsType string) bool {
	switch strings.ToLower(fsType) {
	case "exfat", "msdos", "vfat":
		return true
	default:
		return false
	}
}

func classifySymlinkErr(err error) error {
	for _, denied := range []error{syscall.EPERM, syscall.EOPNOTSUPP, syscall.ENOTSUP, syscall.ENOSYS} {
		if errors.Is(err, denied) {
			return fmt.Errorf("%w: %w", ErrSymlinksUnsupported, err)
		}
	}

	return err
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: dir is the parent of the file being written, resolved by callers
	if err != nil {
		return err
	}

	defer func() { _ = d.Close() }()

	return d.Sync()
}
