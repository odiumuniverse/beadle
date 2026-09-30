package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/rulings"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

// writeJSONFile writes a document the migration will read back.
func writeJSONFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeFileAt(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// seedV3Vault lays out the on-disk state an older beadle would have written:
// the adoption stash directory, the plugin pivots and farm render area under
// the historical ids, and the two ledgers that key entries by them.
func seedV3Vault(t *testing.T, v *vault.Vault) {
	t.Helper()

	writeFileAt(t, filepath.Join(v.AdoptionsDir(), "claude-code", "alpha", "SKILL.md"), "alpha\n")
	writeFileAt(t, filepath.Join(v.PluginsDir(), "gemini-cli", "tool", "current", "SKILL.md"), "tool\n")
	writeFileAt(t, filepath.Join(v.PluginsDir(), "farm", "gemini-cli", "tool", "skills", "a.md"), "a\n")
	writeFileAt(t, filepath.Join(v.PluginsDir(), "quarantine", "antigravity-cli", "other", "current", "SKILL.md"), "other\n")

	writeJSONFile(t, v.PluginsLedgerPath(), `{
  "version": 1,
  "plugins": {
    "gemini-cli/tool": {"version": "1.0.0", "target": "/home/u/.gemini/extensions/tool", "source": "gemini-cli", "overridden": ["antigravity-cli", "claude-code"], "updated_at": "2026-01-01T00:00:00Z"},
    "acme/tool": {"version": "1.0.0", "target": "/home/u/.claude/plugins/acme/tool", "source": "claude-code", "updated_at": "2026-01-01T00:00:00Z"},
    "cursor/other": {"version": "1.0.0", "target": "/home/u/.cursor/plugins/other", "source": "cursor", "updated_at": "2026-01-01T00:00:00Z"}
  }
}
`)

	writeJSONFile(t, filepath.Join(v.RulingsDir(), "rulings.json"), `{
  "rulings": {
    "1111111111111111": {"signature": {"kind": "mcp", "target": "alpha", "divergence": "json:value-changed", "scope": "host:claude-code"}, "ruling": "take-agent", "state": "trusted", "source": "human", "confirmations": 1, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"}
  }
}
`)
}

func TestMigrateVaultAgentIDs(t *testing.T) {
	Convey("Given a vault written by a beadle that still used the historical agent ids", t, func() {
		f := newFixture(t)
		seedV3Vault(t, f.vault)

		Convey("When a sync runs", func() {
			report := f.sync(t)

			Convey("Then the adoption stash directory is renamed", func() {
				_, err := os.Stat(filepath.Join(f.vault.AdoptionsDir(), "claude", "alpha", "SKILL.md"))
				So(err, ShouldBeNil)

				_, err = os.Stat(filepath.Join(f.vault.AdoptionsDir(), "claude-code"))
				So(os.IsNotExist(err), ShouldBeTrue)
			})

			Convey("And the plugin pivot and farm directories are renamed", func() {
				_, err := os.Stat(filepath.Join(f.vault.PluginsDir(), "gemini", "tool", "current", "SKILL.md"))
				So(err, ShouldBeNil)

				_, err = os.Stat(filepath.Join(f.vault.PluginsDir(), "farm", "gemini", "tool", "skills", "a.md"))
				So(err, ShouldBeNil)

				_, err = os.Stat(filepath.Join(f.vault.PluginsDir(), "quarantine", "agy", "other", "current", "SKILL.md"))
				So(err, ShouldBeNil)

				_, err = os.Stat(filepath.Join(f.vault.PluginsDir(), "gemini-cli"))
				So(os.IsNotExist(err), ShouldBeTrue)
			})

			Convey("And the plugin ledger keys and sources are rewritten", func() {
				data, err := os.ReadFile(f.vault.PluginsLedgerPath()) //nolint:gosec // G304: test reads its own temp vault
				So(err, ShouldBeNil)

				So(string(data), ShouldContainSubstring, `"gemini/tool"`)
				So(string(data), ShouldContainSubstring, `"agy"`)
				So(string(data), ShouldContainSubstring, `"claude"`)
				So(string(data), ShouldContainSubstring, `"cursor/other"`)
				So(string(data), ShouldNotContainSubstring, "gemini-cli")
				So(string(data), ShouldNotContainSubstring, "antigravity-cli")
				So(string(data), ShouldNotContainSubstring, "claude-code")
			})

			Convey("And the ruling scope is renamed and re-keyed under its new hash", func() {
				ledger := gwsMigratedRulings(t, f.vault)
				So(ledger, ShouldHaveLength, 1)

				for _, r := range ledger {
					So(r.Signature.Scope, ShouldEqual, "host:claude")
				}
			})

			Convey("And each rename is reported once", func() {
				warnings := strings.Join(report.Warnings, "\n")
				So(warnings, ShouldContainSubstring, "claude-code")
				So(warnings, ShouldContainSubstring, "gemini-cli")
			})
		})

		Convey("When a dry run runs instead", func() {
			f.run(t, engine.SyncOptions{DryRun: true})

			Convey("Then the vault is left exactly as it was", func() {
				_, err := os.Stat(filepath.Join(f.vault.AdoptionsDir(), "claude-code", "alpha", "SKILL.md"))
				So(err, ShouldBeNil)

				_, err = os.Stat(filepath.Join(f.vault.PluginsDir(), "gemini-cli", "tool", "current", "SKILL.md"))
				So(err, ShouldBeNil)

				data, err := os.ReadFile(f.vault.PluginsLedgerPath()) //nolint:gosec // G304: test reads its own temp vault
				So(err, ShouldBeNil)
				So(string(data), ShouldContainSubstring, "gemini-cli/tool")
			})
		})
	})
}

func TestMigrateVaultAgentIDsIsIdempotent(t *testing.T) {
	Convey("Given a vault that was already migrated", t, func() {
		// A manager that claims the vault's plugin tree, so the first sync
		// really finishes the farm migration. "Already migrated" then means
		// what it says: the farm is gone, not merely recorded as gone.
		manager := &recordingManager{}

		f := newFixture(t, engine.WithPluginManager(manager), engine.WithVergerOwns(manager.Owns))
		manager.claims = []string{filepath.Join(f.vault.Root(), "plugins"), filepath.Join(f.home, ".claude")}
		manager.home = filepath.Join(f.vault.Root(), "verger")

		seedV3Vault(t, f.vault)
		f.sync(t)

		Convey("When a second sync runs", func() {
			report := f.sync(t)

			Convey("Then nothing is renamed again and nothing is reported", func() {
				So(report.Warnings, ShouldBeEmpty)

				_, err := os.Stat(filepath.Join(f.vault.AdoptionsDir(), "claude", "alpha", "SKILL.md"))
				So(err, ShouldBeNil)
			})
		})
	})
}

func TestMigrateVaultAgentIDsResumesAfterACrash(t *testing.T) {
	Convey("Given a vault where the directories moved but the ledgers did not", t, func() {
		f := newFixture(t)
		seedV3Vault(t, f.vault)

		// Simulate a crash between the renames and the ledger writes: the
		// directories are already canonical and only the ledgers lag.
		So(os.Rename(filepath.Join(f.vault.AdoptionsDir(), "claude-code"), filepath.Join(f.vault.AdoptionsDir(), "claude")), ShouldBeNil)
		So(os.Rename(filepath.Join(f.vault.PluginsDir(), "gemini-cli"), filepath.Join(f.vault.PluginsDir(), "gemini")), ShouldBeNil)

		Convey("When a sync runs", func() {
			f.sync(t)

			Convey("Then the run finishes the migration", func() {
				data, err := os.ReadFile(f.vault.PluginsLedgerPath()) //nolint:gosec // G304: test reads its own temp vault
				So(err, ShouldBeNil)
				So(string(data), ShouldContainSubstring, `"gemini/tool"`)
				So(string(data), ShouldNotContainSubstring, "gemini-cli")

				ledger := gwsMigratedRulings(t, f.vault)
				So(ledger, ShouldHaveLength, 1)

				for _, r := range ledger {
					So(r.Signature.Scope, ShouldEqual, "host:claude")
				}

				_, err = os.Stat(filepath.Join(f.vault.AdoptionsDir(), "claude", "alpha", "SKILL.md"))
				So(err, ShouldBeNil)
			})
		})
	})
}

func TestMigrateVaultAgentIDsKeepsForeignOrigins(t *testing.T) {
	Convey("Given a vault whose plugin origins are marketplace names and bundle hosts", t, func() {
		f := newFixture(t)
		writeFileAt(t, filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current", "SKILL.md"), "tool\n")
		writeFileAt(t, filepath.Join(f.vault.BundlesDir(), "claude", "marketplace.json"), "{}\n")
		writeFileAt(t, filepath.Join(f.vault.BundlesDir(), "antigravity", "plugin.json"), "{}\n")

		Convey("When a sync runs", func() {
			f.sync(t)

			Convey("Then those directories are left in place", func() {
				_, err := os.Stat(filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current", "SKILL.md"))
				So(err, ShouldBeNil)

				_, err = os.Stat(filepath.Join(f.vault.BundlesDir(), "claude", "marketplace.json"))
				So(err, ShouldBeNil)

				_, err = os.Stat(filepath.Join(f.vault.BundlesDir(), "antigravity", "plugin.json"))
				So(err, ShouldBeNil)
			})
		})
	})
}

// gwsMigratedRulings reads the vault ruling ledger back after a migration.
func gwsMigratedRulings(t *testing.T, v *vault.Vault) []rulings.Ruling {
	t.Helper()

	ledger, err := rulings.Load(v.RulingsPath())
	if err != nil {
		t.Fatalf("load rulings: %v", err)
	}

	return ledger.All()
}
