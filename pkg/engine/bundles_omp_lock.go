package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/odiumuniverse/beadle/pkg/lock"
	"github.com/odiumuniverse/beadle/pkg/plugin"
)

const (
	// ompPluginLockFile is the advisory lock file that serializes writers of
	// omp's plugin manager. omp's installer holds no cross-process lock of its
	// own ("No cross-process locking or merge strategy exists; concurrent
	// writers can overwrite each other" — omp://plugin-manager-installer-
	// plumbing.md), so verger ships this lock and beadle adopts the very same
	// file: two tools, one mutex, no protocol change.
	//
	// The name reads "verger" because verger shipped first; renaming it would
	// mean a lock migration for no functional gain.
	ompPluginLockFile = ".omp-plugin.verger.lock"
)

// ompPluginLockWait bounds one wait for the shared lock: another writer may
// hold it for the length of an install, and a sync must not hang on it. flock
// is released by the kernel when its holder dies, so a crashed writer cannot
// leave the lock behind.
var ompPluginLockWait = 30 * time.Second

// lockOmpPlugin takes the shared omp plugin lock and returns its release. The
// second result is false when another writer holds the lock or it cannot be
// taken at all: the caller then keeps the bundle on the manual path (file
// copies plus the printed commands) instead of mutating omp's state.
//
// The lock sits next to the plugin state that is actually mutated, so the
// default (no profile) case is byte-identical to verger's file. With a named
// profile omp moves plugins/ to profiles/<name>/plugins and verger still
// locks the base root: beadle locks the profile state root it writes, and
// verger should adopt the same base (see agent.OmpStateRoot).
func (e *Engine) lockOmpPlugin(report *Report) (func(), bool) {
	ctx, cancel := context.WithTimeout(context.Background(), ompPluginLockWait)

	release, err := lock.Acquire(ctx, filepath.Join(plugin.OmpRoot(e.home), ompPluginLockFile))
	if err != nil {
		cancel()

		report.Warnings = append(report.Warnings,
			fmt.Sprintf("bundles: %v; the omp bundle stays on the manual path", err))

		return func() { cancel() }, false
	}

	return func() {
		_ = release()

		cancel()
	}, true
}
