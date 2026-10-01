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

// fileSync is the file barrier, a var so a test can count it. syncDir is the
// directory barrier for the same reason: naming the seam means a new call site
// cannot forget it by calling the underlying implementation directly.
//
// Neither is a behaviour change — the same syscalls, on the same paths, behind
// one indirection.
var fileSync = func(f *os.File) error { return f.Sync() }

// syncDir is the directory barrier. syncDirDefault is the real one; nothing
// calls it directly, so every directory sync in this package is countable.
var syncDir = syncDirDefault

// The call sites this seam does NOT pin, written down rather than asserted. The
// test below proves the mechanism — which writer takes which barrier. It cannot
// prove who calls which writer, and reading the source to assert that would test
// the file instead of the behaviour. So the contract is here, next to the rule it
// qualifies:
//
//	WriteFileAtomic    ← state.json (pkg/state/state.go:692), config.json
//	                     (pkg/config/config.go:423), every host file
//	WriteFileAtomicCAS ← pkg/cas/cas.go, the only caller — content-addressed
//	                     objects, recoverable by hash, repaired by the next Put
//	Store.Sync()       ← pkg/engine/engine.go:223, once per sync, under the lock
//
// If one of the first two migrates to the cheap writer it silently loses
// durability for a file beadle cannot rebuild. If a caller outside pkg/cas starts
// using the cheap writer, the guarantee in its name no longer holds, because
// nothing about it says the data is recoverable. A lint rule forbidding
// WriteFileAtomicCAS outside pkg/cas would enforce the second half; it is worth
// its own task.

func WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	return WriteFileAtomicChecked(path, data, perm, nil)
}

// WriteFileAtomicChecked writes data and returns only after the bytes and the
// rename are durable. It is the discipline for files beadle cannot reconstruct
// — state, config, a secret. Content-addressed objects go through
// WriteFileAtomicCAS instead; see there for why the cost differs so much.
func WriteFileAtomicChecked(path string, data []byte, perm fs.FileMode, check func() error) error {
	return writeTempRename(path, data, perm, check, true)
}

// WriteFileAtomicCAS is WriteFileAtomic for content-addressed data: the same
// write-temp-rename sequence and the same atomicity, without the two durability
// barriers.
//
// It exists because those barriers are the single most expensive thing beadle
// does on darwin. A File.Sync there is F_FULLFSYNC — a request to the drive to
// flush its own write cache — and it costs about 20 ms; on linux the same call
// is a cheap journal barrier, around 28 µs. Measured on this repository
// (pkg/fsutil, 4 KiB objects, three runs each):
//
//	darwin  WriteFileAtomic 18.8–21.5 ms   write+rename only 0.37–0.47 ms
//	linux   WriteFileAtomic 27–30 µs      write+rename only 20–25 µs
//
// For a vault object that is immutable, named by the hash of its contents and
// verified against that hash on every read, a barrier per write buys durability
// of a file nobody needs: a torn object is detected by its hash and rewritten
// by the next sync, while a barrier on the directory once per batch makes the
// rename itself durable. For a file beadle cannot reconstruct — state.json, the
// config, a secret — the barriers stay, and those go through
// WriteFileAtomic.
//
// Callers must batch: pass every directory they wrote in to SyncDirs once at
// the end, or the rename that put the object in place is not itself durable.
func WriteFileAtomicCAS(path string, data []byte, perm fs.FileMode) error {
	return writeTempRename(path, data, perm, nil, false)
}

// SyncDirs flushes the directories a batch of CAS writes landed in. It is the
// single barrier that replaces the per-file ones, so it wants every directory
// the batch touched, not just the last.
func SyncDirs(dirs ...string) error {
	for _, dir := range dirs {
		if err := syncDir(dir); err != nil {
			return fmt.Errorf("sync directory %s: %w", dir, err)
		}
	}

	return nil
}

// writeTempRename is the sequence both writers share: a temp file beside the
// target, the bytes, the mode, an optional guard, and a rename. The rename is
// what makes the write atomic, so it is in both; only the barriers differ.
func writeTempRename(path string, data []byte, perm fs.FileMode, check func() error, durable bool) error {
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

	// Only the durable writer pays this. Skipping it is the whole point of the
	// CAS path: the object is immutable and hash-verified, so a lost write is a
	// missing object, never a wrong one.
	if durable {
		if err = fileSync(tmp); err != nil {
			_ = tmp.Close()

			cleanup()

			return fmt.Errorf("sync temp file: %w", err)
		}
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

	if !durable {
		return nil
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

func syncDirDefault(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: dir is the parent of the file being written, resolved by callers
	if err != nil {
		return err
	}

	defer func() { _ = d.Close() }()

	return d.Sync()
}
