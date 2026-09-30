package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// TestInitReportsTheEnabledAgentThatVanished pins the one path in `init` that
// a nil guard used to sit in front of.
//
// The guard read `if enabled != nil`, which is always true: `enabled` is a map
// literal, never nil. So the block ran on every run, including a first one
// where the map is empty. That is harmless on a first run, and it is the
// reason the check can be deleted without changing what any run prints — which
// is exactly what this test is here to keep true, because "harmless" is the
// kind of claim that stops being harmless the moment somebody reads it as
// "this only happens when a vault already exists".
func TestInitReportsTheEnabledAgentThatVanished(t *testing.T) {
	Convey("Given a vault that enabled an agent", t, func() {
		home := gwsHome(t)

		if _, err := gwsRun(t, "init"); err != nil {
			t.Fatal(err)
		}

		if _, err := gwsRun(t, "agents", "enable", "claude"); err != nil {
			t.Fatal(err)
		}

		Convey("When the agent is no longer on the machine", func() {
			// Remove what the detector looks for, so the host is not found.
			if err := os.RemoveAll(filepath.Join(home, ".claude")); err != nil {
				t.Fatal(err)
			}

			out, err := gwsRun(t, "init", "--yes")
			So(err, ShouldBeNil)

			Convey("Then init says it is still enabled and how to stop it", func() {
				So(out, ShouldContainSubstring, "enabled but no longer on this machine")
				So(out, ShouldContainSubstring, "beadle agents disable")
			})
		})

		Convey("When init runs on a home that was never initialised", func() {
			// A second gwsHome: by the time this leaf runs, the outer body's
			// home already has claude enabled, and an enabled agent the
			// detector cannot see is branch one. A first run is a different
			// thing and needs its own home to still be one.
			gwsHome(t)

			out, err := gwsRun(t, "init")
			So(err, ShouldBeNil)
			Convey("Then nothing is reported as gone", func() {
				// The loop runs here too — the nil guard never kept it quiet,
				// the empty map did. This is the case that breaks if the
				// block is ever "fixed" into an unconditional report.
				So(strings.Count(out, "no longer on this machine"), ShouldEqual, 0)
			})
		})
	})
}
