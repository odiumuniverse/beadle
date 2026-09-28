package engine_test

import (
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// foreignSource is the sourceUri of a marketplace the user registered under
// beadle's own marketplace name.
const foreignSource = "/tmp/not-beadles-catalog"

// cliRan reports whether any recorded CLI call carried the substring.
func cliRan(calls [][]string, substring string) bool {
	for _, call := range calls {
		if strings.Contains(strings.Join(call, " "), substring) {
			return true
		}
	}

	return false
}

func TestOmpBundleEnableLeavesAForeignMarketplaceAlone(t *testing.T) {
	Convey("Given omp has a marketplace named beadle that points elsewhere", t, func() {
		f := ompBundleFixture(t)

		host := &ompHostState{Marketplace: true, SourceURI: foreignSource}
		host.writeState(t, f.home)

		cli := ompBundleCLI(t, f, host)
		fakeRunner(t, cli)
		foundCLI(t)

		report, err := f.engine.BundlesEnable(t.Context(), "omp")

		Convey("When the bundle is enabled", func() {
			Convey("Then no install runs from it, the conflict is reported and the entry is untouched", func() {
				So(err, ShouldBeNil)
				So(cliRan(cli.calls, "install"), ShouldBeFalse)
				So(cliRan(cli.calls, "marketplace update"), ShouldBeFalse)
				So(cliRan(cli.calls, "marketplace add"), ShouldBeFalse)

				warnings := strings.Join(report.Warnings, "\n")
				So(warnings, ShouldContainSubstring, "already has a marketplace named \"beadle\"")
				So(warnings, ShouldContainSubstring, foreignSource)
				So(warnings, ShouldContainSubstring, "left untouched")

				So(report.Bundles, ShouldHaveLength, 1)
				// The registration was refused, so the host keeps the file
				// copies and the bundle is not marked registered; the note
				// names the conflict the user has to settle.
				So(report.Bundles[0].Action, ShouldEqual, "failed")
				So(report.Bundles[0].Registered, ShouldBeFalse)
				So(report.Bundles[0].Note, ShouldContainSubstring, "already has a marketplace named \"beadle\"")

				// The user's own entry is still there, still pointing where
				// they pointed it.
				state := read(t, filepath.Join(ompHomeDir(f.home), "marketplaces.json"))
				So(state, ShouldContainSubstring, foreignSource)
			})
		})
	})
}

func TestOmpBundleDisableLeavesAForeignMarketplaceAlone(t *testing.T) {
	Convey("Given a bundle beadle registered whose marketplace the user re-pointed", t, func() {
		f := ompBundleFixture(t)

		host := &ompHostState{}
		cli := ompBundleCLI(t, f, host)
		fakeRunner(t, cli)
		foundCLI(t)

		_, err := f.engine.BundlesEnable(t.Context(), "omp")
		So(err, ShouldBeNil)

		// The user replaced the entry with one of their own, under the same
		// name: what omp will serve is no longer beadle's.
		host.SourceURI = foreignSource
		host.Version = "9.9.9"
		host.writeState(t, f.home)

		cli.calls = nil

		report, err := f.engine.BundlesDisable(t.Context(), "omp")

		Convey("When the bundle is disabled", func() {
			Convey("Then nothing of the user's is removed and the conflict is reported", func() {
				So(err, ShouldBeNil)
				So(cliRan(cli.calls, "uninstall"), ShouldBeFalse)
				So(cliRan(cli.calls, "marketplace remove"), ShouldBeFalse)

				warnings := strings.Join(report.Warnings, "\n")
				So(warnings, ShouldContainSubstring, "nothing of beadle's was removed")

				state := read(t, filepath.Join(ompHomeDir(f.home), "marketplaces.json"))
				So(state, ShouldContainSubstring, foreignSource)
			})
		})
	})
}

func TestOmpBundleOwnMarketplaceStillWorks(t *testing.T) {
	Convey("Given omp has beadle's own marketplace and plugin registered", t, func() {
		f := ompBundleFixture(t)

		host := &ompHostState{}
		cli := ompBundleCLI(t, f, host)
		fakeRunner(t, cli)
		foundCLI(t)

		_, err := f.engine.BundlesEnable(t.Context(), "omp")
		So(err, ShouldBeNil)

		Convey("When it is enabled again", func() {
			cli.calls = nil

			report, err := f.engine.BundlesEnable(t.Context(), "omp")

			Convey("Then beadle recognizes its own entry and does not touch the host", func() {
				So(err, ShouldBeNil)
				// Ownership is what makes the second enable a noop instead of
				// a conflict: the recorded source is beadle's own catalog.
				So(cliRan(cli.calls, "install"), ShouldBeFalse)
				So(cliRan(cli.calls, "marketplace"), ShouldBeFalse)
				So(report.Bundles, ShouldHaveLength, 1)
				So(report.Bundles[0].Action, ShouldEqual, "noop")
				So(strings.Join(report.Warnings, "\n"), ShouldNotContainSubstring, "left untouched")
			})
		})

		Convey("And when it is disabled", func() {
			cli.calls = nil

			report, err := f.engine.BundlesDisable(t.Context(), "omp")

			Convey("Then the plugin and beadle's own marketplace are removed", func() {
				So(err, ShouldBeNil)
				So(cliRan(cli.calls, "uninstall beadle-canon@beadle"), ShouldBeTrue)
				So(cliRan(cli.calls, "marketplace remove beadle"), ShouldBeTrue)
				So(strings.Join(report.Warnings, "\n"), ShouldNotContainSubstring, "left untouched")
			})
		})
	})
}
