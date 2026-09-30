package engine_test

import (
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/engine"
)

// The plugin library reconciles the machine with the vault's spec and lock, and
// it must do that *after* the kinds have pulled, adopted and rendered. Run
// earlier, it answers about a vault state the sync had not reached yet: a run
// that delivered a package still reported "the vault carries no package spec",
// because the report described the pass before the delivery.
func TestThePluginLibraryReconcilesAfterTheKinds(t *testing.T) {
	Convey("Given a sync that pulls a host skill into the canon", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		// The canon skill the sync writes: its existence is proof the skills
		// kind already ran.
		canonMarker := filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md")
		f.manager.applyAfter = canonMarker

		write(t, filepath.Join(openCodeSkillsDir(f.home), "alpha", "SKILL.md"), "# alpha\n")

		Convey("When sync runs", func() {
			f.sync(t)

			Convey("Then the library was asked once, after the kinds had written the canon", func() {
				So(f.manager.applyCalls, ShouldEqual, 1)
				So(f.manager.appliedAfterKinds, ShouldBeTrue)
			})
		})
	})
}

// A dry run must not touch the library's write path, and the report must still
// carry a section for the spec - "nothing to do" is a fact worth printing.
func TestADryRunStillReportsThePluginSection(t *testing.T) {
	Convey("Given a sync in dry-run mode", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		Convey("When the dry run finishes", func() {
			report := f.run(t, engine.SyncOptions{DryRun: true})

			Convey("Then the library was asked in dry-run mode and the report has the section", func() {
				So(f.manager.applyCalls, ShouldEqual, 1)
				So(report.Packages, ShouldNotBeNil)
			})
		})
	})
}
