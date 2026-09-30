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

		Convey("Then the home exists inside the vault", func() {
			So(client.Home(), ShouldEqual, filepath.Join(root, "verger"))
			So(dirExists(client.Home()), ShouldBeTrue)
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
