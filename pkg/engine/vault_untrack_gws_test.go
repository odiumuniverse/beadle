package engine_test

import (
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
