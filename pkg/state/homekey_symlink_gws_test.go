package state_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/state"
)

// TestHomeKeySeesThroughASymlinkedHome is the product half of a bug that only
// showed on one operating system.
//
// The key is made portable by relating a path to the home directory, and the two
// are routinely the same place spelled two ways: on macOS a temporary home lives
// under /var, which is a symlink to /private/var, and something in the scan path
// resolves it while the engine was handed the other spelling. `filepath.Rel`
// between two spellings walks out of the home and comes back as a chain of `..`,
// so the key was returned as the absolute path it started as — machine-specific,
// in a field whose whole purpose is not to be. On Linux /tmp is a real
// directory, the spellings match, and the degradation never happens.
//
// The symlink here is one the test makes, not one the operating system happens to
// have, and the two spellings are written out rather than derived: my first
// version asserted that `EvalSymlinks` changed the path, which is true on macOS
// and false on Linux, so the test asserted its own precondition and failed on the
// platform it was written to protect.
func TestHomeKeySeesThroughASymlinkedHome(t *testing.T) {
	Convey("Given a home directory reachable under two names", t, func() {
		root := t.TempDir()

		actual := filepath.Join(root, "real", "home")
		if err := os.MkdirAll(filepath.Join(actual, ".claude", "skills"), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		link := filepath.Join(root, "link")
		if err := os.Symlink(actual, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}

		// Two spellings of one directory: the engine was handed the linked one,
		// and the scan that produced the path resolved it. The strings differ on
		// every platform, which is the whole point — the comparison must not
		// depend on whether the temporary root happens to be behind a symlink.
		viaLink := filepath.Join(link, ".claude", "skills")
		viaReal := filepath.Join(actual, ".claude", "skills")
		So(viaLink, ShouldNotEqual, viaReal)

		// "One directory under two names" is asserted by reading through both,
		// not by comparing resolved strings: a temporary root is itself behind a
		// symlink on one platform and not on the other, so the resolved form of a
		// path built from it differs from the path only on macOS. Comparing the
		// FILE is the claim; comparing the two spellings of a path is not.
		marker := filepath.Join(viaReal, "marker")
		if err := os.WriteFile(marker, []byte("one directory\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}

		through, err := os.ReadFile(filepath.Join(viaLink, "marker")) //nolint:gosec // G304: the test reads the file it wrote a line above
		So(err, ShouldBeNil)
		So(string(through), ShouldEqual, "one directory\n")

		Convey("Then the linked spelling is keyed by its name, not its path", func() {
			So(state.HomeKey(viaLink, link), ShouldEqual, state.HomePrefix+".claude/skills")
		})

		Convey("And so is the resolved one, compared against the linked home", func() {
			// This is the shape the macOS scan produced: a path that came back
			// from EvalSymlinks, and a home that never did.
			resolved, resolveErr := filepath.EvalSymlinks(viaLink)
			So(resolveErr, ShouldBeNil)
			So(state.HomeKey(resolved, link), ShouldEqual, state.HomePrefix+".claude/skills")
		})

		Convey("And so is the plain spelling, so the rule does not depend on which side resolved", func() {
			So(state.HomeKey(viaReal, link), ShouldEqual, state.HomePrefix+".claude/skills")
			So(state.HomeKey(viaLink, actual), ShouldEqual, state.HomePrefix+".claude/skills")
		})

		Convey("And a path genuinely outside home keeps its absolute form", func() {
			outside := filepath.Join(root, "elsewhere")
			So(state.HomeKey(outside, link), ShouldEqual, outside)
			So(state.HomeKey(outside, actual), ShouldEqual, outside)
		})

		Convey("And a path outside home reached through a symlink is still absolute", func() {
			// The rule is "portable when it is inside", not "portable whenever a
			// symlink is involved": a place that is not under home has no portable
			// name, and inventing one is how two unrelated machines end up sharing
			// a key.
			target := filepath.Join(root, "shared")
			if err := os.MkdirAll(target, 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}

			outsideLink := filepath.Join(link, "..", "shared")
			So(state.HomeKey(outsideLink, link), ShouldEqual, outsideLink)
		})
	})
}
