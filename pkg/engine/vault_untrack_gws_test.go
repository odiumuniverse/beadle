package engine_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/engine"
)

// The migration has to be visible: a vault that quietly stopped tracking files
// would look like a vault that lost them.
func TestSyncStopsTrackingMachineLocalVergerFiles(t *testing.T) {
	Convey("Given a vault committed with the library's machine-local state", t, func() {
		f := newFixture(t)
		vaultRoot := f.vault.Root()

		receipt := filepath.Join(vaultRoot, "verger", "state", "receipts", "claude.json")
		So(os.MkdirAll(filepath.Dir(receipt), 0o700), ShouldBeNil)
		So(os.WriteFile(receipt, []byte("{}"), 0o600), ShouldBeNil)

		gitRun(t, vaultRoot, "init")
		// -f: today's .gitignore would not stage it, but a vault committed
		// before the rules did.
		gitRun(t, vaultRoot, "add", "-A", "-f")

		Convey("When sync runs", func() {
			report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then it says what it stopped tracking", func() {
				joined := strings.Join(report.Notes, "\n")
				So(joined, ShouldContainSubstring, "stopped tracking")
				So(joined, ShouldContainSubstring, "machine-local")
			})

			Convey("Then the file is untracked and still on disk", func() {
				So(gitTracked(t, vaultRoot), ShouldNotContain, "verger/state/receipts/claude.json")

				_, statErr := os.Stat(receipt)
				So(statErr, ShouldBeNil)
			})

			Convey("Then a second sync says nothing: the migration is one-time", func() {
				again, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
				So(err, ShouldBeNil)
				So(strings.Join(again.Notes, "\n"), ShouldNotContainSubstring, "stopped tracking")
			})
		})
	})
}

// A vault that is not in git at all is a legitimate choice, and the sync
// migration has to be invisible there. Found by VERIFY-CP-beadle-pC-6: the
// migration was covered where it is called directly and the doctor was covered,
// but nothing said what a plain `sync` does in a directory that was never a
// repository — which is the only shape a user who keeps no version control ever
// runs.
func TestSyncOnAVaultThatIsNotInGitDoesNothingAndSaysNothing(t *testing.T) {
	Convey("Given a vault that is not a git work tree at all", t, func() {
		f := newFixture(t)

		secret := filepath.Join(f.vault.Root(), "mcp", "secrets.json")
		So(os.MkdirAll(filepath.Dir(secret), 0o700), ShouldBeNil)
		So(os.WriteFile(secret, []byte(`{"token":"never-in-git"}`), 0o600), ShouldBeNil)

		Convey("When doctor runs, it says nothing about a repository", func() {
			// A vault that was never a repository has no history to warn
			// about, and inventing a finding for it would train a user to
			// ignore the ones that matter.
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)
			So(findingWithSubject(issues, "vault.secrets-in-git-history"), ShouldBeNil)
			So(findingWithSubject(issues, "vault.secrets-staged"), ShouldBeNil)
		})

		Convey("When sync runs, the migration has nothing to do and says nothing", func() {
			report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			// Zero warnings, not "no warning about git": a migration that
			// reached for git in a directory that is not a repository and
			// passed git's own error on would put a warning in the report of
			// every sync of every user who keeps their vault out of version
			// control, which is a legitimate thing to do.
			So(report.Warnings, ShouldBeEmpty)

			// And the sync really ran, so the row above is not satisfied by
			// a sync that reported nothing at all because it did nothing.
			So(report.Notes, ShouldNotBeEmpty)
			So(strings.Join(report.Notes, "\n"), ShouldNotContainSubstring, "stopped tracking")

			// The sharp one: a migration with nothing to do must not help by
			// initialising a repository. beadle does not get to decide that a
			// user's vault belongs in git.
			_, statErr := os.Stat(filepath.Join(f.vault.Root(), ".git"))
			So(os.IsNotExist(statErr), ShouldBeTrue)
		})

		Convey("When sync runs, git is asked whether there is a repository and nothing else", func() {
			// The guard IS this case, and the report cannot see it: with the
			// guard deleted the migration still returns two nils, because
			// trackedMachineLocal swallows git's error and finds nothing
			// tracked. Both versions look identical from outside, which is why
			// the leaf above could not pin the guard (VERIFY-CP-beadle-pC-8-
			// untrack, M1). What does differ is the question beadle puts to
			// git. A directory that is not a repository has no index to read
			// and nothing to untrack, so the only call that belongs there is
			// the one asking whether it is a repository at all — and asking
			// git to read an index that does not exist is a command whose
			// failure the code is about to ignore.
			log := gitSpy(t)

			report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)
			So(report.Warnings, ShouldBeEmpty)

			// Scoped to the vault root on purpose: the sync also asks git
			// about the repository beadle itself is running in, and those
			// calls belong to other code. Exactly one call, and it is the
			// guard's, pins both halves at once — the guard ran, and nothing
			// followed it. A future read-only call added here would fail this
			// leaf by name, which is the right way to find out.
			So(gitCallsIn(t, log, f.vault.Root()), ShouldResemble, []string{"rev-parse --is-inside-work-tree"})
		})
	})
}

// gitSpy puts a `git` at the front of PATH that records every invocation and
// then hands over to the real one. It must delegate: a shim that answered for
// itself would decide the test's own premise, and the guard turns on git
// failing where a repository would succeed.
func gitSpy(t *testing.T) string {
	t.Helper()

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git on PATH: %v", err)
	}

	dir, log := t.TempDir(), ""
	log = filepath.Join(dir, "invocations")
	shim := fmt.Sprintf("#!/usr/bin/env bash\nprintf '%%s\\t%%s\\n' \"$PWD\" \"$*\" >> %q\nexec %s \"$@\"\n", log, realGit)

	// 0o700, not 0o600: PATH only finds an executable, and a shim nobody can
	// execute would make this leaf pass without ever being called.
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(shim), 0o700); err != nil { //nolint:gosec // G306: it must be executable to be found on PATH
		t.Fatalf("write git shim: %v", err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return log
}

// gitCallsIn returns the arguments git was invoked with while its working
// directory was dir, in the order they happened. Keying on the directory is
// what separates the vault's own conversation with git from the one beadle has
// with the repository it is running inside.
func gitCallsIn(t *testing.T, log, dir string) []string {
	t.Helper()

	raw, err := os.ReadFile(log) //nolint:gosec // G304: the shim wrote this path
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		t.Fatalf("read git spy log: %v", err)
	}

	var calls []string

	for line := range strings.SplitSeq(strings.TrimRight(string(raw), "\n"), "\n") {
		if where, args, ok := strings.Cut(line, "\t"); ok && where == dir {
			calls = append(calls, args)
		}
	}

	return calls
}

// The credential case is the one the migration exists for, and it is the only
// one where dropping the path from the index is not enough: a vault committed
// with its secrets file carries those values in the history, and a file the
// user can no longer trust has to keep being reported after it leaves the
// index. beadle does not rewrite history — that is the user's git and the
// user's call — so the finding carries the command and says so.
func TestSyncDropsTrackedSecretsAndDoctorSaysWhatToDoAboutThem(t *testing.T) {
	Convey("Given a vault committed with a secrets file and machine-local state", t, func() {
		f := newFixture(t)
		vaultRoot := f.vault.Root()

		secret := filepath.Join(vaultRoot, "mcp", "secrets.json")
		So(os.MkdirAll(filepath.Dir(secret), 0o700), ShouldBeNil)
		So(os.WriteFile(secret, []byte(`{"token":"never-in-git"}`), 0o600), ShouldBeNil)

		journal := filepath.Join(vaultRoot, "state", "journal.jsonl")
		So(os.MkdirAll(filepath.Dir(journal), 0o700), ShouldBeNil)
		So(os.WriteFile(journal, []byte("{}\n"), 0o600), ShouldBeNil)

		gitRun(t, vaultRoot, "init")
		// -f: today's .gitignore would not stage them, but a vault committed
		// before the rules did.
		gitRun(t, vaultRoot, "add", "-A", "-f")
		gitRun(t, vaultRoot, "commit", "-m", "before the rules")

		Convey("When sync runs", func() {
			report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then the secret and the machine record leave the index", func() {
				tracked := gitTracked(t, vaultRoot)
				So(tracked, ShouldNotContain, "mcp/secrets.json")
				So(tracked, ShouldNotContain, "state/journal.jsonl")
			})

			Convey("Then both files are still on disk", func() {
				// git rm --cached, not git rm: beadle never deletes what the
				// user's own vault holds.
				So(fileOnDisk(secret), ShouldBeTrue)
				So(fileOnDisk(journal), ShouldBeTrue)
			})

			Convey("Then it says what it stopped tracking", func() {
				joined := strings.Join(report.Notes, "\n")
				So(joined, ShouldContainSubstring, "stopped tracking")
				So(joined, ShouldContainSubstring, "mcp/secrets.json")
			})

			Convey("Then a second sync has nothing left to do", func() {
				again, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
				So(err, ShouldBeNil)
				So(strings.Join(again.Notes, "\n"), ShouldNotContainSubstring, "stopped tracking")
			})

			Convey("Then doctor warns that the values may be in the history", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)

				found := findingWithSubject(issues, "vault.secrets-in-git-history")
				So(found, ShouldNotBeNil)
				So(found.Severity, ShouldEqual, engine.SeverityWarning)
				// The message, not a paraphrase of it: these are the words a
				// user acts on, and a warning nobody can act on is noise.
				So(found.Message, ShouldContainSubstring, "mcp/secrets.json")
				So(found.Message, ShouldContainSubstring, "Rotate the credentials")
				So(found.Message, ShouldContainSubstring, "rewrite the history if the repository is shared")
				So(found.Message, ShouldContainSubstring, "beadle does not rewrite history")

				// A finding with no command is a complaint. The command is
				// the user's own git, because the history is the user's.
				So(found.Fix, ShouldHaveLength, 1)
				So(strings.Join(found.Fix[0], " "), ShouldContainSubstring, "filter-repo")

				// The promise that beadle never rewrites history, in the one
				// form that can actually break it: `doctor --fix` applies the
				// findings marked safe, and a history rewrite is not a repair
				// beadle owns.
				So(found.SafeToAutofix, ShouldBeFalse)
				So(found.CanAutoFix(), ShouldBeFalse)
			})
		})
	})

	Convey("Given a vault a user has just run git init and git add -A in", t, func() {
		f := newFixture(t)

		secret := filepath.Join(f.vault.Root(), "mcp", "secrets.json")
		So(os.MkdirAll(filepath.Dir(secret), 0o700), ShouldBeNil)
		So(os.WriteFile(secret, []byte(`{"token":"never-in-git"}`), 0o600), ShouldBeNil)

		gitRun(t, f.vault.Root(), "init")
		// -f, and no commit: this is what a user who has just been handed a
		// vault does with it, and the values are one `git commit` away.
		gitRun(t, f.vault.Root(), "add", "-A", "-f")

		Convey("When doctor runs before any sync", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			staged := findingWithSubject(issues, "vault.secrets-staged")
			So(staged, ShouldNotBeNil)
			So(staged.Message, ShouldContainSubstring, "the next commit would carry it")

			// Not the history finding: nothing has been committed, and
			// telling a user to rotate credentials for a file that has
			// never been in a commit is a cry for help they do not need.
			So(findingWithSubject(issues, "vault.secrets-in-git-history"), ShouldBeNil)

			Convey("Then the command it names is one beadle can run", func() {
				So(strings.Join(staged.Fix[0], " "), ShouldEqual, "beadle sync")
			})
		})

		Convey("When sync runs, the finding is gone afterwards", func() {
			_, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			// A warning that goes away when the thing it warns about is
			// fixed is the only kind worth printing.
			So(findingWithSubject(issues, "vault.secrets-staged"), ShouldBeNil)
			So(findingWithSubject(issues, "vault.secrets-in-git-history"), ShouldBeNil)
		})
	})
}

func findingWithSubject(issues []engine.Issue, subject string) *engine.Issue {
	for i := range issues {
		if issues[i].Subject == subject {
			return &issues[i]
		}
	}

	return nil
}

func fileOnDisk(path string) bool {
	info, err := os.Stat(path)

	return err == nil && !info.IsDir()
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()

	//nolint:gosec // G204: the test runs git with the arguments and paths it just wrote itself
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir

	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=beadle", "GIT_AUTHOR_EMAIL=b@b",
		"GIT_COMMITTER_NAME=beadle", "GIT_COMMITTER_EMAIL=b@b")

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitTracked(t *testing.T, dir string) []string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "git", "ls-files")
	cmd.Dir = dir

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}

	var names []string

	for line := range strings.SplitSeq(strings.TrimRight(string(out), "\n"), "\n") {
		if line != "" {
			names = append(names, line)
		}
	}

	return names
}
