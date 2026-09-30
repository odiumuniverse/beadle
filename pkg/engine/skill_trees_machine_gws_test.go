package engine_test

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/state"
)

// enableCursor gives one machine a Cursor host that writes skills, which is
// what makes a skill tree get scanned and the cache written at all.
func enableCursor(t *testing.T, f *fixture) {
	t.Helper()

	f.emptyConfigs(t)
	cursorWritesSkills(t, f)
}

// treeKeys is the cache's key set, sorted. It is the thing that must not
// depend on which machine wrote it.
func treeKeys(st *state.State) []string {
	return slices.Sorted(maps.Keys(st.SkillTrees))
}

// stateJSON is the state document as bytes, which is the only honest way to ask
// what was persisted: reading it back through state.Load would migrate it in
// memory and hide exactly what this test is about.
func stateJSON(t *testing.T, f *fixture) []byte {
	t.Helper()

	data, err := os.ReadFile(f.vault.StatePath()) //nolint:gosec // G304: the test reads its own fixture
	if err != nil {
		t.Fatalf("read state: %v", err)
	}

	return data
}

// copyTreeWithTimes copies a directory with its modification times intact, and
// copies a symlink AS a symlink. It is not the copyTree the other engine tests
// use, which stamps every file with the current time: the cache's fingerprint
// is built from the listing including modification times, so a copy that
// dropped them would read as every file having changed, and the test would be
// measuring the copy rather than the keys.
//
// The symlink case is not a corner. A vault travels with whatever it holds, and
// an adopted skill's stash is a symlink into whichever home adopted it: reading
// through it would either fail on a link to a directory or, worse, silently
// copy the target's bytes and hand the next machine a regular file where the
// user's own link was.
func copyTreeWithTimes(t *testing.T, src, dst string) {
	t.Helper()

	info, err := os.Stat(src)
	if err != nil {
		t.Fatalf("stat %s: %v", src, err)
	}

	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
		t.Fatalf("mkdir %s: %v", dst, err)
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}

	for _, entry := range entries {
		from := filepath.Join(src, entry.Name())
		to := filepath.Join(dst, entry.Name())

		if entry.Type()&fs.ModeSymlink != 0 {
			link, readErr := os.Readlink(from)
			if readErr != nil {
				t.Fatalf("readlink %s: %v", from, readErr)
			}

			if linkErr := os.Symlink(link, to); linkErr != nil {
				t.Fatalf("symlink %s: %v", to, linkErr)
			}

			continue
		}

		if entry.IsDir() {
			copyTreeWithTimes(t, from, to)

			continue
		}

		fileInfo, statErr := entry.Info()
		if statErr != nil {
			t.Fatalf("stat %s: %v", from, statErr)
		}

		data, readErr := os.ReadFile(from) //nolint:gosec // G304: the test copies its own fixture
		if readErr != nil {
			t.Fatalf("read %s: %v", from, readErr)
		}

		//nolint:gosec // G703: the target is built from the test's own fixture tree
		if writeErr := os.WriteFile(to, data, fileInfo.Mode().Perm()); writeErr != nil {
			t.Fatalf("write %s: %v", to, writeErr)
		}

		if timeErr := os.Chtimes(to, fileInfo.ModTime(), fileInfo.ModTime()); timeErr != nil {
			t.Fatalf("chtimes %s: %v", to, timeErr)
		}
	}
}

// TestSkillTreeKeysSurviveTheMachineThatWroteThem is the vault that travels.
//
// beadle keeps a digest cache of every skill tree it has scanned, keyed by the
// tree's path. Keyed by an absolute path, the cache is a list of one machine's
// home directory: sync the vault to a Linux box and every key names a path
// that matches nothing there, so the whole cache misses, a fresh one is written
// beside it, and a vault two machines both write accumulates a second machine's
// home directory on every pass.
func TestSkillTreeKeysSurviveTheMachineThatWroteThem(t *testing.T) {
	Convey("Given a vault synced on a machine whose home is /Users/a", t, func() {
		homeA := filepath.Join(t.TempDir(), "Users", "a")

		a := newFixtureAt(t, homeA, filepath.Join(homeA, ".beagle"))
		enableCursor(t, a)
		write(t, filepath.Join(a.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha\n")
		a.sync(t)

		// A second pass, with the delivered copy aged out of the cache's racy
		// window first: one sync leaves a vault that has never been settled, and
		// the machine-to-machine question is about a vault both machines have
		// already written to more than once.
		ageFiles(t, filepath.Join(homeA, ".cursor", "skills", "alpha"))
		a.sync(t)
		keysA := treeKeys(loadState(t, a))
		So(keysA, ShouldNotBeEmpty)

		Convey("Then the keys name the tree, not the machine it was scanned on", func() {
			for _, key := range keysA {
				So(key, ShouldStartWith, state.HomePrefix)
			}

			Convey("And the only path of that machine left in the state is its own home", func() {
				// The state records the home on purpose - it is how a run knows the
				// vault was last synced from somewhere else - so the claim is not
				// "no path of that machine appears anywhere", it is "the cache
				// carries none", and one occurrence in the whole document is the
				// home field being honest about where it came from.
				So(strings.Count(string(stateJSON(t, a)), homeA), ShouldEqual, 1)
			})
		})

		Convey("When the same vault is opened on a machine whose home is /home/b", func() {
			homeB := filepath.Join(t.TempDir(), "home", "b")
			b := newFixtureAt(t, homeB, filepath.Join(homeB, ".beagle"))
			enableCursor(t, b)

			// The vault and the host's own skills arrive the way a synced pair
			// does, and the copy keeps the modification times, because the
			// fingerprint is built from the listing including them.
			if err := os.RemoveAll(b.vault.Root()); err != nil {
				t.Fatalf("clear vault: %v", err)
			}

			copyTreeWithTimes(t, filepath.Join(homeA, ".beagle"), filepath.Join(homeB, ".beagle"))
			copyTreeWithTimes(t, filepath.Join(homeA, ".cursor"), filepath.Join(homeB, ".cursor"))

			b.sync(t)

			Convey("Then the cache holds the same keys, not a second machine's", func() {
				So(treeKeys(loadState(t, b)), ShouldResemble, keysA)
			})

			Convey("And the first machine's home is nowhere in the state", func() {
				So(strings.Contains(string(stateJSON(t, b)), homeA), ShouldBeFalse)
			})

			Convey("And the next sync on this machine writes nothing at all", func() {
				before := stateJSON(t, b)

				b.sync(t)

				So(stateJSON(t, b), ShouldResemble, before)
			})
		})
	})
}
