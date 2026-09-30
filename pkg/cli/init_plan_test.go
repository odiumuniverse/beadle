package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
)

// runInitOn runs `beadle init` on a fresh isolated home and returns what the
// user would have seen. The vault root is derived from HOME so the assertions
// do not depend on a machine's real paths.
func runInitOn(t *testing.T, home, stdin string, args ...string) string {
	t.Helper()

	// Only HOME: BEADLE_HOME *is* the vault root, so setting it to the home
	// would put the vault in the home rather than under it, and the existence
	// assertions below would look in the wrong place.
	t.Setenv("HOME", home)
	isolateTestRoots(t)
	t.Setenv(vaultEnvForTest, "")

	root := NewRootCmd(Options{Version: "test"})

	var out bytes.Buffer

	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"init"}, args...))
	root.SilenceUsage = true

	So(root.ExecuteContext(t.Context()), ShouldBeNil)

	return out.String()
}

// runInitCode is runInitOn without the nil assertion, so a test can pin the
// exit code itself. runInitOn failing on any error is the right default for a
// screen assertion and the wrong tool for "and it exits 0".
func runInitCode(t *testing.T, home, stdin string, args ...string) (int, string) {
	t.Helper()

	t.Setenv("HOME", home)
	isolateTestRoots(t)
	t.Setenv(vaultEnvForTest, "")

	root := NewRootCmd(Options{Version: "test"})

	var out bytes.Buffer

	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"init"}, args...))
	root.SilenceUsage = true

	err := root.ExecuteContext(t.Context())
	if err != nil {
		return 1, out.String()
	}

	return 0, out.String()
}

const vaultEnvForTest = "BEADLE_HOME"

func TestInitPrintsThePlanBeforeItWrites(t *testing.T) {
	Convey("Given a home with nothing in it", t, func() {
		home := t.TempDir()

		out := runInitOn(t, home, "n\n")

		Convey("Then the screen names what it found, what it will not touch, and how to stop each piece", func() {
			So(out, ShouldContainSubstring, "Here is what I found")
			So(out, ShouldContainSubstring, "what I will not touch")
			So(out, ShouldContainSubstring, "to turn things off, one at a time")
			So(out, ShouldContainSubstring, "beadle daemon uninstall")
			So(out, ShouldContainSubstring, "beadle agents --disable")
		})

		Convey("Then it asked exactly once", func() {
			So(strings.Count(out, "[Y/n]"), ShouldEqual, 1)
		})

		Convey("Then saying no wrote nothing at all", func() {
			_, err := os.Stat(filepath.Join(home, ".beadle"))
			So(os.IsNotExist(err), ShouldBeTrue)
			So(out, ShouldContainSubstring, "nothing was written")
		})
	})
}

func TestInitDryRunWritesNothing(t *testing.T) {
	Convey("Given --dry-run", t, func() {
		home := t.TempDir()

		out := runInitOn(t, home, "", "--dry-run")

		Convey("Then the vault does not exist afterwards", func() {
			// The flag is read inside RunE, not where it was registered: an
			// earlier version copied the value at construction time, so the
			// flag was inert and the vault appeared anyway.
			_, err := os.Stat(filepath.Join(home, ".beadle"))
			So(os.IsNotExist(err), ShouldBeTrue)
		})

		Convey("Then the last line says it was a preview", func() {
			So(out, ShouldContainSubstring, "nothing was written: this was a preview")
		})
	})
}

func TestInitYesSkipsTheQuestion(t *testing.T) {
	Convey("Given -y", t, func() {
		home := t.TempDir()

		out := runInitOn(t, home, "", "-y")

		Convey("Then it asked nothing and created the vault", func() {
			So(out, ShouldNotContainSubstring, "[Y/n]")
			So(existsDir(filepath.Join(home, ".beadle")), ShouldBeTrue)
		})
	})
}

func TestInitRerunIsANoOp(t *testing.T) {
	Convey("Given a home that has already been set up", t, func() {
		home := t.TempDir()
		runInitOn(t, home, "", "-y")

		out := runInitOn(t, home, "")

		Convey("Then it says so, asks nothing, and exits cleanly", func() {
			// Idempotence: a second run must not ask again and must not write
			// again. The earlier version required nothing to be found, so on
			// any machine with one agent it asked and wrote every time.
			So(out, ShouldContainSubstring, "Already set up, nothing to change.")
			So(out, ShouldNotContainSubstring, "[Y/n]")
		})
	})
}

func TestInitNamesAnAgentThatAppearedBetweenRuns(t *testing.T) {
	Convey("Given a set-up home and an agent that appeared since", t, func() {
		home := t.TempDir()
		runInitOn(t, home, "", "-y")

		// The agent appears after the first run.
		So(os.MkdirAll(filepath.Join(home, ".claude"), 0o700), ShouldBeNil)

		// No answer supplied: the run must not ask.
		out := runInitOn(t, home, "")

		Convey("Then it names the change against the vault that is already there", func() {
			So(out, ShouldContainSubstring, "vault already exists at")
			So(out, ShouldContainSubstring, "new since the last run")
			So(out, ShouldContainSubstring, "Claude Code")
			So(out, ShouldContainSubstring, "not enabled yet")
			// The change is applied, not proposed: the vault is there, so
			// there is nothing to confirm, and a re-run that asked "create
			// the vault?" about a vault on disk was the reported bug.
			So(out, ShouldNotContainSubstring, "[Y/n]")
			So(out, ShouldNotContainSubstring, "Here is what I found")
		})
	})
}

// TestInitExitIsZeroOnRerun pins the exit code of the re-run, which is what a
// script reads: a second `beadle init` that found a new agent is a successful
// run that applied it, and a non-zero exit would tell a caller to stop.
func TestInitExitIsZeroOnRerun(t *testing.T) {
	Convey("Given a set-up home and an agent that appeared since", t, func() {
		home := t.TempDir()
		runInitOn(t, home, "", "-y")
		So(os.MkdirAll(filepath.Join(home, ".claude"), 0o700), ShouldBeNil)

		code, out := runInitCode(t, home, "")

		Convey("Then the run exits 0 and applied the change", func() {
			So(code, ShouldEqual, 0)
			So(out, ShouldContainSubstring, "vault already exists at")
			So(enabledAgents(filepath.Join(home, ".beadle"))["claude"], ShouldBeTrue)
		})
	})
}

func TestTheQuestionDefaultsToYes(t *testing.T) {
	Convey("Given a closed stdin", t, func() {
		home := t.TempDir()

		// A script is the most likely reader of this prompt; a closed stdin is
		// not a failure, it is the documented default.
		out := runInitOn(t, home, "")

		Convey("Then the run proceeds and the vault is created", func() {
			So(out, ShouldNotContainSubstring, "read the confirmation")
			So(existsDir(filepath.Join(home, ".beadle")), ShouldBeTrue)
		})
	})

	Convey("Given an explicit no", t, func() {
		So(answerIsYes(""), ShouldBeTrue)
		So(answerIsYes("y"), ShouldBeTrue)
		So(answerIsYes("YES"), ShouldBeTrue)
		So(answerIsYes("n"), ShouldBeFalse)
		So(answerIsYes("no"), ShouldBeFalse)
	})
}

// The plan is data first: a test can assert what was found without matching the
// prose around it.
func TestThePlanIsData(t *testing.T) {
	Convey("Given two detected agents", t, func() {
		agents := []*agent.Agent{
			{ID: "a", Name: "Alpha", Detect: func() (bool, error) { return true, nil }},
			{ID: "b", Name: "Beta", Detect: func() (bool, error) { return false, nil }},
		}

		p := buildPlan("/vault", t.TempDir(), agents, nil)

		Convey("Then each host carries its own answer", func() {
			So(p.Hosts, ShouldHaveLength, 2)
			So(p.Hosts[0].Found, ShouldBeTrue)
			So(p.Hosts[1].Found, ShouldBeFalse)
		})

		Convey("Then a found host is counted, and a missing one is not", func() {
			So(p.countFound(), ShouldEqual, 1)
		})

		Convey("Then the honest list of what is not touched is present and non-empty", func() {
			So(p.Untouched, ShouldNotBeEmpty)
			So(p.Plugin, ShouldContainSubstring, "verger")
		})
	})
}
