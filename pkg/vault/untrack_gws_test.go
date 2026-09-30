package vault_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/vault"
)

// A vault committed before the rules carries the machine-local files with it.
// `.gitignore` does not untrack a path, so the migration has to do it - and a
// second run must find nothing left to do and say nothing.
func TestUntrackVergerState(t *testing.T) {
	Convey("Given a vault committed with the library's machine-local state", t, func() {
		home := t.TempDir()
		root := filepath.Join(home, "vault")
		v := vault.New(root)

		So(v.Init(), ShouldBeNil)

		// A machine-local file and a portable one, both tracked.
		So(os.MkdirAll(filepath.Join(root, "verger", "state", "receipts"), 0o700), ShouldBeNil)
		So(os.WriteFile(filepath.Join(root, "verger", "state", "receipts", "claude.json"), []byte("{}"), 0o600), ShouldBeNil)
		So(os.WriteFile(filepath.Join(root, "verger", "verger.lock"), []byte("{}"), 0o600), ShouldBeNil)
		git(t, root, "init")
		// -f, because a vault committed before the rules tracked these files
		// even though today's .gitignore would not.
		git(t, root, "add", "-A", "-f")

		Convey("When the migration runs", func() {
			untracked, err := v.UntrackVergerState(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the receipt is no longer tracked", func() {
				So(untracked, ShouldContain, "verger/state/receipts/claude.json")
				So(tracked(t, root), ShouldNotContain, "verger/state/receipts/claude.json")
			})

			Convey("Then it is still on disk - the machine keeps it", func() {
				_, err := os.Stat(filepath.Join(root, "verger", "state", "receipts", "claude.json"))
				So(err, ShouldBeNil)
			})

			Convey("Then the lock still travels", func() {
				So(tracked(t, root), ShouldContain, "verger/verger.lock")
			})

			Convey("Then a second run has nothing to do", func() {
				again, err := v.UntrackVergerState(t.Context())
				So(err, ShouldBeNil)
				So(again, ShouldBeEmpty)
			})
		})
	})

	Convey("Given a vault that is not a git repository", t, func() {
		v := vault.New(filepath.Join(t.TempDir(), "vault"))

		So(v.Init(), ShouldBeNil)

		Convey("Then the migration is a no-op, not an error", func() {
			untracked, err := v.UntrackVergerState(t.Context())
			So(err, ShouldBeNil)
			So(untracked, ShouldBeEmpty)
		})
	})
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // G204: the test's own git with the temp-dir paths it wrote
	cmd.Dir = dir

	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=beadle", "GIT_AUTHOR_EMAIL=b@b", "GIT_COMMITTER_NAME=beadle", "GIT_COMMITTER_EMAIL=b@b")

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func tracked(t *testing.T, dir string) []string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "git", "ls-files")
	cmd.Dir = dir

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}

	var names []string

	for _, name := range splitLines(string(out)) {
		if name != "" {
			names = append(names, name)
		}
	}

	return names
}

func splitLines(s string) []string {
	var out []string

	start := 0

	for i := range len(s) {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}

	return append(out, s[start:])
}
