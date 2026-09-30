package engine_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/engine"
)

// farmTree is the farm's whole content, path to bytes. A "nothing changed"
// claim about a rendered tree is only worth anything over every file in it.
func farmTree(t *testing.T, root string) map[string]string {
	t.Helper()

	out := map[string]string{}

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		data, readErr := os.ReadFile(path) //nolint:gosec // G304: the test reads its own temp tree
		if readErr != nil {
			return readErr
		}

		out[path] = string(data)

		return nil
	})
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}

	return out
}

// TestTheFirstSyncKeepsTheFarmSkillsItJustDelivered is the farm half of what the
// first sync does, and it is here rather than in pkg/cli because the shape that
// matters cannot be built across two processes.
//
// The vault is a synced pair, so the naive way to test the farm is `init` and
// then `sync` as two commands. That shape cannot fail for the reason the bug
// had: by the time the second process starts, the first one's delivery is on
// disk, the coverage question has an answer that needs no judgement, and the
// render agrees with itself. The regression test written that way stayed green
// against the reverted fix, which is what VERIFY-CP-beadle-pS-5 §3 caught.
//
// The bug lived in one run. `syncKinds` pushes the canon skills to the host's
// file surface, and the farm refresh later in the SAME run reads a coverage view
// in which `<home>/.claude/skills/<name>` now exists - and credits it, because
// "a copy is there" and "the bundle delivers it" look identical. So the farm
// dropped the very skills it was about to deliver, the second sync put them
// back, and a vault that changes shape on every other run is a vault no script
// can hash. The fix is that a file surface is not a delivery path once the
// bundle owns the kind, so its copies are not credited.
func TestTheFirstSyncKeepsTheFarmSkillsItJustDelivered(t *testing.T) {
	Convey("Given a vault whose canon skills and a claude bundle the first sync will enable", t, func() {
		f := bundleFixture(t)

		cli, _ := claudeBundleCLI(t, f)
		fakeRunner(t, cli)
		foundCLI(t)

		e := autoBundleEngine(t, f)
		farmSkill := filepath.Join(f.vault.BundlesDir(), "claude", "plugins", "beadle-canon", "skills", "alpha", "SKILL.md")
		fileSurfaceCopy := filepath.Join(f.home, ".claude", "skills", "alpha", "SKILL.md")

		Convey("When the one run that both delivers the skills and enables the bundle finishes", func() {
			f.runOn(t, e, engine.SyncOptions{})

			Convey("Then the file surface holds the copy that would otherwise hide the skill", func() {
				// Asserted first, and asserted as present on purpose: without it
				// the leaves below would pass on a farm that never faced the
				// question, which is the failure mode of every assertion about a
				// render that may not have been asked.
				So(read(t, fileSurfaceCopy), ShouldEqual, "# alpha\n")
			})

			Convey("And the farm holds the canon skill", func() {
				So(read(t, farmSkill), ShouldEqual, "# alpha\n")
			})

			Convey("And a second run changes nothing, so the first one settled it", func() {
				// This is the leaf that carries the fix, and it is worth being
				// exact about why the leaf above does not: with the file surface
				// credited again, the first run still happens to leave the skill
				// in the farm, and only the NEXT run drops it — the two manifests
				// re-stamping between `0.0.0-0d2b5825493c` and
				// `0.0.0-b6d932ebc8a1` on alternate runs. So the property worth
				// guarding is not "the farm has the skill", which a vault can
				// satisfy while oscillating; it is "the first run settled it",
				// which is what makes the vault hashable.
				before := farmTree(t, f.vault.BundlesDir())
				f.runOn(t, e, engine.SyncOptions{})
				So(farmTree(t, f.vault.BundlesDir()), ShouldResemble, before)
			})
		})
	})
}
