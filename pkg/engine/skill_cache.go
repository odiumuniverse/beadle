package engine

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"time"

	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/skill"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// skillTreeRacyWindow is the safety margin between a file's modification time
// and the moment its tree digest was computed. A tree whose newest file is
// newer than this window is read again: a filesystem with a coarse clock can
// hide a same-size content change written in the same second, so the digest
// is not trusted while that ambiguity is possible (git's racy-index rule).
const skillTreeRacyWindow = 2 * time.Second

// skillTreeDigest returns the digest of one skill tree, reusing a cached
// digest while the tree listing is unchanged and not racy. The in-process
// cache serves repeated scans of one run; the state cache serves the next
// process, and only the passes that save the state persist it. A miss reads
// the tree and refreshes both; the digest is always the one TreeDigest
// computes for the tree ReadTree reads.
func (e *Engine) skillTreeDigest(st *state.State, root string) (cas.Hash, error) {
	if e.skillCacheOff {
		return readSkillTreeDigest(root)
	}

	stat, err := skill.StatTree(root)
	if err != nil {
		// Fall back to the tree read so the caller reports exactly what the
		// uncached scan would report.
		return readSkillTreeDigest(root)
	}

	if digest, ok := e.cachedSkillTree(st, root, stat); ok {
		return digest, nil
	}

	digest, err := readSkillTreeDigest(root)
	if err != nil {
		return "", err
	}

	// The stamp records when the digest was computed, so it moves only when
	// the digest does. Re-stamping an unchanged digest on every run made
	// state.json a function of the clock rather than of the content: a sync
	// that re-rendered the farm refreshed the mtimes, the cache missed, the
	// stamp moved, and a second sync produced a different state file. A script
	// that hashes the vault to decide whether anything happened cannot tell
	// that from work, and `migrate.idempotent` fails on it.
	stamp := e.now()
	key := state.SkillTreeKey(root, e.home)
	if previous, ok := st.SkillTreeFor(key); ok && previous.Digest == digest {
		stamp = previous.Stamp
	}

	e.setSkillTree(st, root, key, state.SkillTree{
		Digest:      digest,
		Fingerprint: stat.Fingerprint,
		Latest:      stat.Latest,
		Stamp:       stamp,
	})

	return digest, nil
}

// cachedSkillTree returns a cached digest that still describes the listing:
// the in-process entry first, the persisted state entry second.
func (e *Engine) cachedSkillTree(st *state.State, root string, stat skill.TreeStat) (cas.Hash, bool) {
	if entry, ok := e.skillCache[root]; ok && skillTreeFresh(entry, stat) {
		return entry.Digest, true
	}

	// The in-process cache is keyed by the real path, because it never leaves
	// this machine. The state is keyed by the portable one, because it does.
	if entry, ok := st.SkillTreeFor(state.SkillTreeKey(root, e.home)); ok && skillTreeFresh(entry, stat) {
		e.setSkillCache(root, entry)

		return entry.Digest, true
	}

	return "", false
}

// skillTreeFresh reports whether a cache entry still describes the listing
// and lies outside the racy window. An entry without a digest never counts:
// a hand-edited state must not hand the scan an empty digest.
func skillTreeFresh(entry state.SkillTree, stat skill.TreeStat) bool {
	return entry.Digest != "" &&
		entry.Fingerprint == stat.Fingerprint &&
		!entry.Stamp.IsZero() &&
		stat.Latest.Before(entry.Stamp.Add(-skillTreeRacyWindow))
}

// setSkillTree records one digest in the in-process cache and in the state the
// caller may save. root is this machine's path and key is the portable name the
// state stores it under.
func (e *Engine) setSkillTree(st *state.State, root, key string, entry state.SkillTree) {
	e.setSkillCache(root, entry)
	st.SetSkillTree(key, entry)
}

// setSkillCache records one digest in the in-process cache.
func (e *Engine) setSkillCache(root string, entry state.SkillTree) {
	if e.skillCache == nil {
		e.skillCache = map[string]state.SkillTree{}
	}

	e.skillCache[root] = entry
}

// readSkillTreeDigest reads and hashes one skill tree without the cache.
func readSkillTreeDigest(root string) (cas.Hash, error) {
	tree, err := skill.ReadTree(root)
	if err != nil {
		return "", err
	}

	return skill.TreeDigest(tree), nil
}

// pruneSkillTrees drops cache entries whose root no longer exists, so a
// vanished skill tree does not linger in the state. It runs on the sync pass
// that saves the state; read-only commands never persist the result.
func (e *Engine) pruneSkillTrees(st *state.State) {
	drop := func(root string) bool {
		_, err := os.Lstat(root)

		return errors.Is(err, fs.ErrNotExist)
	}

	st.DropSkillTrees(drop)
	maps.DeleteFunc(e.skillCache, func(root string, _ state.SkillTree) bool { return drop(root) })
}
