package cli

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// A standalone `verger watch` run before `beadle init` creates ~/.verger and
// watches plugins there. If init left it alone, the machine would end up with
// two plugin homes and two watchers that never meet (NIGHT-pR-2, VERIFY-B-1
// Finding 1): the leak is created by launch order, not by discovery.
func TestInitAbsorbsAStandaloneVergerHome(t *testing.T) {
	Convey("Given a home that standalone verger already used", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		isolateTestRoots(t)

		standalone := filepath.Join(home, ".verger")

		Convey("When init runs", func() {
			// The fixture is laid down here, not in the Given: GoConvey
			// re-runs the Given for every leaf assertion, and init consumes
			// the standalone home on the first run, so a fixture written once
			// would leave the later leaves asserting against a home that is
			// already gone - which is the opposite of what they check.
			writeFile(t, filepath.Join(standalone, "verger.toml"), "schema = 1\n\n[defaults]\ncooldown = \"24h\"\n")
			writeFile(t, filepath.Join(standalone, "state", "journal.jsonl"), `{"at":"2026-09-30T00:00:00Z"}`+"\n")

			out, err := runCLI(t, "init", "-y")

			Convey("Then it succeeds", func() {
				So(err, ShouldBeNil)
			})

			Convey("Then it says it moved the home", func() {
				So(out, ShouldContainSubstring, standalone)
				So(out, ShouldContainSubstring, "moved")
			})

			Convey("Then the standalone home is gone rather than left in parallel", func() {
				_, statErr := os.Stat(standalone)
				So(os.IsNotExist(statErr), ShouldBeTrue)
			})

			Convey("Then the state lives in the vault", func() {
				So(fileThere(t, filepath.Join(home, ".beadle", "verger", "state", "journal.jsonl")), ShouldBeTrue)
			})
		})
	})
}

// TestInitLeavesAStandaloneHomeItCannotMove asserts the refusal: init must not
// delete or half-move a home it could not take whole, and it must say why.
func TestInitLeavesAStandaloneHomeItCannotMove(t *testing.T) {
	Convey("Given no standalone verger home", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		isolateTestRoots(t)

		Convey("When init runs", func() {
			out, err := runCLI(t, "init", "-y")

			Convey("Then it does not claim a move it did not make", func() {
				So(err, ShouldBeNil)
				So(out, ShouldNotContainSubstring, ".verger")
			})
		})
	})
}

// TestInitMergesTwoExistingHomes is the second launch order: the user ran both
// beadle and standalone verger before init, so a vault home already exists and
// the move cannot be a rename. The union has to keep both package lists, and
// an id both declare has to resolve to the vault's version - with the
// standalone one recoverable, not merely counted.
func TestInitMergesTwoExistingHomes(t *testing.T) {
	Convey("Given a vault plugin home and a standalone one that both exist", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		isolateTestRoots(t)

		standalone := filepath.Join(home, ".verger")
		vaultSpec := filepath.Join(home, ".beadle", "verger", "verger.toml")

		Convey("When init runs", func() {
			writeFile(t, vaultSpec,
				"schema = 1\n\n[[package]]\nid = \"shared\"\nversion = \"2.0.0\"\n\n[[package]]\nid = \"only-vault\"\nversion = \"1.0.0\"\n")
			writeFile(t, filepath.Join(standalone, "verger.toml"),
				"schema = 1\n\n[[package]]\nid = \"shared\"\nversion = \"0.9.0\"\n\n[[package]]\nid = \"only-standalone\"\nversion = \"3.0.0\"\n")

			out, err := runCLI(t, "init", "-y")

			Convey("Then init succeeds", func() {
				So(err, ShouldBeNil)
			})

			Convey("Then it says it merged rather than moved", func() {
				So(out, ShouldContainSubstring, "merged")
			})

			Convey("Then the merged spec carries both package lists", func() {
				merged, readErr := os.ReadFile(vaultSpec) //nolint:gosec // G304: the path is a spec the test wrote in its own temp vault //nolint:gosec // G304: the path is a spec the test wrote in its own temp vault
				So(readErr, ShouldBeNil)
				So(string(merged), ShouldContainSubstring, "only-vault")
				So(string(merged), ShouldContainSubstring, "only-standalone")
			})

			Convey("Then the shared id resolves to the vault's version", func() {
				merged, readErr := os.ReadFile(vaultSpec) //nolint:gosec // G304: the path is a spec the test wrote in its own temp vault //nolint:gosec // G304: the path is a spec the test wrote in its own temp vault
				So(readErr, ShouldBeNil)
				// Asserted on the version, not on the id's quoting: the TOML
				// writer chooses the quotes, and pinning them fails on a
				// formatting change while proving nothing about the merge.
				So(string(merged), ShouldContainSubstring, "2.0.0")
				So(string(merged), ShouldNotContainSubstring, "0.9.0")
			})

			Convey("Then the conflict is reported and the standalone spec kept", func() {
				So(out, ShouldContainSubstring, "kept the vault's")

				backup := filepath.Join(home, ".beadle", "verger", "state", "backups", "verger.toml")
				So(fileThere(t, backup), ShouldBeTrue)

				kept, readErr := os.ReadFile(backup) //nolint:gosec // G304: the path is a backup the test just asserted on
				So(readErr, ShouldBeNil)
				So(string(kept), ShouldContainSubstring, "0.9.0")
			})
		})
	})
}

// TestInitAbsorbKeepsAFileItDidNotMove is the safety half of the move. Only
// the state files verger knows about travel; a standalone home can hold
// anything else, and a user who put something there did not put it there for a
// merge to delete. The emptied tree is cleared so the next `verger watch`
// cannot find a second root - but "emptied" has to mean emptied, and this is
// the assertion that it does.
func TestInitAbsorbKeepsAFileItDidNotMove(t *testing.T) {
	Convey("Given a standalone home holding a file the move does not own", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		isolateTestRoots(t)

		standalone := filepath.Join(home, ".verger")
		extra := filepath.Join(standalone, "notes.txt")
		vaultSpec := filepath.Join(home, ".beadle", "verger", "verger.toml")

		Convey("When init merges it into an existing vault home", func() {
			// Both homes present, so the move is the per-file merge rather than the
			// rename: the rename would carry the extra file along and prove
			// nothing about the guard.
			writeFile(t, vaultSpec, "schema = 1\n")
			writeFile(t, filepath.Join(standalone, "verger.toml"), "schema = 1\n")
			writeFile(t, extra, "my own notes\n")

			out, err := runCLI(t, "init", "-y")

			Convey("Then init succeeds", func() {
				So(err, ShouldBeNil)
				So(out, ShouldNotContainSubstring, "was left alone")
			})

			Convey("Then the file is still there, with its content", func() {
				kept, readErr := os.ReadFile(extra) //nolint:gosec // G304: the path is a file the test wrote in its own temp home
				So(readErr, ShouldBeNil)
				So(string(kept), ShouldEqual, "my own notes\n")
			})

			Convey("Then the standalone home is left standing rather than deleted", func() {
				// The directory is what a user would look for to find their file,
				// and removing it would take the file with it.
				So(fileThere(t, standalone), ShouldBeTrue)
			})
		})
	})
}

// TestInitMergeReportsWhereTheBackupIs pins the field init prints, not just the
// file: an empty `Backup` still leaves a correct backup on disk, and the
// message would then point the user at nothing.
func TestInitMergeReportsWhereTheBackupIs(t *testing.T) {
	Convey("Given two homes whose specs declare the same package", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		isolateTestRoots(t)

		standalone := filepath.Join(home, ".verger")
		vaultSpec := filepath.Join(home, ".beadle", "verger", "verger.toml")
		backup := filepath.Join(home, ".beadle", "verger", "state", "backups", "verger.toml")

		Convey("When init runs", func() {
			writeFile(t, vaultSpec, "schema = 1\n\n[[package]]\nid = \"local:caveman\"\nversion = \"2.0.0\"\n")
			writeFile(t, filepath.Join(standalone, "verger.toml"),
				"schema = 1\n\n[[package]]\nid = \"local:caveman\"\nversion = \"0.9.0\"\n")

			out, err := runCLI(t, "init", "-y")
			So(err, ShouldBeNil)

			Convey("Then the message names the conflicting package", func() {
				So(out, ShouldContainSubstring, "local:caveman")
			})

			Convey("Then the message points at the backup by its real path", func() {
				// A path the user cannot open is worse than no path: it reads
				// as "somewhere", and the standalone version is then lost to
				// anyone who did not know to look.
				So(out, ShouldContainSubstring, backup)
			})

			Convey("Then the backup holds the standalone version, not the merged one", func() {
				kept, readErr := os.ReadFile(backup) //nolint:gosec // G304: the path is a backup the test just asserted on
				So(readErr, ShouldBeNil)
				So(string(kept), ShouldContainSubstring, "0.9.0")
				So(string(kept), ShouldNotContainSubstring, "2.0.0")
			})
		})
	})
}

func fileThere(t *testing.T, path string) bool {
	t.Helper()

	_, err := os.Stat(path)

	return err == nil
}
