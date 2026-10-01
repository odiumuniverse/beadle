package homescan

import (
	"path/filepath"
	"slices"
	"testing"
)

// The invariant `TestTheRootsAreNotResolvedAgainAtTheEnd` observes only through
// its effect: the recheck finds the same tree, so `Changed` comes back empty. An
// effect-based test is only as good as the mutation that is supposed to break it,
// and a mutation that rewrites the `roots` field but leaves the loop walking the
// stored roots passes it while claiming to remove the defence.
//
// So the invariant is asserted here, where the field is reachable: after the
// environment moves, the roots the recheck walks are still the ones the snapshot
// took. A Recheck that resolves afresh cannot satisfy this, whichever half of it
// was edited.
func TestRecheckWalksTheRootsTheSnapshotRecorded(t *testing.T) {
	home := t.TempDir()
	original := filepath.Join(home, ".claude", "skills")

	before := Take(home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "elsewhere"))

	after := before.Recheck()

	if !slices.Contains(after.roots, original) {
		t.Errorf("Recheck dropped the root the snapshot recorded: %s", original)
	}

	if slices.Contains(after.roots, filepath.Join(home, "elsewhere", "skills")) {
		t.Error("Recheck resolved the claude skills surface again and followed the environment")
	}

	// And the snapshot keeps its own: a later Recheck chains from the same place.
	if !slices.Contains(before.Recheck().roots, original) {
		t.Error("a second Recheck no longer walks the recorded roots")
	}
}
