package engine_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// TestTheFarmRemovalTouchesOnlyRecordedPaths is the security invariant: a file
// or a symlink in the vault's plugins directory that beadle never recorded is
// not the farm's, and it must still be there afterwards. A glob over the
// directory would take it, which is why the test puts something there that a
// glob would collect.
func TestTheFarmRemovalTouchesOnlyRecordedPaths(t *testing.T) {
	Convey("Given an unrecorded file and symlink beside the farm", t, func() {
		m := buildMachine(t)

		pluginsDir := m.vault.PluginsDir()

		// A file that is not a ledger record and not a farm pivot.
		foreignFile := filepath.Join(pluginsDir, "notes.txt")
		if err := os.WriteFile(foreignFile, []byte("a user's own file\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		// A symlink that points somewhere the farm never wrote, sitting
		// exactly where a glob would step on it.
		elsewhere := filepath.Join(m.home, "elsewhere")
		if err := os.MkdirAll(elsewhere, 0o700); err != nil {
			t.Fatal(err)
		}

		foreignLink := filepath.Join(pluginsDir, "shared-link")
		if err := os.Symlink(elsewhere, foreignLink); err != nil {
			t.Fatal(err)
		}

		// And a directory the ledger never named.
		foreignDir := filepath.Join(pluginsDir, "unrecorded", "deep")
		if err := os.MkdirAll(foreignDir, 0o700); err != nil {
			t.Fatal(err)
		}

		e := m.engine(t, true)

		Convey("When the machine migrates", func() {
			_, err := e.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then the farm is gone", func() {
				_, pivotErr := os.Lstat(filepath.Join(pluginsDir, "acme", "caveman", "current"))
				So(os.IsNotExist(pivotErr), ShouldBeTrue)
			})

			Convey("And everything beadle did not record is still there", func() {
				data, readErr := os.ReadFile(foreignFile) //nolint:gosec // G304: the test's own tree
				So(readErr, ShouldBeNil)
				So(string(data), ShouldEqual, "a user's own file\n")

				target, linkErr := os.Readlink(foreignLink)
				So(linkErr, ShouldBeNil)
				So(target, ShouldEqual, elsewhere)

				_, dirErr := os.Stat(foreignDir)
				So(dirErr, ShouldBeNil)
			})

			Convey("And the record names only what was removed", func() {
				st, loadErr := state.Load(m.vault.StatePath())
				So(loadErr, ShouldBeNil)
				So(st.FarmMigration, ShouldNotBeNil)
				So(st.FarmMigration.Removed, ShouldNotContain, foreignFile)
				So(st.FarmMigration.Removed, ShouldNotContain, foreignLink)
				So(st.FarmMigration.Removed, ShouldNotContain, foreignDir)
			})
		})
	})
}

// TestTheFarmIsNotRemovedWithoutItsBackup is the other half: the backup has to
// be a directory that exists, not a path someone recorded once.
func TestTheFarmIsNotRemovedWithoutItsBackup(t *testing.T) {
	Convey("Given a migration whose backup directory is gone", t, func() {
		m := buildMachine(t)
		e := m.engine(t, true)

		// One sync takes the backup and hands the farm over; the record then
		// names a directory that is deleted behind the migration's back.
		_, err := e.Sync(t.Context(), engine.SyncOptions{})
		So(err, ShouldBeNil)

		st, loadErr := state.Load(m.vault.StatePath())
		So(loadErr, ShouldBeNil)
		So(st.FarmMigration, ShouldNotBeNil)
		So(st.FarmMigration.Backup, ShouldNotBeEmpty)
		So(os.RemoveAll(st.FarmMigration.Backup), ShouldBeNil)

		// Re-open the migration as if it had never run, with the farm back.
		if err := os.MkdirAll(filepath.Join(m.vault.PluginsDir(), "acme", "caveman", "current"), 0o700); err != nil {
			t.Fatal(err)
		}

		reset, err := state.Load(m.vault.StatePath())
		So(err, ShouldBeNil)

		reset.FarmMigration.Done = false
		reset.FarmMigration.Stage = state.FarmStageDeliver
		So(reset.Save(m.vault.StatePath()), ShouldBeNil)

		Convey("When the machine syncs again", func() {
			report, syncErr := e.Sync(t.Context(), engine.SyncOptions{})
			So(syncErr, ShouldBeNil)

			Convey("Then it refuses to remove the farm and says why", func() {
				So(strings.Join(report.Warnings, "\n"), ShouldContainSubstring, "is not a directory")
			})

			Convey("And the farm is still there", func() {
				_, pivotErr := os.Lstat(filepath.Join(m.vault.PluginsDir(), "acme", "caveman", "current"))
				So(pivotErr, ShouldBeNil)
			})
		})
	})
}

// TestAMigrationThatRanTwiceDoesNotWorkTwice is the idempotency claim as an
// observable: a second pass over the delivery stage removes nothing new and
// records nothing new. Forgetting which paths are already gone makes the
// ledger grow the same paths again, which is how a record stops describing
// what happened.
func TestAMigrationThatRanTwiceDoesNotWorkTwice(t *testing.T) {
	Convey("Given a machine that has already migrated and is asked to deliver again", t, func() {
		m := buildMachine(t)
		e := m.engine(t, true)

		_, err := e.Sync(t.Context(), engine.SyncOptions{})
		So(err, ShouldBeNil)

		first, loadErr := state.Load(m.vault.StatePath())
		So(loadErr, ShouldBeNil)
		So(first.FarmMigration, ShouldNotBeNil)
		So(first.FarmMigration.Removed, ShouldNotBeEmpty)

		// A host re-wrote its farm link - opencode and the others do that,
		// and a repeated or resumed run sees the path again. This is the
		// case the "already removed" mark exists for: the path is back, and
		// the ledger already says what happened to it.
		revived := filepath.Join(m.vault.PluginsDir(), "acme", "caveman", "current")
		if err := os.MkdirAll(filepath.Join(revived, "skills", "caveman"), 0o700); err != nil {
			t.Fatal(err)
		}

		link := filepath.Join(m.skillsHost[0], "caveman")
		if err := os.Symlink(filepath.Join(revived, "skills", "caveman"), link); err != nil {
			t.Fatal(err)
		}

		// Re-enter the machine before the removal: the stage is recorded as
		// the last one *completed*, so a run that has to see remove() again
		// starts from the stage before it - exactly what a repeated or
		// resumed run does.
		second, err := state.Load(m.vault.StatePath())
		So(err, ShouldBeNil)

		second.FarmMigration.Done = false
		second.FarmMigration.Stage = state.FarmStageDeliver
		So(second.Save(m.vault.StatePath()), ShouldBeNil)

		Convey("When it runs the removal again", func() {
			_, syncErr := e.Sync(t.Context(), engine.SyncOptions{})
			So(syncErr, ShouldBeNil)

			Convey("Then the ledger is unchanged - no path is recorded twice", func() {
				after, loadErr := state.Load(m.vault.StatePath())
				So(loadErr, ShouldBeNil)
				So(after.FarmMigration.Removed, ShouldResemble, first.FarmMigration.Removed)
				So(slices.Sorted(slices.Values(after.FarmMigration.Removed)), ShouldResemble, after.FarmMigration.Removed)
				So(after.FarmMigration.Removed, ShouldContain, link)
				So(after.FarmMigration.Done, ShouldBeTrue)
			})
		})
	})
}
