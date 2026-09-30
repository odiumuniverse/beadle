package cli

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/testhost"
)

// A run that refused to reconcile is not a successful run. The table says what
// is blocked and the exit code says so to a script: a CI job that drives beadle
// must not read "delivered" and exit 0 while nothing was delivered and a
// destructive-looking plan is waiting for the user.
func TestSyncExitsNonZeroWhenItRefused(t *testing.T) {
	Convey("Given a vault that delivered files and then lost them all", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		isolateTestRoots(t)

		testhost.Stubs(t)

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		_, err = runCLI(t, "agents", "enable", "claude")
		So(err, ShouldBeNil)

		_, err = runCLI(t, "sync")
		So(err, ShouldBeNil)

		Convey("When the user removes everything beadle delivered", func() {
			removeDeliveredSkills(t, home)

			Convey("Then the sync reports the block and fails", func() {
				out, err := runCLI(t, "sync")

				So(out, ShouldContainSubstring, wordBlocked)

				blocked, isBlocked := engine.IsOpenConflicts(err)
				So(isBlocked, ShouldBeTrue)
				So(blocked.Conflicts, ShouldBeGreaterThan, 0)
			})
		})
	})
}

// removeDeliveredSkills takes away the skill trees beadle wrote under the home,
// which is what a user's mass deletion looks like to the next sync.
func removeDeliveredSkills(t *testing.T, home string) {
	t.Helper()

	for _, dir := range []string{
		filepath.Join(home, ".claude", "skills"),
		filepath.Join(home, ".agents", "skills"),
	} {
		So(os.RemoveAll(dir), ShouldBeNil)
	}
}
