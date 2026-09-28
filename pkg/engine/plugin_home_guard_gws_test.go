package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// foreignHomeVault syncs one Claude plugin into a vault, then makes the vault
// belong to another machine and removes the plugin from this home's
// registries: the readers see nothing, which is what a run with a foreign
// HOME looks like.
func foreignHomeVault(t *testing.T) (*fixture, string) {
	t.Helper()

	f := newFixture(t)
	f.emptyConfigs(t)
	enableAgents(t, f, agent.ClaudeCodeID)

	dir := pluginTree(t, f.home, "caveman", "caveman", "1.0.0")
	writeSkill(t, dir, "alpha", "# alpha\n")

	f.sync(t)

	const foreign = "/somewhere/else"

	st, err := state.Load(f.vault.StatePath())
	So(err, ShouldBeNil)

	st.Home = foreign
	So(st.Save(f.vault.StatePath()), ShouldBeNil)

	So(os.RemoveAll(filepath.Join(f.home, ".claude", "plugins")), ShouldBeNil)

	return f, foreign
}

func TestPluginRetireSkippedOnAForeignHome(t *testing.T) {
	Convey("Given a vault recorded as belonging to another home", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f, foreign := foreignHomeVault(t)
		pivot := filepath.Join(f.vault.PluginsDir(), "caveman", "caveman")

		report := f.sync(t)

		Convey("Then no plugin is retired, the pivot stays and the warning explains it", func() {
			warnings := strings.Join(report.Warnings, "\n")
			So(warnings, ShouldContainSubstring, "was synced from "+foreign)
			So(warnings, ShouldContainSubstring, "BEADLE_ALLOW_HOME_MOVE")

			_, err := os.Stat(pivot)
			So(err, ShouldBeNil)

			// The plugins are still presented: nothing was withdrawn either.
			for _, result := range report.Plugins {
				So(result.Action, ShouldNotEqual, "retired")
			}
		})
	})
}

func TestPluginRetireRunsOnTheRecordedHome(t *testing.T) {
	Convey("Given the vault belongs to the home that is running", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		enableAgents(t, f, agent.ClaudeCodeID)

		dir := pluginTree(t, f.home, "caveman", "caveman", "1.0.0")
		writeSkill(t, dir, "alpha", "# alpha\n")

		f.sync(t)
		So(os.RemoveAll(filepath.Join(f.home, ".claude", "plugins")), ShouldBeNil)

		report := f.sync(t)

		Convey("Then the plugin really is gone and is retired as before", func() {
			So(strings.Join(report.Warnings, "\n"), ShouldNotContainSubstring, "BEADLE_ALLOW_HOME_MOVE")

			_, err := os.Stat(filepath.Join(f.vault.PluginsDir(), "caveman", "caveman"))
			So(os.IsNotExist(err), ShouldBeTrue)
		})
	})
}

func TestPluginRetireOnAForeignHomeWithTheOptIn(t *testing.T) {
	Convey("Given the user accepted the home move", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("BEADLE_ALLOW_HOME_MOVE", "1")

		f, _ := foreignHomeVault(t)
		pivot := filepath.Join(f.vault.PluginsDir(), "caveman", "caveman")

		report := f.sync(t)

		Convey("Then the retirement runs again", func() {
			So(strings.Join(report.Warnings, "\n"), ShouldNotContainSubstring, "BEADLE_ALLOW_HOME_MOVE=1 to adopt")

			_, err := os.Stat(pivot)
			So(os.IsNotExist(err), ShouldBeTrue)
		})
	})
}

// orphanPivotDir plants an orphan pivot: a symlink-only directory under the
// vault whose plugin key the ledger never recorded.
func orphanPivotDir(t *testing.T, f *fixture) string {
	t.Helper()

	dir := filepath.Join(f.vault.PluginsDir(), "orphan", "gone")
	So(os.MkdirAll(dir, 0o750), ShouldBeNil)

	target := filepath.Join(t.TempDir(), "cache", "skills", "alpha")
	So(os.MkdirAll(target, 0o750), ShouldBeNil)
	So(os.Symlink(target, filepath.Join(dir, "current")), ShouldBeNil)

	return dir
}

func TestOrphanPivotKeptOnAForeignHome(t *testing.T) {
	Convey("Given an orphan pivot in a vault that belongs to another home", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		dir := orphanPivotDir(t, f)

		st, err := state.Load(f.vault.StatePath())
		So(err, ShouldBeNil)

		st.Home = "/somewhere/else"
		So(st.Save(f.vault.StatePath()), ShouldBeNil)

		report := f.sync(t)

		Convey("Then the pivot survives and the warning names every skipped pass", func() {
			_, err := os.Stat(dir)
			So(err, ShouldBeNil)

			warnings := strings.Join(report.Warnings, "\n")
			So(warnings, ShouldContainSubstring, "orphan pivot removal")
			So(warnings, ShouldContainSubstring, "stale link cleanup")
			So(warnings, ShouldContainSubstring, "plugin retirement")
		})
	})
}

func TestOrphanPivotRetiredOnTheRecordedHome(t *testing.T) {
	Convey("Given an orphan pivot in a vault that belongs to this home", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		dir := orphanPivotDir(t, f)

		report := f.sync(t)

		Convey("Then it is retired exactly as before", func() {
			_, err := os.Stat(dir)
			So(os.IsNotExist(err), ShouldBeTrue)
			So(strings.Join(report.Warnings, "\n"), ShouldNotContainSubstring, "BEADLE_ALLOW_HOME_MOVE")
		})
	})
}

func TestOrphanPivotRetiredWithTheOptInAndTheHomeIsAdopted(t *testing.T) {
	Convey("Given the user accepted the home move", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("BEADLE_ALLOW_HOME_MOVE", "1")

		f := newFixture(t)
		f.emptyConfigs(t)

		dir := orphanPivotDir(t, f)

		st, err := state.Load(f.vault.StatePath())
		So(err, ShouldBeNil)

		st.Home = "/somewhere/else"
		So(st.Save(f.vault.StatePath()), ShouldBeNil)

		f.sync(t)

		Convey("Then the pivot is retired and the vault records the new home", func() {
			_, err := os.Stat(dir)
			So(os.IsNotExist(err), ShouldBeTrue)

			after, err := state.Load(f.vault.StatePath())
			So(err, ShouldBeNil)
			So(after.Home, ShouldEqual, f.home)
		})
	})
}
