package vergerx_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"github.com/vmkteam/embedlog"

	"github.com/odiumuniverse/beadle/pkg/testhost"
	"github.com/odiumuniverse/beadle/pkg/vergerx"
)

// The adopt ref is the one string beadle hands the library, and the library
// parses it: `<host>:<name>@<marketplace>`. The key's first segment is a
// MARKETPLACE, so reading it as the host builds a ref no host resolves and the
// farm migration never advances. These tests sit in the layer that builds the
// ref, so the failure is the parser's own verdict rather than a fixture's.
func TestAdoptBuildsARefVergerCanParse(t *testing.T) {
	// The three ledger forms a real machine carries, 1:1 with its ledger.json.
	forms := []struct {
		key    string
		source string
	}{
		{"vmkteam/vmkteam-developer", "claude-code"},
		{"caveman/caveman", "claude-code"},
		{"goland-claude-marketplace/modern-go-guidelines", "claude-code"},
	}

	for _, form := range forms {
		Convey("Given the farmed plugin "+form.key, t, func() {
			c := openVergerx(t)

			Convey("When beadle adopts it under the record's own host", func() {
				err := c.Adopt(context.Background(), form.key, form.source)

				Convey("Then the ref is in the grammar, so the parser never refuses it", func() {
					// An adoption of a package the spec does not list fails on
					// the package, not on the ref: that is the difference between
					// a migration that moves and one that retries forever.
					if err != nil {
						So(err.Error(), ShouldNotContainSubstring, "unknown scheme or host")
						So(err.Error(), ShouldNotContainSubstring, "invalid source ref")
					}
				})

				Convey("Then the ref names the host, the plugin and the marketplace", func() {
					marketplace, name, _ := strings.Cut(form.key, "/")

					if err != nil {
						So(err.Error(), ShouldContainSubstring, "claude:")
						So(err.Error(), ShouldContainSubstring, name+"@"+marketplace)
					}
				})
			})
		})
	}
}

func TestAdoptRefusesARecordWithoutAHost(t *testing.T) {
	Convey("Given a ledger record with no host", t, func() {
		c := openVergerx(t)

		Convey("When beadle adopts it", func() {
			err := c.Adopt(context.Background(), "vmkteam/vmkteam-developer", "")

			Convey("Then it is refused by name, before any ref is built", func() {
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, "no host")
			})
		})
	})
}

func TestAdoptRefusesAKeyThatIsNotAMarketplacePair(t *testing.T) {
	Convey("Given a key that is not <marketplace>/<name>", t, func() {
		c := openVergerx(t)

		for _, key := range []string{"vmkteam", "/name", "marketplace/"} {
			Convey("When beadle adopts "+key, func() {
				err := c.Adopt(context.Background(), key, "claude-code")

				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, "<marketplace>/<name>")
			})
		}
	})
}

func openVergerx(t *testing.T) *vergerx.Client {
	t.Helper()

	// The host has to look installed, and it is found with `exec.LookPath`:
	// on a developer box the real CLI is on PATH and adoption reaches the ref,
	// while under `env -i` no host CLI exists, the adapter list comes back
	// empty, and the test fails on "no available adapter" — a different
	// product question than the one it asks. Stubbing the CLIs makes the
	// answer the same everywhere; giving the test its own HOME alone did not,
	// because the binaries are found on PATH, not under the home.
	testhost.Stubs(t)

	// And the host that is found by its config directory rather than by a
	// CLI: Claude Code has no binary in the shared table, so stubs alone leave
	// it "installed, but no config found" and adoption still refuses.
	testhost.ConfigDirs(t, "claude")

	// The CLI itself, named: `claude` is not in the shared table's binary list
	// — it is a host this table identifies by config — so Stubs does not create
	// it, and adoption reaches the oracle and is refused for want of a host
	// CLI. A test that is about one host should say which.
	testhost.Wrap(t, "claude")

	c, err := vergerx.Open(t.Context(), vergerx.Config{
		VaultRoot: filepath.Join(t.TempDir(), "vault"),
		Logger:    embedlog.NewDevLogger(),
	})
	if err != nil {
		t.Fatalf("open vergerx: %v", err)
	}

	t.Cleanup(func() { _ = c.Close() })

	return c
}
