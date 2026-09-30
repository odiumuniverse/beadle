package cli

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vmkteam/embedlog"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// fakeDaemonChecker reports every service as absent without running
// launchctl or systemctl.
type fakeDaemonChecker struct{}

func (fakeDaemonChecker) Run(string, []string, []byte) ([]byte, int, error) {
	return nil, 1, nil
}

// captureLogs captures the process stderr, for the tests that build an app
// directly and point its errOut at os.Stderr. A command run through gwsRun has
// its own stderr buffer instead, and those tests read that.
func captureLogs(t *testing.T, fn func() (string, error)) (string, error) {
	t.Helper()

	old := os.Stderr

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	os.Stderr = writer

	done := make(chan string, 1)

	go func() {
		data, _ := io.ReadAll(reader)
		done <- string(data)
	}()

	_, runErr := fn()

	_ = writer.Close()

	// The captured stream is stderr, so stderr is what goes back. Restoring
	// stdout here left the process writing to a closed pipe for the rest of
	// the run and swallowed the test reporter's own output.
	os.Stderr = old

	captured := <-done

	_ = reader.Close()

	return captured, runErr
}

// TestMain pins the environment for the suite: every test runs against a temp
// home, and a default must not reach the developer's machine (host CLIs,
// launchctl/systemctl) or enable the project files of the beadle checkout
// itself. The unattended defaults themselves are per-app now (see testOptions),
// and the tests that exercise a default ask for it in their own run.
func TestMain(m *testing.M) {
	os.Exit(isolateTestHome(m))
}

// autoBinDir returns a PATH without agent CLIs, so the auto path takes the
// no-binary route and never launches a host process. git is linked in when
// present, keeping vault history quiet.
func autoBinDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	git, err := exec.LookPath("git")
	if err != nil {
		return dir
	}

	if err := os.Symlink(git, filepath.Join(dir, "git")); err != nil {
		t.Fatal(err)
	}

	return dir
}

func autoBundleHome(t *testing.T) string {
	t.Helper()

	home := gwsHome(t)

	gwsWrite(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {}}`)
	gwsWrite(t, filepath.Join(home, ".claude", "CLAUDE.md"), "# r\n")

	return home
}

func loadCliState(t *testing.T, home string) *state.State {
	t.Helper()

	st, err := state.Load(filepath.Join(home, ".beadle", "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	return st
}

// claudeStub is a minimal stand-in for the claude CLI: it validates, installs
// and lists the rendered bundle, reading the version straight from the vault
// manifest so the probe sees the version beadle just rendered.
const claudeStub = `#!/bin/sh
case "$1 $2" in
  "plugin validate")
    printf '{"success": true}'
    ;;
  "plugin list")
    manifest="${BEADLE_HOME:-$HOME/.beadle}/bundles/claude/plugins/beadle-canon/.claude-plugin/plugin.json"
    version=""
    while IFS= read -r line; do
      case "$line" in
        *'"version"'*)
          version=${line#*\"version\"}
          version=${version#*:}
          version=${version#*\"}
          version=${version%%\"*}
          break
          ;;
      esac
    done < "$manifest"
    printf '[{"id":"beadle-canon@beadle","version":"%s","enabled":true}]' "$version"
    ;;
  *)
    ;;
esac
exit 0
`

// claudeStubDir returns a PATH where claude is the stub above and git still
// works, so init can register a verified bundle without a real host.
func claudeStubDir(t *testing.T) string {
	t.Helper()

	dir := autoBinDir(t)

	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(claudeStub), 0o700); err != nil { //nolint:gosec // G306: the stub must be executable
		t.Fatal(err)
	}

	return dir
}

// initModeLine returns the "what is synchronized" line of one surface, so a
// test can assert what the table claims about a mode.
func initModeLine(t *testing.T, out, surface string) string {
	t.Helper()

	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "    "+surface) {
			return line
		}
	}

	t.Fatalf("no %s line in the init output:\n%s", surface, out)

	return ""
}

func TestInitAutoEnablesBundles(t *testing.T) {
	Convey("Given a detected claude without its CLI", t, func() {
		home := autoBundleHome(t)

		t.Setenv("PATH", autoBinDir(t))

		Convey("When init runs", func() {
			out, err := gwsRunAuto(t, "init")
			So(err, ShouldBeNil)

			Convey("Then the rendered bundle and the instruction are reported", func() {
				So(out, ShouldContainSubstring, "bundles")
				So(out, ShouldContainSubstring, "claude")
				So(out, ShouldContainSubstring, "(auto)")
				So(out, ShouldContainSubstring, "delivered")
				So(out, ShouldContainSubstring, "unverifiable")
				So(out, ShouldContainSubstring, "run: claude plugin marketplace add")

				entry := loadCliState(t, home).Bundles["claude"]
				So(entry.Enabled, ShouldBeTrue)
				So(entry.Registered, ShouldBeFalse)
				So(entry.AutoAttempt, ShouldNotBeNil)
				So(entry.AutoAttempt.Tier, ShouldEqual, state.VerifyUnverifiable)
			})

			Convey("And the file kinds stay on", func() {
				cfg, err := config.Load(filepath.Join(home, ".beadle", "config.json"))
				So(err, ShouldBeNil)
				So(cfg.ModeFor("claude", kind.Skills, config.ModeSync), ShouldEqual, config.ModeSync)
				So(cfg.ModeFor("claude", kind.MCP, config.ModeSync), ShouldEqual, config.ModeSync)
			})
		})
	})
}

func TestInitAutoEnableVerifiedFlipsModesInTable(t *testing.T) {
	Convey("Given a detected claude with a working CLI", t, func() {
		home := autoBundleHome(t)

		t.Setenv("PATH", claudeStubDir(t))

		Convey("When init runs", func() {
			out, err := gwsRunAuto(t, "init")
			So(err, ShouldBeNil)

			Convey("Then the modes table shows the bundle-managed kinds off", func() {
				So(initModeLine(t, out, "skills"), ShouldContainSubstring, "off")
				So(initModeLine(t, out, "mcp"), ShouldContainSubstring, "off")

				entry := loadCliState(t, home).Bundles["claude"]
				So(entry.VerifyTier, ShouldEqual, state.VerifyExecuted)

				cfg, err := config.Load(filepath.Join(home, ".beadle", "config.json"))
				So(err, ShouldBeNil)
				So(cfg.ModeFor("claude", kind.Skills, config.ModeSync), ShouldEqual, config.ModeOff)
				So(cfg.ModeFor("claude", kind.MCP, config.ModeSync), ShouldEqual, config.ModeOff)
			})
		})
	})
}

func TestWatchSyncLogsBundleLines(t *testing.T) {
	Convey("Given a sync report with a failed unattended bundle attempt", t, func() {
		report := &engine.Report{Bundles: []engine.BundleResult{{
			Host: "claude", Action: "failed", Tier: state.VerifyFailed, Auto: true,
			Note: "run beadle bundles enable claude",
		}}}

		Convey("When the background sync logs the report", func() {
			logs, err := captureLogs(t, func() (string, error) {
				// The app is built inside the capture: the logger binds the
				// stream it writes to when it is created, and beadle's
				// background lines travel on stderr.
				a := &app{logger: embedlog.NewLogger(false, false), errOut: os.Stderr}

				a.logSyncReport(t.Context(), report)

				return "", nil
			})
			So(err, ShouldBeNil)

			Convey("Then the retry command reaches the daemon log", func() {
				So(logs, ShouldContainSubstring, "bundle: claude")
				So(logs, ShouldContainSubstring, "(auto)")
				So(logs, ShouldContainSubstring, "run beadle bundles enable claude")
			})
		})
	})
}

func TestBundlesDisablePositionalHostOptsOut(t *testing.T) {
	Convey("Given a detected claude without a bundle entry", t, func() {
		home := autoBundleHome(t)

		t.Setenv("PATH", autoBinDir(t))

		if _, err := gwsRun(t, "init"); err != nil {
			t.Fatal(err)
		}

		Convey("When disable runs with a positional host", func() {
			out, err := gwsRun(t, "bundles", "disable", "claude")
			So(err, ShouldBeNil)

			Convey("Then the host is opted out and the next sync leaves it alone", func() {
				So(out, ShouldContainSubstring, "no enabled bundle")
				So(loadCliState(t, home).BundleOptedOut("claude"), ShouldBeTrue)

				// The scenario is clean: nothing is in conflict, so the run
				// succeeds. What is asserted here is that no bundle was
				// attempted, not what the run did with a conflict.
				syncOut, err := gwsRun(t, "sync")
				So(err, ShouldBeNil)
				So(syncOut, ShouldNotContainSubstring, "(auto)")
				So(loadCliState(t, home).Bundles, ShouldBeEmpty)
			})

			Convey("And enable with a positional host clears the opt-out", func() {
				_, err := gwsRun(t, "bundles", "enable", "claude")
				So(err, ShouldBeNil)
				So(loadCliState(t, home).BundleOptedOut("claude"), ShouldBeFalse)
			})
		})

		Convey("When the --host form is used", func() {
			out, err := gwsRun(t, "bundles", "disable", "--host", "gemini")
			So(err, ShouldBeNil)
			So(out, ShouldContainSubstring, "no enabled bundle")
		})

		Convey("When the positional host and --host disagree", func() {
			_, err := gwsRun(t, "bundles", "disable", "gemini", "--host", "claude")

			Convey("Then it is rejected", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "does not match")
			})
		})
	})
}

func TestSyncAutoEnablesBundlesOnce(t *testing.T) {
	Convey("Given an initialized vault with an untouched detected claude", t, func() {
		home := autoBundleHome(t)

		t.Setenv("PATH", autoBinDir(t))

		// "Untouched" is the premise: the init must not attempt the bundle, or
		// the sync under test would find the attempt already recorded and have
		// nothing to say.
		if _, err := gwsRun(t, "init"); err != nil {
			t.Fatal(err)
		}

		Convey("When a full sync runs", func() {
			out, err := gwsRunAuto(t, "sync")
			So(err, ShouldBeNil)

			Convey("Then the untouched host is attempted once", func() {
				So(out, ShouldContainSubstring, "(auto)")
				So(out, ShouldContainSubstring, "unverifiable")

				entry := loadCliState(t, home).Bundles["claude"]
				So(entry.AutoAttempt, ShouldNotBeNil)
				So(entry.AutoAttempt.Tier, ShouldEqual, state.VerifyUnverifiable)

				Convey("And the next sync does not attempt again", func() {
					again, err := gwsRunAuto(t, "sync")
					So(err, ShouldBeNil)
					So(again, ShouldNotContainSubstring, "(auto)")
				})
			})
		})
	})
}
