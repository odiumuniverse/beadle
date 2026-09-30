package cli

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/spf13/cobra"
)

// TestPluginsSubcommandsAreReachable is the test the previous round was
// missing: the functions existed, no command reached them. It walks the
// command tree a user types.
func TestPluginsSubcommandsAreReachable(t *testing.T) {
	Convey("Given the plugins command tree", t, func() {
		root := newRootCmd(testOptions())

		var plugins *cobra.Command

		for _, cmd := range root.Commands() {
			if cmd.Name() == "plugins" {
				plugins = cmd
			}
		}

		So(plugins, ShouldNotBeNil)

		names := []string{}
		for _, cmd := range plugins.Commands() {
			names = append(names, cmd.Name())
		}

		Convey("Then every documented subcommand is there", func() {
			So(names, ShouldContain, "list")
			So(names, ShouldContain, "install")
			So(names, ShouldContain, "remove")
			So(names, ShouldContain, "eject")
			So(names, ShouldContain, "pins")
			So(names, ShouldContain, "pin")
			So(names, ShouldContain, "unpin")
		})
	})
}
