package cli

import (
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// kindsConfig reads back the config the command saved, so the assertion is on
// what a later run will see rather than on what the command printed.
func kindsConfig(t *testing.T, home string) *config.Config {
	t.Helper()

	cfg, err := config.Load(filepath.Join(home, ".beadle", "config.json"))
	if err != nil {
		t.Fatal(err)
	}

	return cfg
}

// TestKindsToggleAgentFlag covers FLAG-PARITY-1 §Decisions 7: `kinds
// enable`/`disable` take a repeatable --agent, so one run can narrow the
// change to the agents named and leave every other agent as it was.
//
// The assertion is on the configuration that is saved, not on the sentence
// the command prints: a switch that says "saved" and writes a global
// default is exactly the bug this flag exists to prevent.
func TestKindsToggleAgentFlag(t *testing.T) {
	Convey("Given an initialised vault", t, func() {
		// gwsHome hands out a fresh directory on every call, so the home the
		// config is read back from has to be the one the commands ran in.
		home := gwsHome(t)

		if _, err := gwsRun(t, "init"); err != nil {
			t.Fatal(err)
		}

		Convey("When kinds disable is given one --agent", func() {
			_, err := gwsRun(t, "kinds", "disable", string(kind.Skills), "--agent", "claude")
			So(err, ShouldBeNil)

			Convey("Then that agent's mode is off", func() {
				cfg := kindsConfig(t, home)
				So(cfg.ModeFor("claude", kind.Skills, config.ModeSync), ShouldEqual, config.ModeOff)
			})

			Convey("And the global default is untouched", func() {
				cfg := kindsConfig(t, home)
				So(cfg.KindEnabled(kind.Skills), ShouldBeTrue)
			})

			Convey("And another agent keeps its own mode", func() {
				cfg := kindsConfig(t, home)
				So(cfg.ModeFor("gemini", kind.Skills, config.ModeSync), ShouldEqual, config.ModeSync)
			})
		})

		Convey("When the flag is repeated", func() {
			_, err := gwsRun(t, "kinds", "disable", string(kind.Skills), "--agent", "claude", "--agent", "gemini")
			So(err, ShouldBeNil)

			Convey("Then every named agent is off", func() {
				cfg := kindsConfig(t, home)
				So(cfg.ModeFor("claude", kind.Skills, config.ModeSync), ShouldEqual, config.ModeOff)
				So(cfg.ModeFor("gemini", kind.Skills, config.ModeSync), ShouldEqual, config.ModeOff)
			})
		})

		Convey("When kinds enable is given one --agent", func() {
			_, err := gwsRun(t, "kinds", "disable", string(kind.Skills))
			So(err, ShouldBeNil)

			_, err = gwsRun(t, "kinds", "enable", string(kind.Skills), "--agent", "claude")
			So(err, ShouldBeNil)

			Convey("Then only that agent is on again", func() {
				cfg := kindsConfig(t, home)
				So(cfg.ModeFor("claude", kind.Skills, config.ModeSync), ShouldEqual, config.ModeSync)
				So(cfg.KindEnabled(kind.Skills), ShouldBeFalse)
			})
		})

		Convey("When the flag is left off", func() {
			_, err := gwsRun(t, "kinds", "disable", string(kind.Skills))
			So(err, ShouldBeNil)

			Convey("Then the global default is what changed", func() {
				cfg := kindsConfig(t, home)
				So(cfg.KindEnabled(kind.Skills), ShouldBeFalse)
			})
		})

		Convey("When the agent is not one beadle knows", func() {
			_, err := gwsRun(t, "kinds", "disable", string(kind.Skills), "--agent", "nosuchagent")

			Convey("Then it is refused, and says which agents exist", func() {
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, "nosuchagent")
			})
		})
	})
}

// TestKindsToggleFlagIsRepeatable pins the shape of the flag itself: a
// StringVar would take the last occurrence and quietly drop the rest.
func TestKindsToggleFlagIsRepeatable(t *testing.T) {
	Convey("Given the kinds enable command", t, func() {
		a := &app{errOut: discardWriter{}}
		root := newRootCmdWithApp(a, Options{Version: "test"})

		kinds, _, err := root.Find([]string{"kinds", "enable"})
		So(err, ShouldBeNil)

		Convey("Then --agent is advertised as repeatable", func() {
			flag := kinds.Flags().Lookup("agent")
			So(flag, ShouldNotBeNil)
			So(flag.Value.Type(), ShouldEqual, "stringArray")
		})

		Convey("And both verbs carry it", func() {
			disable, _, err := root.Find([]string{"kinds", "disable"})
			So(err, ShouldBeNil)
			So(disable.Flags().Lookup("agent"), ShouldNotBeNil)
		})

		Convey("And the help text says it can be given more than once", func() {
			help := kinds.Flags().Lookup("agent").Usage
			So(strings.ToLower(help), ShouldContainSubstring, "repeat")
		})
	})
}
