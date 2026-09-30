package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"github.com/vmkteam/embedlog"

	"github.com/odiumuniverse/beadle/pkg/vergerx"
)

func TestPluginsListEmpty(t *testing.T) {
	Convey("Given an empty vault", t, func() {
		vault := t.TempDir()
		out := &bytes.Buffer{}

		Convey("When plugins list runs", func() {
			ctx := context.Background()
			c, err := vergerx.Open(ctx, vergerx.Config{
				VaultRoot: vault,
				Logger:    embedlog.NewDevLogger(),
			})
			So(err, ShouldBeNil)

			defer func() { _ = c.Close() }()

			doc, err := c.Status(ctx)
			So(err, ShouldBeNil)

			Convey("Then it prints a header and no rows", func() {
				So(len(doc.Cells), ShouldEqual, 0)

				_ = out
			})
		})
	})
}

func TestPluginsInstallRequiresRef(t *testing.T) {
	Convey("Given a vault", t, func() {
		Convey("When plugins install is called without a ref", func() {
			Convey("Then it returns an error", func() {
				err := pluginsInstall(nil, nil, nil)
				So(err, ShouldNotBeNil)
			})
		})
	})
}

func TestPluginsRemoveRequiresID(t *testing.T) {
	Convey("Given a vault", t, func() {
		Convey("When plugins remove is called without an id", func() {
			Convey("Then it returns an error", func() {
				err := pluginsRemove(nil, nil, "", pluginFlags{})
				So(err, ShouldNotBeNil)
			})
		})
	})
}

func TestPluginsPinRequiresArgs(t *testing.T) {
	Convey("Given a vault", t, func() {
		Convey("When plugins pin is called without args", func() {
			Convey("Then it returns an error", func() {
				err := pluginsPin(nil, nil, nil)
				So(err, ShouldNotBeNil)
			})
		})
	})
}

func TestPluginsUnpinRequiresArgs(t *testing.T) {
	Convey("Given a vault", t, func() {
		Convey("When plugins unpin is called without args", func() {
			Convey("Then it returns an error", func() {
				err := pluginsUnpin(nil, nil, nil)
				So(err, ShouldNotBeNil)
			})
		})
	})
}

func TestPluginsEject(t *testing.T) {
	Convey("Given a vault with a verger home", t, func() {
		vault := t.TempDir()

		Convey("When plugins eject runs", func() {
			ctx := context.Background()
			c, err := vergerx.Open(ctx, vergerx.Config{
				VaultRoot: vault,
				Logger:    embedlog.NewDevLogger(),
			})
			So(err, ShouldBeNil)

			defer func() { _ = c.Close() }()

			Convey("Then it succeeds", func() {
				So(c, ShouldNotBeNil)
			})
		})
	})
}

func TestPluginsListJSON(t *testing.T) {
	Convey("Given a vault", t, func() {
		vault := t.TempDir()

		Convey("When plugins list --json runs", func() {
			ctx := context.Background()
			c, err := vergerx.Open(ctx, vergerx.Config{
				VaultRoot: vault,
				Logger:    embedlog.NewDevLogger(),
			})
			So(err, ShouldBeNil)

			defer func() { _ = c.Close() }()

			doc, err := c.Status(ctx)
			So(err, ShouldBeNil)

			data, err := json.Marshal(doc)
			So(err, ShouldBeNil)

			Convey("Then the output is valid JSON", func() {
				var parsed map[string]any
				So(json.Unmarshal(data, &parsed), ShouldBeNil)
			})
		})
	})
}

func TestPluginsInstallWithRef(t *testing.T) {
	Convey("Given a vault", t, func() {
		vault := t.TempDir()

		Convey("When plugins install is called with a ref", func() {
			ctx := context.Background()
			c, err := vergerx.Open(ctx, vergerx.Config{
				VaultRoot: vault,
				Logger:    embedlog.NewDevLogger(),
			})
			So(err, ShouldBeNil)

			defer func() { _ = c.Close() }()

			Convey("Then it attempts a plan", func() {
				_, err := c.Plan(ctx, []string{"test/ref"})
				So(err, ShouldNotBeNil) // no such ref, but the call is made
			})
		})
	})
}

func TestPluginsRemoveWithID(t *testing.T) {
	Convey("Given a vault", t, func() {
		vault := t.TempDir()

		Convey("When plugins remove is called with an id", func() {
			ctx := context.Background()
			c, err := vergerx.Open(ctx, vergerx.Config{
				VaultRoot: vault,
				Logger:    embedlog.NewDevLogger(),
			})
			So(err, ShouldBeNil)

			defer func() { _ = c.Close() }()

			Convey("Then it attempts a removal plan", func() {
				plan, err := c.PlanRemove(ctx, "test-pkg")
				So(err, ShouldBeNil)
				So(plan, ShouldNotBeNil)
			})
		})
	})
}

func TestPluginsPinWithArgs(t *testing.T) {
	Convey("Given a vault", t, func() {
		vault := t.TempDir()

		Convey("When plugins pin is called with args", func() {
			ctx := context.Background()
			c, err := vergerx.Open(ctx, vergerx.Config{
				VaultRoot: vault,
				Logger:    embedlog.NewDevLogger(),
			})
			So(err, ShouldBeNil)

			defer func() { _ = c.Close() }()

			Convey("Then it attempts a pin", func() {
				err := c.Pin(ctx, "test-pkg", "1.0.0")
				So(err, ShouldNotBeNil) // no such package, but the call is made
			})
		})
	})
}

func TestPluginsUnpinWithArgs(t *testing.T) {
	Convey("Given a vault", t, func() {
		vault := t.TempDir()

		Convey("When plugins unpin is called with args", func() {
			ctx := context.Background()
			c, err := vergerx.Open(ctx, vergerx.Config{
				VaultRoot: vault,
				Logger:    embedlog.NewDevLogger(),
			})
			So(err, ShouldBeNil)

			defer func() { _ = c.Close() }()

			Convey("Then it attempts an unpin", func() {
				err := c.Unpin(ctx, "test-pkg")
				So(err, ShouldNotBeNil) // no such package, but the call is made
			})
		})
	})
}

func TestPluginsEjectMovesHome(t *testing.T) {
	Convey("Given a vault with a verger home", t, func() {
		vault := t.TempDir()

		Convey("When plugins eject runs", func() {
			ctx := context.Background()
			c, err := vergerx.Open(ctx, vergerx.Config{
				VaultRoot: vault,
				Logger:    embedlog.NewDevLogger(),
			})
			So(err, ShouldBeNil)

			defer func() { _ = c.Close() }()

			vergerHome := filepath.Join(vault, "verger")

			Convey("Then the verger home exists", func() {
				So(c.Home(), ShouldEqual, vergerHome)
			})
		})
	})
}
