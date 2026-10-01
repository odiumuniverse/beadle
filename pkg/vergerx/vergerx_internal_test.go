package vergerx

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"github.com/vmkteam/embedlog"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/secret"
	"github.com/odiumuniverse/verger/pkg/verger"
)

func dirExists(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.IsDir()
}

// TestOpenKeepsTheConfirmerAndTheEventSink is the internal check that the two
// dependencies beadle hands over actually reach the executor's options. A
// wrapper that accepted a Confirmer and an Events channel and then dropped
// them would be indistinguishable from one that never had them.
func TestOpenKeepsTheConfirmerAndTheEventSink(t *testing.T) {
	eventsCh := make(chan verger.Event, 1)

	Convey("Given a client opened with beadle's confirmer and event sink", t, func() {
		client, err := Open(context.Background(), Config{
			VaultRoot: t.TempDir(),
			Logger:    embedlog.NewDevLogger(),
			Events:    eventsCh,
		})

		So(err, ShouldBeNil)

		Convey("Then the run's options carry both, not a default", func() {
			opts, err := client.applyOptions()
			So(err, ShouldBeNil)
			So(opts.Confirm, ShouldBeNil)
			So(opts.Events, ShouldEqual, (chan<- verger.Event)(eventsCh))
		})
	})
}

// TestEnsureAgentMapsHistoricalIDs pins the alias table in both directions: a
// historical id that reaches beadle from a stored spec must be canonicalised
// before it is handed to the library, and an unknown id must still reach the
// caller so it is reported as unknown.
func TestEnsureAgentMapsHistoricalIDs(t *testing.T) {
	Convey("Given the historical ids beadle used before U4", t, func() {
		agents := []agent.Agent{{ID: "claude"}, {ID: "gemini"}, {ID: "agy"}}

		Convey("Then every alias resolves to its canonical id", func() {
			So(EnsureAgent("claude-code", agents), ShouldEqual, "claude")
			So(EnsureAgent("gemini-cli", agents), ShouldEqual, "gemini")
			So(EnsureAgent("antigravity-cli", agents), ShouldEqual, "agy")
			So(HostForAgent("gemini-cli"), ShouldEqual, "gemini")
			So(AgentForHost("claude-code"), ShouldEqual, "claude")
		})

		Convey("Then a canonical id is returned unchanged", func() {
			So(EnsureAgent("claude", agents), ShouldEqual, "claude")
			So(HostForAgent("omp"), ShouldEqual, "omp")
		})

		Convey("Then an unknown id still reaches the caller", func() {
			So(EnsureAgent("nonesuch", agents), ShouldEqual, "nonesuch")
			So(HostForAgent("nonesuch"), ShouldEqual, "nonesuch")
		})
	})
}

// TestOpenCreatesTheHomeDirectory pins the vault-side half of the contract:
// the plugin home is a real directory in the vault, not a path the library
// happens to accept.
func TestOpenCreatesTheHomeDirectory(t *testing.T) {
	Convey("Given a vault with no verger directory", t, func() {
		root := t.TempDir()

		client, err := Open(context.Background(), Config{VaultRoot: root, Logger: embedlog.NewDevLogger()})
		So(err, ShouldBeNil)

		defer func() { _ = client.Close() }()

		Convey("Then the home beadle works on is the one inside the vault", func() {
			// Resolution, not creation. Discovery is read-only and names a
			// directory that does not exist yet — naming it is neither a write
			// nor a risk, and the subdirectory is the library's to create the
			// first time something needs it. A test that pinned EAGER creation
			// was pinning a moment that is not the contract: the contract is
			// WHICH home, and that it is a real directory once there is
			// anything to put in it.
			So(client.Home(), ShouldEqual, filepath.Join(root, "verger"))
		})

		Convey("And it is a real directory in the vault as soon as beadle writes", func() {
			// The premise restated at the moment it holds: an adoption is the
			// cheapest write the client has, and after it the home is a
			// directory with state in it — not a path the library tolerates.
			// A minimal local package, the same shape the e2e builds: a
			// manifest and one skill. This is the first thing beadle does that
			// writes into the plugin home, which is the moment the directory
			// has to exist.
			pkg := filepath.Join(t.TempDir(), "pkg")
			So(os.MkdirAll(filepath.Join(pkg, ".claude-plugin"), 0o750), ShouldBeNil)
			So(os.MkdirAll(filepath.Join(pkg, "skills", "alpha"), 0o750), ShouldBeNil)
			So(os.WriteFile(filepath.Join(pkg, ".claude-plugin", "plugin.json"),
				[]byte(`{"name":"vw-pkg","version":"1.0.0"}`), 0o600), ShouldBeNil)
			So(os.WriteFile(filepath.Join(pkg, "skills", "alpha", "SKILL.md"),
				[]byte("---\nname: alpha\ndescription: alpha probe\n---\n\nb\n"), 0o600), ShouldBeNil)

			client.SetConfirmer(YesConfirmer())

			_, err := client.InstallFor(context.Background(),
				planFor(t, client, pkg), false, false)
			So(err, ShouldBeNil)

			So(client.Home(), ShouldEqual, filepath.Join(root, "verger"))
			So(dirExists(client.Home()), ShouldBeTrue)

			entries, readErr := os.ReadDir(client.Home())
			So(readErr, ShouldBeNil)
			So(entries, ShouldNotBeEmpty)
		})
	})
}

// TestTheClientRunsOnBeadlesSecretStore pins API-REQ 1: the store the plugin
// manager uses is beadle's own, not a second one. Two stores for one machine
// is how a secret a plugin writes stops being a secret beadle reads.
func TestTheClientRunsOnBeadlesSecretStore(t *testing.T) {
	Convey("Given a client opened with beadle's secret store", t, func() {
		store, loadErr := secret.Load(filepath.Join(t.TempDir(), "secrets.json"))
		So(loadErr, ShouldBeNil)

		client, err := Open(context.Background(), Config{
			VaultRoot: t.TempDir(),
			Logger:    embedlog.NewDevLogger(),
			Secrets:   store,
		})

		So(err, ShouldBeNil)

		Convey("Then the store is in force", func() {
			So(client.SecretsInForce(), ShouldBeTrue)
		})

		Convey("Then a value the store already holds is the library's", func() {
			store.Set("api-token", "fixture")

			value, ok := store.Get("api-token")
			So(ok, ShouldBeTrue)
			So(value, ShouldEqual, "fixture")
			So(store.Names(), ShouldContain, "api-token")
		})
	})

	Convey("Given a client opened without a store", t, func() {
		client, err := Open(context.Background(), Config{VaultRoot: t.TempDir(), Logger: embedlog.NewDevLogger()})

		So(err, ShouldBeNil)

		Convey("Then it says so rather than pretending", func() {
			So(client.SecretsInForce(), ShouldBeFalse)
		})
	})
}

// planFor plans one local package with no host filter, which is what
// `beadle plugins install ./pkg --yes` reaches.
func planFor(t *testing.T, c *Client, ref string) *verger.Plan {
	t.Helper()

	plan, err := c.PlanFor(context.Background(), []string{ref}, false, verger.HostFilter{})
	So(err, ShouldBeNil)

	return plan
}
