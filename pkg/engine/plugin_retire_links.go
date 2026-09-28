package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// pluginLinkKinds lists the surfaces beadle farms plugin artifacts into.
var pluginLinkKinds = []kind.ID{kind.Skills, kind.Subagents, kind.Commands}

// repairPluginLinks removes the host symlinks beadle farmed into a plugin
// pivot that no longer exists. Retiring a plugin deletes its pivot (symlinks
// only) and the links that pointed into it — in the pull-mode hosts of the
// skills surfaces and in the shared agents home as much as in the farmed
// ones — would dangle forever otherwise: the farm plan only walks plugins
// that are still parked, so nothing else revisits them.
//
// The sweep is state-based, not transition-based: it repairs whatever is
// broken, including links left behind by an earlier retire, so a later sync
// or heal converges. Only symlinks under the vault's own plugin tree are
// touched, and only when their target is gone; every other entry, including
// foreign files and live links, is left alone.
func (e *Engine) repairPluginLinks(active []*agent.Agent) (int, []string) {
	root := e.vault.PluginsDir()

	if root == "" {
		return 0, nil
	}

	// Only a plugin the ledger retired (or one it never knew: an orphan pivot
	// heal already removed) leaves links to clean. A live parked plugin keeps
	// its links even when the pivot is not on disk at this instant.
	live := e.livePluginKeys()

	var (
		removed int
		warns   []string
		seen    = map[string]bool{}
	)

	for _, a := range active {
		for _, k := range pluginLinkKinds {
			surface := a.Surface(k)
			if surface == nil {
				continue
			}

			for _, dir := range linkScanDirs(surface) {
				if dir == "" || seen[dir] {
					continue
				}

				seen[dir] = true

				count, dirWarns := removeDanglingPluginLinks(dir, root, live)
				removed += count

				warns = append(warns, dirWarns...)
			}
		}
	}

	return removed, warns
}

// linkScanDirs lists the directories one surface can hold farmed links in:
// the directory beadle writes, plus the read areas a skills surface shadows
// (a link beadle farmed there dangles the same way).
func linkScanDirs(surface agent.Surface) []string {
	dirs := []string{surface.Path()}

	if area, ok := surface.(agent.SkillReadArea); ok {
		dirs = append(dirs, area.ReadDirs()...)
	}

	return dirs
}

// removeDanglingPluginLinks drops the symlinks under dir whose target is
// missing and lies inside pivotRoot. It returns how many it removed; a
// missing dir is not an error.
func removeDanglingPluginLinks(dir, pivotRoot string, live map[string]bool) (int, []string) {
	var (
		removed int
		warns   []string
	)

	walkErr := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}

			warns = append(warns, fmt.Sprintf("plugins: cannot scan %s: %v", path, err))

			return nil
		}

		if path == dir || entry.IsDir() || entry.Type()&fs.ModeSymlink == 0 {
			return nil
		}

		drop, warn := danglingPivotLink(path, pivotRoot, live)
		if warn != "" {
			warns = append(warns, warn)
		}

		if !drop {
			return nil
		}

		//nolint:gosec // G122: path comes from WalkDir over a host directory beadle owns and was just verified to be a dangling symlink into the vault plugin tree
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			warns = append(warns, fmt.Sprintf("plugins: cannot remove the dangling link %s: %v", path, err))

			return nil
		}

		removed++

		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.ErrNotExist) {
		warns = append(warns, fmt.Sprintf("plugins: cannot scan %s: %v", dir, walkErr))
	}

	return removed, warns
}

// livePluginKeys lists the ledger keys beadle still presents. A link into a
// plugin that is missing from the set was retired (or never recorded at all,
// which is what an orphan pivot heal already removed looks like).
func (e *Engine) livePluginKeys() map[string]bool {
	live := map[string]bool{}

	ledger, _, err := loadPluginLedger(e.vault.PluginsLedgerPath())
	if err != nil {
		return live
	}

	for key, rec := range ledger.Plugins {
		if rec.RetiredAt.IsZero() {
			live[key] = true
		}
	}

	return live
}

// pluginKeyAt splits the plugin key a link target under pivotRoot belongs to:
// <pivotRoot>/<marketplace>/<name>/...
func pluginKeyAt(target, pivotRoot string) (string, bool) {
	rel, err := filepath.Rel(pivotRoot, target)
	if err != nil {
		return "", false
	}

	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}

	return parts[0] + "/" + parts[1], true
}

// danglingPivotLink reports whether the symlink at path points into pivotRoot
// at a target that is gone, and returns the diagnostic when the entry cannot
// be read at all.
func danglingPivotLink(path, pivotRoot string, live map[string]bool) (bool, string) {
	target, err := os.Readlink(path)
	if err != nil {
		return false, fmt.Sprintf("plugins: cannot read the link %s: %v", path, err)
	}

	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}

	target = filepath.Clean(target)
	if !underDir(target, pivotRoot) {
		return false, ""
	}

	if key, ok := pluginKeyAt(target, pivotRoot); ok && live[key] {
		return false, ""
	}

	// A live link into a plugin pivot is the farm's own work: the plan
	// removes it when the plugin goes.
	switch _, err := os.Stat(target); {
	case err == nil:
		return false, ""
	case errors.Is(err, fs.ErrNotExist):
		return true, ""
	default:
		return false, fmt.Sprintf("plugins: cannot stat %s: %v", target, err)
	}
}
