package engine_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/hostcli"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// installedClaude writes an executable claude into a fresh directory and
// makes only that binary visible on the process PATH lookup.
func installedClaude(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "claude")

	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatalf("write claude: %v", err)
	}

	if err := os.Chmod(path, 0o755); err != nil { //nolint:gosec // G302: the test needs an executable stand-in for the host CLI
		t.Fatalf("chmod claude: %v", err)
	}

	restore := engine.SetBundlesLookPathForTest(func(name string) (string, error) {
		if name == "claude" {
			return path, nil
		}

		return "", errors.New(name + ": not found")
	})
	t.Cleanup(restore)

	return path
}

// unattendedEngine builds the watcher's engine over the fixture's vault.
func unattendedEngine(t *testing.T, f *fixture) *engine.Engine {
	t.Helper()

	e, err := engine.New(f.vault, f.config, agent.All(f.home, t.TempDir()), engine.WithHome(f.home), engine.WithUnattended())
	if err != nil {
		t.Fatalf("new unattended engine: %v", err)
	}

	return e
}

func TestHostCLIRecordedForTheWatcher(t *testing.T) {
	Convey("Given a claude bundle an attended run enabled with claude on its PATH", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		claude := installedClaude(t)

		cli, _ := claudeBundleCLI(t, f)
		fakeRunner(t, cli)

		_, err := f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

		records, err := hostcli.LoadRecords(f.vault.HostCLIPath())
		So(err, ShouldBeNil)

		Convey("When the enable finishes", func() {
			Convey("Then the claude location and this run's PATH are recorded", func() {
				So(records["claude"].Path, ShouldEqual, claude)
				So(records["claude"].PATH, ShouldEqual, os.Getenv("PATH"))
			})
		})

		Convey("When the canon changes and the watcher syncs without claude on its PATH", func() {
			cli.calls, cli.bins = nil, nil

			missingCLI(t)
			t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
			write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha v2\n")

			watcher := unattendedEngine(t, f)

			_, err := watcher.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			entry := loadState(t, f).Bundles["claude"]
			after, err := hostcli.LoadRecords(f.vault.HostCLIPath())
			So(err, ShouldBeNil)

			Convey("Then it runs the recorded binary with the recorded PATH and the host takes the new version", func() {
				So(cli.calls, ShouldHaveLength, 5)
				So(cli.bins[0].Source, ShouldEqual, hostcli.SourceRecorded)
				So(cli.bins[0].Path, ShouldEqual, claude)
				So(cli.bins[0].PATH, ShouldEqual, records["claude"].PATH)
				So(entry.Version, ShouldEqual, renderedClaudeVersion(t, f))
				So(entry.VerifyTier, ShouldEqual, state.VerifyExecuted)
				So(entry.Pending, ShouldBeNil)
			})

			Convey("Then the watcher never rewrites the record with its own PATH", func() {
				So(after, ShouldResemble, records)
			})
		})

		Convey("When the recorded binary is gone and the watcher syncs a change", func() {
			cli.calls = nil

			So(os.Remove(claude), ShouldBeNil)
			missingCLI(t)
			write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha v2\n")

			watcher := unattendedEngine(t, f)

			_, err := watcher.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			entry := loadState(t, f).Bundles["claude"]

			Convey("Then the refresh waits instead of failing", func() {
				So(cli.calls, ShouldBeEmpty)
				So(entry.VerifyTier, ShouldEqual, state.VerifyExecuted)
				So(entry.Pending, ShouldNotBeNil)
			})
		})
	})
}

func TestHostCLIDryRunRecordsNothing(t *testing.T) {
	Convey("Given claude on the PATH of an attended dry run", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		installedClaude(t)

		Convey("When the dry run syncs", func() {
			f.run(t, engine.SyncOptions{DryRun: true})

			_, err := os.Stat(f.vault.HostCLIPath())

			Convey("Then nothing is recorded", func() {
				So(errors.Is(err, os.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}
