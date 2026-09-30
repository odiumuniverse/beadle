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

// canonMachine builds a vault with one canon skill and a secrets store, which is
// all the three guards below need.
func canonMachine(t *testing.T) (*engine.Engine, *vault.Vault, string) {
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

	e, err := engine.New(v, cfg, agent.All(home, t.TempDir()), engine.WithHome(home))
	if err != nil {
		t.Fatal(err)
	}

	return e, v, home
}

func writeCanonSkill(t *testing.T, home, name, body string) string {
	t.Helper()

	dir := filepath.Join(home, ".claude", "skills", name)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	skill := "---\nname: " + name + "\ndescription: d\n---\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skill), 0o600); err != nil {
		t.Fatal(err)
	}

	return dir
}

// TestTheCanonPackageVersionFollowsItsContent is the content-addressing
// requirement stated as the property it exists for: change one byte of the
// canon and the version changes; change nothing and it does not. A constant
// version satisfies neither half, which is what M1 was.
func TestTheCanonPackageVersionFollowsItsContent(t *testing.T) {
	Convey("Given a canon with one skill", t, func() {
		e, v, home := canonMachine(t)
		writeCanonSkill(t, home, "canon-skill", "first")

		_, err := e.Sync(t.Context(), engine.SyncOptions{})
		So(err, ShouldBeNil)

		Convey("Then the version is content-addressed", func() {
			So(packageVersion(t, v.CanonPackageDir()), ShouldStartWith, "0.0.0-")

			Convey("And an unchanged canon keeps it", func() {
				before := packageVersion(t, v.CanonPackageDir())

				_, syncErr := e.Sync(t.Context(), engine.SyncOptions{})
				So(syncErr, ShouldBeNil)

				So(packageVersion(t, v.CanonPackageDir()), ShouldEqual, before)
			})

			Convey("And a changed canon moves it", func() {
				before := packageVersion(t, v.CanonPackageDir())

				writeCanonSkill(t, home, "canon-skill", "second")

				_, syncErr := e.Sync(t.Context(), engine.SyncOptions{})
				So(syncErr, ShouldBeNil)

				So(packageVersion(t, v.CanonPackageDir()), ShouldNotEqual, before)
			})
		})
	})
}

// TestTheCanonPackageIsRefusedWhenItWouldCarryASecret is the guard the renderer
// cannot be trusted to enforce on its own. A value that reaches the package by
// any route — a skill copied verbatim, a future field — must still stop it from
// being written, and the refusal must be loud.
func TestTheCanonPackageIsRefusedWhenItWouldCarryASecret(t *testing.T) {
	Convey("Given a canon skill that quotes a stored secret", t, func() {
		e, v, home := canonMachine(t)

		e.Secrets().Set("api-token", leakValue)

		// A skill tree is copied verbatim, so a value pasted into its body
		// reaches the rendered package unless something stops it. That is the
		// realistic leak, and it is the one this guard exists for.
		dir := filepath.Join(home, ".claude", "skills", "leaky")
		So(os.MkdirAll(dir, 0o700), ShouldBeNil)

		body := "---\nname: leaky\ndescription: d\n---\n\nthe token is " + leakValue + "\n"
		So(os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600), ShouldBeNil)

		Convey("When the canon is packaged", func() {
			report, err := e.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then the package is not written and the refusal is loud", func() {
				So(strings.Join(report.Warnings, "\n"), ShouldContainSubstring, "carries a secret value")
			})

			Convey("Then there is no package at all", func() {
				// The refusal is total: the tree is not written, so there is
				// no version of it that could be picked up later. The user's
				// own host file still holds the value they typed, and so may
				// the canon that mirrors it — a package is the artefact that
				// travels to another machine, and it does not exist.
				_, statErr := os.Stat(v.CanonPackageDir())
				So(os.IsNotExist(statErr), ShouldBeTrue)
			})
		})
	})
}

// leakValue is a fixture, not a credential: it exists so a test can prove the
// value never ships.
const leakValue = "fixture-secret-value-never-shipped"
