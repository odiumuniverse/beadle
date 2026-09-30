package cli

import (
	"slices"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/spf13/pflag"
)

// One flag set, one meaning. beadle's plugin commands carry the same switches
// verger's install/sync/remove carry, with the same words, so a script written
// for one tool runs against the other. The test names every flag each command
// must answer for, and then exercises the ones that narrow a run.
func TestPluginCommandsCarryTheSharedFlags(t *testing.T) {
	// --force is here because §Decisions 1 gave plugins install and remove a
	// way to overwrite what the user edited; the list is the shared surface
	// of the tree, so a new flag on both verbs belongs in it.
	want := []string{"dry-run", "except", "force", "hosts", "json", "project", "yes"}

	Convey("Given the plugins command tree", t, func() {
		a := &app{errOut: discardWriter{}}
		root := newRootCmdWithApp(a, Options{Version: "test"})

		plugins, _, err := root.Find([]string{"plugins"})
		So(err, ShouldBeNil)
		So(plugins.Name(), ShouldEqual, "plugins")

		for _, name := range []string{"install", "remove"} {
			Convey("When `beadle plugins "+name+"` is asked for its flags", func() {
				cmd, _, err := root.Find([]string{"plugins", name})
				So(err, ShouldBeNil)

				names := []string{}

				cmd.Flags().VisitAll(func(f *pflag.Flag) {
					names = append(names, f.Name)
				})

				slices.Sort(names)
				So(names, ShouldResemble, want)
			})
		}

		Convey("Then the host names the switches take are the ones beadle prints", func() {
			So(hostNames(), ShouldContain, "claude")
			So(hostNames(), ShouldNotContain, "claude-code")
		})
	})
}

// hostNames exists to keep the host vocabulary honest: the two switches take
// the same names the agent table prints.
func hostNames() []string {
	names := []string{}
	for _, h := range bundle.Hosts() {
		names = append(names, string(h))
	}

	slices.Sort(names)

	return names
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
