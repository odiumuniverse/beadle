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
	"github.com/odiumuniverse/beadle/pkg/testhost"
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

		// No ~/.verger anywhere on this machine: that is what puts the first
		// home in the vault. beadle's own branch is conditioned on the
		// standalone home NOT existing, and TestOpenKeepsAnExistingStandaloneHome
		// pins the other half of that condition.
		client, err := Open(context.Background(), Config{VaultRoot: root, Logger: embedlog.NewDevLogger()})
		So(err, ShouldBeNil)

		defer func() { _ = client.Close() }()

		Convey("Then the home beadle works on is the one inside the vault", func() {
			// Resolution. Discovery finds no standalone home on this machine
			// (see the Given) and no vault home either — verger's vaultAt only
			// answers for a directory that exists — so it is beadle's own
			// first-run branch that places this home inside the vault.
			//
			// What this does NOT pin, and cannot yet: that Open creates nothing.
			// Under the vendored verger v0.1.1 the home is already a directory
			// when Open returns, measured — so the opposite assertion would be
			// red today. pR-30 makes discovery read-only and hands creation to
			// the first write; when that lands, the missing half belongs here as
			// `So(dirExists(client.Home()), ShouldBeFalse)`.
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

// TestOpenKeepsAnExistingStandaloneHome pins the other half of the condition
// TestOpenCreatesTheHomeDirectory relies on. beadle puts the first plugin home
// in the vault only while there is no standalone home to lose; the moment
// ~/.verger exists it keeps working there.
//
// The condition is worth pinning from both sides. Drop the `!h.Exists()` and a
// machine with packages already in ~/.verger grows a second, empty home inside
// its vault — and an empty directory inside the vault is a home as far as
// discovery is concerned, which is the same trap an eject once fell into. Drop
// the `h.Exists()` and beadle migrates a populated home into the vault behind
// the user's back.
func TestOpenKeepsAnExistingStandaloneHome(t *testing.T) {
	Convey("Given a machine that already has a standalone plugin home", t, func() {
		root := t.TempDir()
		userHome := t.TempDir()
		t.Setenv("HOME", userHome)

		standalone := filepath.Join(userHome, ".verger")
		So(os.MkdirAll(standalone, 0o700), ShouldBeNil)
		So(os.WriteFile(filepath.Join(standalone, "spec.yaml"), []byte("{}\n"), 0o600), ShouldBeNil)

		client, err := Open(context.Background(), Config{VaultRoot: root, Logger: embedlog.NewDevLogger()})
		So(err, ShouldBeNil)

		defer func() { _ = client.Close() }()

		Convey("Then the home beadle works on is still the one the user has", func() {
			So(client.Home(), ShouldEqual, standalone)
		})

		Convey("And no empty home appears in the vault beside it", func() {
			So(dirExists(filepath.Join(root, "verger")), ShouldBeFalse)
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

	// The hosts have to look installed before a plan can name one. beadle
	// decides that with `exec.LookPath` on each host's own CLI, so on a
	// developer box the real CLIs answer and under `env -i` — which is what CI
	// runs, and what this repository's hermetic gate runs — the adapter list
	// comes back empty and the plan refuses with "--hosts: no available
	// adapter". That is a fact about the machine, not about the code under
	// test, so the test states the machine it needs instead of inheriting one.
	testhost.Stubs(t)

	plan, err := c.PlanFor(context.Background(), []string{ref}, false, verger.HostFilter{})
	So(err, ShouldBeNil)

	return plan
}
