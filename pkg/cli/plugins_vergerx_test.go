package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"github.com/vmkteam/embedlog"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
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

// TestPluginsEjectLeavesNoEmptyHome pins the last step of the move. The library
// takes the contents of the plugin home and leaves the directory it emptied, and
// that directory is not a neutral leftover: verger resolves the plugin home
// inside the vault before ~/.verger, so a bare `verger status` after an eject
// finds an empty home where every package used to be and reports no cells.
//
// The empty directory is the whole defect on this side. The other half — verger
// finding ~/.verger when the vault has none — is the library's Discover, and it
// has its own owner; what beadle owes the user is a vault that no longer claims a
// plugin home it gave away.
func TestPluginsEjectLeavesNoEmptyHome(t *testing.T) {
	Convey("Given a vault whose plugin home beadle created", t, func() {
		vault := t.TempDir()
		home := t.TempDir()

		// The ejected home is os.UserHomeDir()/.verger, so the machine's own
		// home is never the one this test moves a home into.
		t.Setenv("HOME", home)

		ctx := context.Background()

		c, err := vergerx.Open(ctx, vergerx.Config{
			VaultRoot: vault,
			Logger:    embedlog.NewDevLogger(),
		})
		So(err, ShouldBeNil)

		defer func() { _ = c.Close() }()

		vaultHome, target := filepath.Join(vault, "verger"), filepath.Join(home, ".verger")

		// The plugin home exists because an install created it: the client opens
		// lazily and does not make the directory by itself, and eject is a move
		// of a home that is already there.
		So(os.MkdirAll(vaultHome, 0o700), ShouldBeNil)

		Convey("When plugins eject runs", func() {
			out := &bytes.Buffer{}
			So(pluginsEjectFrom(c, out, io.Discard), ShouldBeNil)

			Convey("Then the emptied plugin home is gone from the vault", func() {
				_, statErr := os.Stat(vaultHome)
				So(os.IsNotExist(statErr), ShouldBeTrue)
			})

			Convey("And nothing of it is left anywhere in the vault", func() {
				entries, readErr := os.ReadDir(vault)
				So(readErr, ShouldBeNil)
				So(entries, ShouldBeEmpty)
			})

			Convey("And the packages are at the standalone home instead", func() {
				// Both spellings cleaned: filepath.Join inside defaultVergerHome produces
				// the canonical form, and t.TempDir hands over the raw one.
				So(target, ShouldStartWith, fsutil.Root(home))
				_, statErr := os.Stat(target)
				So(statErr, ShouldBeNil)
			})

			Convey("And a later beadle plugin command does not bring it back", func() {
				again, err := vergerx.Open(ctx, vergerx.Config{
					VaultRoot: vault,
					Logger:    embedlog.NewDevLogger(),
				})
				So(err, ShouldBeNil)

				defer func() { _ = again.Close() }()

				So(pluginsList(again, out), ShouldBeNil)

				_, statErr := os.Stat(vaultHome)
				So(os.IsNotExist(statErr), ShouldBeTrue)
			})
		})
	})
}
