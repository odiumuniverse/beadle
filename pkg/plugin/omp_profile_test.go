package plugin_test

import (
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/plugin"
)

// TestOmpRootFollowsProfile pins the one resolver: the plugin state moves with
// PI_CONFIG_DIR and with a named profile, exactly as the agent surfaces do.
// Live-verified on 18.4.1: with OMP_PROFILE=work omp reads
// ~/.omp/profiles/work/plugins/installed_plugins.json, and PI_CODING_AGENT_DIR
// does not move the plugin tree at all.
func TestOmpRootFollowsProfile(t *testing.T) {
	Convey("Given omp's default root", t, func() {
		home := t.TempDir()

		t.Setenv("PI_CONFIG_DIR", "")
		t.Setenv("OMP_PROFILE", "")
		t.Setenv("PI_PROFILE", "")

		Convey("Then the plugin state lives directly under ~/.omp", func() {
			So(plugin.OmpRoot(home), ShouldEqual, filepath.Join(home, ".omp"))
		})
	})

	Convey("Given PI_CONFIG_DIR names another root", t, func() {
		home := t.TempDir()

		t.Setenv("PI_CONFIG_DIR", ".omp-alt")
		t.Setenv("OMP_PROFILE", "")
		t.Setenv("PI_PROFILE", "")

		Convey("Then the plugin state follows it", func() {
			So(plugin.OmpRoot(home), ShouldEqual, filepath.Join(home, ".omp-alt"))
		})
	})

	Convey("Given a named profile", t, func() {
		home := t.TempDir()

		t.Setenv("PI_CONFIG_DIR", "")
		t.Setenv("OMP_PROFILE", "work")
		t.Setenv("PI_PROFILE", "")

		Convey("Then the plugin state moves next to the profile agent dir", func() {
			So(plugin.OmpRoot(home), ShouldEqual, filepath.Join(home, ".omp", "profiles", "work"))
			// The same root the profile's agent dir hangs off.
			So(plugin.OmpRoot(home), ShouldEqual, filepath.Dir(
				filepath.Join(home, ".omp", "profiles", "work", "agent")))
		})
	})
}
