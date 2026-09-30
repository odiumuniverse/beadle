package agent_test

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
)

// The onboarding screen has to say *which* half is missing, because "claude is
// not on PATH" and "no config at ~/.claude" are different problems with
// different fixes (W7-UX §5.3).
func TestDetectionNamesTheMissingHalf(t *testing.T) {
	Convey("Given a detector that finds the agent", t, func() {
		a := &agent.Agent{
			ID:     "x",
			Name:   "X",
			Detect: func() (bool, error) { return true, nil },
		}

		Convey("Then it is found, and the line says detected", func() {
			d := agent.DetectReasonOf(a, t.TempDir(), "")
			So(d.Found, ShouldBeTrue)
			So(d.Line(), ShouldEqual, "detected")
		})
	})

	Convey("Given a detector that finds nothing and a config path", t, func() {
		a := &agent.Agent{
			ID:     "x",
			Name:   "X",
			Detect: func() (bool, error) { return false, nil },
		}

		Convey("Then the reason names the missing config, not the CLI", func() {
			// A host with a config directory but no CLI is *present*; naming
			// the wrong half sends a user to install what they have.
			d := agent.DetectReasonOf(a, t.TempDir(), "/home/u/.x")
			So(d.Found, ShouldBeFalse)
			So(d.Line(), ShouldContainSubstring, "no config at /home/u/.x")
		})
	})

	Convey("Given a detector that finds nothing and known binaries", t, func() {
		a := &agent.Agent{
			ID:       "x",
			Name:     "X",
			Detect:   func() (bool, error) { return false, nil },
			Binaries: []string{"definitely-not-a-real-binary-xyz"},
		}

		Convey("Then the reason names the CLI that is not on PATH", func() {
			d := agent.DetectReasonOf(a, t.TempDir(), "")
			So(d.Found, ShouldBeFalse)
			So(d.Line(), ShouldContainSubstring, "is not on PATH")
		})
	})

	Convey("Given an agent with its own DetectReason", t, func() {
		a := &agent.Agent{
			ID:     "x",
			Name:   "X",
			Detect: func() (bool, error) { return false, nil },
			DetectReason: func() (agent.Detection, error) {
				return agent.Detection{Found: true, Reason: "seen by the probe"}, nil
			},
		}

		Convey("Then that reason wins", func() {
			// An agent that knows more about itself than Detect does should be
			// believed; Detect is what the rest of beadle calls, this is what
			// the screen shows.
			d := agent.DetectReasonOf(a, t.TempDir(), "")
			So(d.Found, ShouldBeTrue)
			So(d.Line(), ShouldEqual, "seen by the probe")
		})
	})

	Convey("Given an agent with no detector at all", t, func() {
		Convey("Then it says so rather than panicking", func() {
			d := agent.DetectReasonOf(&agent.Agent{ID: "x", Name: "X"}, t.TempDir(), "")
			So(d.Found, ShouldBeFalse)
			So(d.Line(), ShouldNotBeBlank)
		})
	})

	Convey("Given nil", t, func() {
		Convey("Then it says so rather than panicking", func() {
			d := agent.DetectReasonOf(nil, t.TempDir(), "")
			So(d.Found, ShouldBeFalse)
			So(d.Line(), ShouldNotBeBlank)
		})
	})
}
