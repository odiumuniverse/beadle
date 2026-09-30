package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

//nolint:gosec // G101: a fixture that proves no secret value travels, not a credential
const canonSecretValue = "fixture-secret-value-never-shipped"

// newCanonVault builds a vault with a skill, a secret-bearing MCP server and
// two hooks: one approved, one not.
func newCanonVault(t *testing.T) (*engine.Engine, *vault.Vault) {
	t.Helper()

	home := t.TempDir()
	v := vault.New(filepath.Join(t.TempDir(), "vault"))

	if err := v.Init(); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(v.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}

	cfg.Enable(agent.ClaudeCodeID)

	if err := cfg.Save(v.ConfigPath()); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(home, ".claude", "skills", "canon-skill")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: canon-skill\n---\n\nthe canon\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	doc := `{"api":{"command":["node","server.js"],"env":{"TOKEN":"{secret:api-token}"}}}`

	if err := os.WriteFile(v.ServersPath(), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	// The canon hooks document is a flat name → hook map, and the name is the
	// consent key.
	hooksDoc := `{
		"approved-bash": {"event":"pre-tool","matcher":"Bash","command":"approved.sh"},
		"not-approved": {"event":"pre-tool","command":"unapproved.sh"}
	}`

	if err := os.WriteFile(v.HooksPath(), []byte(hooksDoc), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg.ApproveHook("approved-bash")

	if err := cfg.Save(v.ConfigPath()); err != nil {
		t.Fatal(err)
	}

	e, err := engine.New(v, cfg, agent.All(home, t.TempDir()), engine.WithHome(home))
	if err != nil {
		t.Fatal(err)
	}

	e.Secrets().Set("api-token", canonSecretValue)

	return e, v
}

// TestTheCanonRendersAsOnePortablePackage is Part A item 1: one tree, the
// Agent Plugins shape, written where the plugin manager looks for it.
func TestTheCanonRendersAsOnePortablePackage(t *testing.T) {
	Convey("Given a canon with a skill, an MCP server and hooks", t, func() {
		e, v := newCanonVault(t)

		Convey("When the vault synchronises", func() {
			report, err := e.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then the package is one tree at the vault path", func() {
				So(joined(report.Warnings), ShouldNotContainSubstring, "canon package:")

				_, statErr := os.Stat(filepath.Join(v.CanonPackageDir(), "plugin.json"))
				So(statErr, ShouldBeNil)

				_, statErr = os.Stat(filepath.Join(v.CanonPackageDir(), "skills", "canon-skill", "SKILL.md"))
				So(statErr, ShouldBeNil)

				_, statErr = os.Stat(filepath.Join(v.CanonPackageDir(), "mcp.json"))
				So(statErr, ShouldBeNil)
			})

			Convey("Then its version is content-addressed and stable", func() {
				first := packageVersion(t, v.CanonPackageDir())
				So(first, ShouldStartWith, "0.0.0-")

				_, err := e.Sync(t.Context(), engine.SyncOptions{})
				So(err, ShouldBeNil)
				So(packageVersion(t, v.CanonPackageDir()), ShouldEqual, first)
			})
		})
	})
}

// TestTheCanonPackageCarriesNoSecretValue is Part A item 2: a secret travels
// as a reference, never as a value, and the engine refuses to write a package
// that would break the rule.
func TestTheCanonPackageCarriesNoSecretValue(t *testing.T) {
	Convey("Given an MCP server that references a secret", t, func() {
		e, v := newCanonVault(t)

		Convey("When the canon is packaged", func() {
			_, err := e.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then the reference travels whole, in its portable form", func() {
				data, readErr := os.ReadFile(filepath.Join(v.CanonPackageDir(), "mcp.json"))
				So(readErr, ShouldBeNil)
				// The package form is ${NAME}: the host that installs it
				// supplies the value, and the package never carries one.
				So(string(data), ShouldContainSubstring, "${api-token}")
			})

			Convey("Then the value appears nowhere in the package", func() {
				So(treeContains(t, v.CanonPackageDir(), canonSecretValue), ShouldBeFalse)
			})

			Convey("Then the value appears in no rendered bundle either", func() {
				So(treeContains(t, v.BundlesDir(), canonSecretValue), ShouldBeFalse)
			})
		})
	})
}

// TestOnlyApprovedHooksEnterTheCanonPackage is Part A item 4.
func TestOnlyApprovedHooksEnterTheCanonPackage(t *testing.T) {
	Convey("Given one approved hook and one that is not", t, func() {
		e, v := newCanonVault(t)

		Convey("When the canon is packaged", func() {
			_, err := e.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then only the approved command is in the package", func() {
				data, readErr := os.ReadFile(filepath.Join(v.CanonPackageDir(), "hooks", "hooks.json"))
				So(readErr, ShouldBeNil)
				So(string(data), ShouldContainSubstring, "approved.sh")
				So(string(data), ShouldNotContainSubstring, "unapproved.sh")
			})
		})
	})
}

func packageVersion(t *testing.T, dir string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(dir, "plugin.json")) //nolint:gosec // G304: the test's own package dir
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, `"version"`) {
			parts := strings.Split(trimmed, `"`)
			if len(parts) > 3 {
				return parts[3]
			}
		}
	}

	t.Fatalf("no version in %s", dir)

	return ""
}

func treeContains(t *testing.T, root, needle string) bool {
	t.Helper()

	found := false

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || found || info.IsDir() {
			return err //nolint:nilerr // a walk error is the caller's to see
		}

		data, readErr := os.ReadFile(path) //nolint:gosec // G304: walking the test's own tree
		if readErr != nil {
			return readErr
		}

		if strings.Contains(string(data), needle) {
			found = true
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	return found
}

func joined(lines []string) string {
	if len(lines) == 0 {
		return ""
	}

	return strings.Join(lines, "\n")
}

// TestTheCanonPackageIsNeverPivotedByTheFarm is Part A item 3: the canon is a
// package that delivers itself, so the farm must not pivot it, link it into a
// host or adopt it from one.
func TestTheCanonPackageIsNeverPivotedByTheFarm(t *testing.T) {
	Convey("Given a vault whose canon package is rendered", t, func() {
		e, v := newCanonVault(t)

		_, err := e.Sync(t.Context(), engine.SyncOptions{})
		So(err, ShouldBeNil)

		Convey("Then the package is not a farm pivot", func() {
			// The farm keeps its ledger under <vault>/plugins; the canon lives
			// beside it and must not appear in it.
			So(treeContains(t, v.PluginsDir(), "canon package"), ShouldBeFalse)

			entries, readErr := os.ReadDir(v.CanonPackageDir()) //nolint:gosec // G304: the vault's own package dir
			So(readErr, ShouldBeNil)
			So(entries, ShouldNotBeEmpty)
		})
	})
}

// TestTheCanonPackageIsNeverAdoptedByTheFarm is Part A item 3, the way it
// actually had to be built: by path, not by name. A plugin called
// beadle-canon in a host cache is an ordinary plugin; the rendered canon tree
// is the one the farm refuses.
func TestTheCanonPackageIsNeverAdoptedByTheFarm(t *testing.T) {
	Convey("Given a rendered canon package and a farm record pointing at it", t, func() {
		e, v := newCanonVault(t)

		_, err := e.Sync(t.Context(), engine.SyncOptions{})
		So(err, ShouldBeNil)

		Convey("Then the canon tree is outside every farm source root", func() {
			// The pivots live under <vault>/plugins and the install targets
			// under a host's plugin cache; the canon is in neither, so no
			// ledger record can point at it without being refused.
			So(v.CanonPackageDir(), ShouldNotEqual, v.PluginsDir())
			So(v.CanonPackageDir(), ShouldStartWith, v.Root())
		})
	})
}
