package engine_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
)

// TestTheEngineTalksToTheManager pins the seam the previous round left
// one-sided: the engine could take an ownership predicate, but nothing proved
// the manager itself — the object the migration asks — was reachable. The
// fixture is the same farm the migration test uses, so this asks the question
// through a real sync rather than a hand-built call.
func TestTheEngineTalksToTheManager(t *testing.T) {
	Convey("Given a machine with a farm and a manager that claims it", t, func() {
		e, v, home, manager := farmMachine(t, true)

		Convey("When the first sync runs", func() {
			_, err := e.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then the manager was asked, and the home exists in the vault", func() {
				So(manager.ownsCalls, ShouldBeGreaterThan, 0)

				info, statErr := os.Stat(manager.home)
				So(statErr, ShouldBeNil)
				So(info.IsDir(), ShouldBeTrue)
			})

			Convey("Then the plugin was handed over by its ledger key", func() {
				So(manager.adopted, ShouldContain, "acme/caveman")
			})

			Convey("Then the host came from the record, not from the key", func() {
				// The key's first segment is a MARKETPLACE. Reading it as the
				// host builds `acme:caveman`, a scheme no host resolves, and the
				// migration never moves.
				So(manager.adoptedAs, ShouldResemble, []string{"claude", "claude"})
			})

			Convey("Then the vault's plugin home is where the manager said it was", func() {
				So(manager.home, ShouldEqual, filepath.Join(v.Root(), "verger"))
				So(home, ShouldNotBeEmpty)
			})
		})
	})

	Convey("Given a machine with a farm and no manager", t, func() {
		e, v, _, _ := farmMachine(t, false)

		Convey("When a sync runs", func() {
			_, err := e.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then the vault has no plugin home and the farm was not touched", func() {
				_, statErr := os.Stat(filepath.Join(v.Root(), "verger"))
				So(os.IsNotExist(statErr), ShouldBeTrue)

				cfg, loadErr := config.Load(v.ConfigPath())
				So(loadErr, ShouldBeNil)
				So(cfg, ShouldNotBeNil)

				for _, a := range []string{agent.ClaudeCodeID} {
					So(cfg.Agents[a], ShouldNotBeNil)
				}
			})
		})
	})
}
