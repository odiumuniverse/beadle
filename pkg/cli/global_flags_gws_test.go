package cli

import (
	"encoding/json"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// TestJSONIsGlobal covers FLAG-PARITY-1 §Decisions 2: --json is a persistent
// root flag, so it works before the subcommand as well as after it.
//
// The case that matters is the one a persistent flag silently loses. A command
// that declares its own --json shadows the root's, so `beadle --json status`
// parses the flag on the root and then reads the command's own default - the
// document would simply be missing, with nothing to say why.
func TestJSONIsGlobal(t *testing.T) {
	Convey("Given an initialised vault", t, func() {
		if _, err := gwsRun(t, "init"); err != nil {
			t.Fatal(err)
		}

		Convey("Then --json after the subcommand still works", func() {
			_, err := gwsRun(t, "status", "--json")
			So(err, ShouldBeNil)
		})

		Convey("And --json before the subcommand works too", func() {
			out, err := gwsRun(t, "--json", "status")
			So(err, ShouldBeNil)

			var doc map[string]any

			So(json.Unmarshal([]byte(out), &doc), ShouldBeNil)
			So(doc["schema"], ShouldNotBeNil)
		})

		Convey("And the two orders produce the same document", func() {
			after, err := gwsRun(t, "status", "--json")
			So(err, ShouldBeNil)

			before, err := gwsRun(t, "--json", "status")
			So(err, ShouldBeNil)

			So(before, ShouldEqual, after)
		})

		Convey("And it reaches a command other than status", func() {
			out, err := gwsRun(t, "--json", "doctor")
			So(err, ShouldBeNil)

			var doc map[string]any

			So(json.Unmarshal([]byte(out), &doc), ShouldBeNil)
			So(doc["schema"], ShouldNotBeNil)
		})
	})
}

// TestYesIsGlobal covers FLAG-PARITY-1 §Decisions 4: -y is a persistent root
// flag too, and it carries the meaning it has on each command rather than a
// meaning of its own.
func TestYesIsGlobal(t *testing.T) {
	Convey("Given an initialised vault", t, func() {
		if _, err := gwsRun(t, "init"); err != nil {
			t.Fatal(err)
		}

		Convey("Then -y before the subcommand is accepted where the verb asks", func() {
			_, err := gwsRun(t, "-y", "doctor")
			So(err, ShouldBeNil)
		})

		Convey("And --yes before the subcommand is the same flag", func() {
			_, err := gwsRun(t, "--yes", "doctor")
			So(err, ShouldBeNil)
		})

		Convey("And -y after the subcommand still works", func() {
			_, err := gwsRun(t, "doctor", "-y")
			So(err, ShouldBeNil)
		})
	})
}
