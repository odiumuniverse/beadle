package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// RenamePath moves a file or a whole directory tree to a new name and makes the
// move durable: the rename alone lives in the kernel until the parent
// directory is flushed, so a crash right after it can resurrect the old name
// and lose the new one. Every parent the move touched is therefore fsynced
// before the call reports success, which is the directory half of the
// write-temp-fsync-rename-fsync-dir discipline WriteFileAtomic applies to
// files.
//
// A migration uses this so a run interrupted between two renames is finished by
// the next one: whatever reached the disk is already the final name, and a
// target that still holds the old name is simply renamed again.
func RenamePath(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return fmt.Errorf("rename %s to %s: %w", from, to, err)
	}

	source, target := filepath.Dir(from), filepath.Dir(to)

	if err := syncDir(source); err != nil {
		return fmt.Errorf("sync directory %s: %w", source, err)
	}

	if target == source {
		return nil
	}

	if err := syncDir(target); err != nil {
		return fmt.Errorf("sync directory %s: %w", target, err)
	}

	return nil
}
