package vergerx_test

import (
	"context"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"github.com/vmkteam/embedlog"

	"github.com/odiumuniverse/beadle/pkg/vergerx"
)

func TestOpenCreatesVergerHomeInsideVault(t *testing.T) {
	Convey("Given a vault root", t, func() {
		vault := t.TempDir()

		Convey("When vergerx opens", func() {
			ctx := context.Background()
			c, err := vergerx.Open(ctx, vergerx.Config{
				VaultRoot: vault,
				Logger:    embedlog.NewDevLogger(),
			})
			So(err, ShouldBeNil)

			defer func() { _ = c.Close() }()

			Convey("Then the verger home is inside the vault", func() {
				So(c.Home(), ShouldEqual, filepath.Join(vault, "verger"))
			})
		})
	})
}

func TestAgentForHostRoundTrip(t *testing.T) {
	Convey("Given a host id", t, func() {
		Convey("When mapped to agent and back", func() {
			Convey("Then the id is unchanged", func() {
				So(vergerx.AgentForHost("claude"), ShouldEqual, "claude")
				So(vergerx.HostForAgent("claude"), ShouldEqual, "claude")
			})
		})
	})
}

func TestEnsureAgentResolvesCanonicalID(t *testing.T) {
	Convey("Given a list of agents", t, func() {
		Convey("When looking up a known id", func() {
			Convey("Then it returns the canonical id", func() {
				So(vergerx.EnsureAgent("claude", nil), ShouldEqual, "claude")
			})
		})

		Convey("When looking up an unknown id", func() {
			Convey("Then it returns the input unchanged", func() {
				So(vergerx.EnsureAgent("unknown", nil), ShouldEqual, "unknown")
			})
		})
	})
}
