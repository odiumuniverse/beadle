package vergerx_test

import (
	"context"
	"os"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"github.com/vmkteam/embedlog"

	"github.com/odiumuniverse/beadle/pkg/vergerx"
)

// The watch lease is the only thing that stops two watchers writing at once
// (D19), and it is a real file on disk: a pid that is not alive is reclaimed,
// a live one is not. These tests therefore use real clients in a temp vault
// rather than a fake, because the behaviour under test is the file's.
func TestAcquirePluginWatchTakesTheLeaseOrNamesTheHolder(t *testing.T) {
	Convey("Given a vault with a plugin home", t, func() {
		ctx := context.Background()
		c, err := vergerx.Open(ctx, vergerx.Config{
			VaultRoot: t.TempDir(),
			Logger:    embedlog.NewDevLogger(),
		})
		So(err, ShouldBeNil)

		defer func() { _ = c.Close() }()

		Convey("When beadle asks for the plugin watch", func() {
			outcome, err := c.AcquirePluginWatch(ctx, vergerx.WatchOwner)

			Convey("Then it gets the lease and is told to release it on stop", func() {
				So(err, ShouldBeNil)
				So(outcome.Release, ShouldNotBeNil)
				So(outcome.HeldBy, ShouldBeEmpty)

				// A second request on the same home is what a standalone
				// `verger watch` makes while beadle watches.
				second, err := c.AcquirePluginWatch(ctx, "verger")

				So(err, ShouldBeNil)
				So(second.Release, ShouldBeNil)
				So(second.HeldBy, ShouldEqual, vergerx.WatchOwner)
				So(second.PID, ShouldEqual, os.Getpid())
			})
		})

		Convey("When a standalone watcher already holds the lease", func() {
			handle, err := c.AcquireLease("verger")
			So(err, ShouldBeNil)

			defer func() { _ = c.ReleaseLease(handle) }()

			Convey("Then beadle is told who holds it, with the pid to look for", func() {
				outcome, err := c.AcquirePluginWatch(ctx, vergerx.WatchOwner)

				So(err, ShouldBeNil)
				So(outcome.Release, ShouldBeNil)
				So(outcome.HeldBy, ShouldEqual, "verger")
				So(outcome.PID, ShouldEqual, os.Getpid())
			})
		})
	})
}
