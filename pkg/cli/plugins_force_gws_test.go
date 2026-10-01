package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// TestPluginsForceKeepsTheEditedFile is FLAG-PARITY item 1 on the surface
// beadle actually owns.
//
// Before this, a plugin file the user had edited was protected by an accident
// of plumbing: the library refuses to overwrite it, and beadle exposed no way
// to say "I know, overwrite it". So the only two outcomes were "your edit is
// silently kept" and "no way forward at all".
//
// `--force` is the way forward, and it is only honest if the previous version
// is kept. The library does that itself: `apply.Options.Force` copies the
// user's file under the state directory and reports the copy's path, and an
// empty `BackupsRoot` disables forcing rather than inventing a place. These
// tests pin the three states that matter — refused, forced, recoverable — and
// the recovery is read off the disk, not off the message.
//
// KNOWN UPSTREAM GAP (2026-09-29). The two `--force` steps above pass the flag
// all the way down — `plugins install --force` → `vergerx.InstallFor` →
// `verger.ApplyOptions.Force` → `execOptions` → `apply.Options.Force` with
// `BackupsRoot` set by the facade. The re-install of an already-delivered
// package then fails to overwrite, and the cell reports `hands-off`, because
// the *undo* path in verger's `pkg/apply` never consults Force:
// `undoEntry` → `undoMismatch` returns hands-off whenever the file's digest
// differs from the receipt's, with no Force branch. The Force branch exists
// only on the forward-write path. So a re-install of an edited file is
// refused whatever beadle passes.
//
// That is `pkg/apply` in verger, not beadle, and the fix was one branch in
// `undoMismatch`: when `r.opts.Force && r.opts.BackupsRoot != ""`, take the
// same backup the forward path takes and let the write proceed. That branch is
// in the verger beadle builds against now, and the force step of this test
// passes — it is the guard that keeps it passing, so the description above is
// history rather than the current state.
// Pins guide/humans.md:71-73 and guide/ai-agents.md:240-242: a plugin file the
// human edited is not overwritten silently — the run refuses, `--force` is the
// only way past it, what it wrote first is kept under
// `<vault>/state/backups/<timestamp>/` with the path printed, and `-y` does not
// imply `--force`.
func TestPluginsForceKeepsTheEditedFile(t *testing.T) {
	// One leaf, four steps in sequence. GoConvey re-runs the body of an outer
	// block once per leaf, and the vault path is resolved from HOME once per
	// process, so a branching body would install into one home and read from
	// another. The steps are order-dependent by nature anyway: you cannot
	// force what was not first installed and then edited.
	Convey("Given a plugin installed, edited, re-installed and finally forced", t, func() {
		home := t.TempDir()
		t.Setenv("HOME", home)
		isolateTestRoots(t)
		t.Setenv("npm_config_prefix", filepath.Join(home, "npm"))

		pkg := newLocalPackage(t, home)
		So(os.MkdirAll(filepath.Join(home, ".claude", "skills"), 0o700), ShouldBeNil)

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		_, _, err = runCLISplit(t, "plugins", "install", pkg, "--yes")
		So(err, ShouldBeNil)

		installed := filepath.Join(home, ".claude", "skills", "verify-skill", "SKILL.md")

		data, readErr := os.ReadFile(installed) //nolint:gosec // G304: the test reads a path it created
		So(readErr, ShouldBeNil)
		So(string(data), ShouldNotBeEmpty)

		// The user edits the file the tool delivered.
		So(os.WriteFile(installed, []byte("my own copy\n"), 0o600), ShouldBeNil)

		// Step one: a plain re-install must not touch the user's bytes, and
		// must not quietly take a backup either — `-y` accepts defaults and
		// never resolves a destructive conflict, so an overwrite here would
		// be authorised by a flag that promises the opposite.
		plainOut, _, err := runCLISplit(t, "plugins", "install", pkg, "--yes")
		So(err, ShouldBeNil)

		afterPlain, err := os.ReadFile(installed) //nolint:gosec // G304: the test reads a path it created
		So(err, ShouldBeNil)
		So(string(afterPlain), ShouldContainSubstring, "my own copy")
		So(printedBackupPath(plainOut), ShouldBeEmpty)
		// Step two: with --force, the library's copy lands and the user's is
		// kept under the state directory, with the path reported.
		out, _, err := runCLISplit(t, "plugins", "install", pkg, "--yes", "--force")
		So(err, ShouldBeNil)

		afterForce, err := os.ReadFile(installed) //nolint:gosec // G304: the test reads a path it created
		So(err, ShouldBeNil)
		So(string(afterForce), ShouldNotContainSubstring, "my own copy")
		// What makes --force safe is that the previous version is somewhere
		// real to get it from, and that somewhere is the path the tool PRINTED.
		// So the check starts from the printed line and ends on a file with
		// the user's bytes in it: a tool that named a path which does not exist
		// has failed, and a test that looked in a directory it invented would
		// not have noticed.
		backupRoot := printedBackupPath(out)
		So(backupRoot, ShouldNotBeEmpty)

		Convey("Then the printed path is a file holding the user's copy", func() {
			So(findFileContaining(t, backupRoot, "my own copy"), ShouldNotBeEmpty)
		})
	})
}

// printedBackupPath reads the path back out of the install output — the one
// the user is shown. Deriving the search from the output rather than from a
// path the test builds itself is the point: the promise is "this is where your
// copy is", so the promise is what has to be checked.
func printedBackupPath(out string) string {
	for line := range strings.SplitSeq(out, "\n") {
		if _, after, ok := strings.Cut(line, "your previous version: "); ok {
			return strings.TrimSpace(after)
		}
	}

	return ""
}

// findFileContaining walks root and returns the first file whose contents hold
// want, or "" when there is none. Root is whatever the tool printed, so this
// trusts nothing but the disk.
func findFileContaining(t *testing.T, root, want string) string {
	t.Helper()

	found := ""
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || found != "" || info == nil || info.IsDir() {
			return nil //nolint:nilerr // an unreadable entry cannot be the backup we are looking for
		}

		data, readErr := os.ReadFile(path) //nolint:gosec // G304: the test walks the tree the tool named
		if readErr == nil && strings.Contains(string(data), want) {
			found = path
		}

		return nil
	})

	return found
}
