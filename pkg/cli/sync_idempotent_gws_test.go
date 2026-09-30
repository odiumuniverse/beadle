package cli

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/testhost"
)

// The external gate item `migrate.idempotent`: hash every file under the vault,
// run the same command again, and require the hashes to match. A sync that moves
// its own vault on a re-run is not idempotent, and a script that syncs on a
// timer cannot tell that apart from real work.
//
// The failure was a phase-order defect, not a version guard. `beadle init`
// renders the per-host farm, but the canon package the farm is rendered *from*
// is only written later in the same sync — so the first sync after init dropped
// the farm's canon skills, and the second sync put them back. Two runs, and the
// vault only stopped moving on the third.
func TestSyncIsIdempotentOnTheVaultBytes(t *testing.T) {
	Convey("Given an initialized vault", t, func() {
		// The hosts are pinned, not inherited. Which agents a machine has is
		// decided by `exec.LookPath` on their CLIs, so a test that lets PATH
		// and HOME decide measures the developer's laptop: the same assertion
		// then means one thing on a mac with two CLIs installed and another on
		// a Linux box with none. The stubs are inert - they exit 0 - so what
		// they contribute is *which* hosts exist, never what they answer.
		testhost.Stubs(t)

		home := gwsHome(t)
		So(os.MkdirAll(filepath.Join(home, ".claude"), 0o700), ShouldBeNil)

		_, err := runCLI(t, "init", "-y")
		So(err, ShouldBeNil)

		vaultDir := filepath.Join(home, ".beadle")

		Convey("And once the vault is settled, a further sync moves nothing", func() {
			// The idempotency claim, and the shape the external gate uses:
			// hash every file, sync again, require the hashes to match. This
			// used to fail twice over — the farm lost the canon skills on the
			// first sync and the skill-tree cache re-stamped state.json on
			// every run, so a vault that had already synced still hashed
			// differently after the next one.
			_, err := runCLI(t, "sync")
			So(err, ShouldBeNil)

			settled := snapshotTree(t, vaultDir)
			So(settled, ShouldNotBeEmpty)

			_, err = runCLI(t, "sync")
			So(err, ShouldBeNil)

			Convey("Then the second run leaves every file byte-identical", func() {
				So(snapshotTree(t, vaultDir), ShouldResemble, settled)
			})
		})
	})
}

// readTree maps every file under root to its bytes, for the farm subtree the
// first-sync leaf compares.
func readTree(t *testing.T, root string) map[string]string {
	t.Helper()

	out := map[string]string{}

	if _, err := os.Stat(root); err != nil {
		return out
	}

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		out[path] = readFile(t, path)

		return nil
	})
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}

	return out
}

// TestFirstSyncDoesNotReRenderTheFarmItWasGiven is a sentinel, not the guard.
//
// It needs a fixture that actually has a native bundle, which is a different
// fixture from the idempotency one: with `testhost.Stubs` every host reports
// itself present, the bundle probe never verifies, and `init` writes no farm at
// all — so an assertion about the farm there would pass on an empty tree. Here
// the host is present the way a machine that has run Claude Code once looks,
// which is what makes init render the farm in the first place.
//
// It was written as the regression test for the farm losing what init had
// already settled, and it is green with the fix reverted — VERIFY-CP-beadle-pS-5
// §3 measured that, and this pass confirmed why. The bug lived inside ONE run:
// syncKinds pushed the canon skills to the host's file surface and the farm
// refresh later in the same run credited that copy as a provider. Across two
// processes the first run's delivery is already on disk, the coverage question
// answers itself, and the render agrees with itself — so this shape cannot fail
// for the reason the bug had, whichever way the code is built.
//
// What it is worth is narrower and still real: that a vault handed to beadle
// and then synced, with a native bundle rendered at init, keeps the farm init
// rendered. The guard that fails without the fix is
// TestTheFirstSyncKeepsTheFarmSkillsItJustDelivered in pkg/engine, which runs
// the delivery and the farm refresh in one process — the shape the bug needs.
// This one stays because the cross-process property is worth stating once, and
// its doc comment now says which of the two is load-bearing instead of implying
// it is this one.
func TestFirstSyncDoesNotReRenderTheFarmItWasGiven(t *testing.T) {
	Convey("Given a vault whose native bundle init rendered", t, func() {
		home := gwsHome(t)
		So(os.MkdirAll(filepath.Join(home, ".claude"), 0o700), ShouldBeNil)

		// The bundle attempt has to be on: the suite's default options disable
		// the unattended defaults, so `init` would render no farm at all and
		// every assertion below would be about an empty directory.
		_, err := gwsRunAuto(t, "init", "-y")
		So(err, ShouldBeNil)

		vaultDir := filepath.Join(home, ".beadle")
		farm := filepath.Join(vaultDir, "bundles")
		before := readTree(t, farm)

		Convey("Then there is a farm to keep", func() {
			// Without this the leaf below would pass on an empty subtree, which
			// is the failure mode of every assertion written about a directory
			// that may not exist.
			So(before, ShouldNotBeEmpty)
		})

		Convey("When the first sync runs", func() {
			_, err := gwsRunAuto(t, "sync")
			So(err, ShouldBeNil)

			Convey("Then the farm is exactly what init rendered", func() {
				So(readTree(t, farm), ShouldResemble, before)
			})
		})
	})
}
