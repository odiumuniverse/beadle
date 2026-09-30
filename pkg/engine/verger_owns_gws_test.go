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
	"github.com/odiumuniverse/beadle/pkg/skill"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

// newOwnedEngine builds an engine whose plugin manager claims the given paths,
// exactly as the CLI wires it: one predicate, consulted by every kind's pull.
func newOwnedEngineIn(t *testing.T, home string, owned ...string) (*engine.Engine, *vault.Vault) {
	t.Helper()

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

	claim := map[string]bool{}
	for _, path := range owned {
		claim[filepath.Clean(path)] = true
	}

	// The plugin manager owns whole trees, so the predicate is a prefix match
	// on the directories it claimed — the same shape as a package install.
	owns := func(path string) (string, bool) {
		clean := filepath.Clean(path)
		for dir := range claim {
			if clean == dir || strings.HasPrefix(clean, dir+string(filepath.Separator)) {
				return "verger", true
			}
		}

		return "", false
	}

	e, err := engine.New(v, cfg, agent.All(home, t.TempDir()),
		engine.WithHome(home), engine.WithVergerOwns(owns))
	if err != nil {
		t.Fatal(err)
	}

	return e, v
}

func skillTree(t *testing.T, dir, name, body string) {
	t.Helper()

	So := os.MkdirAll(filepath.Join(dir, name), 0o700)
	if So != nil {
		t.Fatalf("mkdir %s: %v", name, So)
	}

	if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestAVergerDeliveredSkillNeverEntersTheVault is the ownership invariant the
// whole embedding exists for: a skill the plugin manager owns is reported to
// the user, and the canon stays free of it.
func TestAVergerDeliveredSkillNeverEntersTheVault(t *testing.T) {
	Convey("Given a skill the plugin manager delivered to a host", t, func() {
		// The owned path is inside the engine's own home, so the home is
		// chosen by the caller and the engine is built around it.
		home := t.TempDir()
		skills := filepath.Join(home, ".claude", "skills")
		vergerSkill := filepath.Join(skills, "verger-delivered")

		skillTree(t, skills, "verger-delivered", "---\nname: verger-delivered\n---\n\nfrom the plugin manager\n")
		skillTree(t, skills, "beadles-own", "---\nname: beadles-own\n---\n\nbeadle's own\n")

		e, v := newOwnedEngineIn(t, home, vergerSkill)

		Convey("When the vault synchronises", func() {
			report, err := e.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then the plugin skill is not in the vault", func() {
				_, statErr := os.Stat(filepath.Join(v.SkillsDir(), "verger-delivered"))
				So(os.IsNotExist(statErr), ShouldBeTrue)
			})

			Convey("Then beadle's own skill is", func() {
				_, statErr := os.Stat(filepath.Join(v.SkillsDir(), "beadles-own"))
				So(statErr, ShouldBeNil)
			})

			Convey("Then the user is told why the other one was left out", func() {
				var joined []string

				joined = append(joined, report.Warnings...)

				for _, k := range report.Kinds {
					joined = append(joined, k.Warnings...)
				}

				line := strings.Join(joined, "\n")
				So(line, ShouldContainSubstring, "verger-delivered")
				So(line, ShouldContainSubstring, "plugin manager")
			})
		})
	})
}

// TestTheOwnershipFilterIsOptional pins the other half: a beadle with no plugin
// manager open owns nothing, and every kind behaves exactly as before.
func TestTheOwnershipFilterIsOptional(t *testing.T) {
	Convey("Given a skill and no plugin manager", t, func() {
		home := t.TempDir()
		skills := filepath.Join(home, ".claude", "skills")
		skillTree(t, skills, "plain", "---\nname: plain\n---\n\nplain\n")

		e, v := newOwnedEngineIn(t, home)

		Convey("When the vault synchronises", func() {
			_, err := e.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then the skill is adopted as before", func() {
				trees, readErr := skill.ReadDir(v.SkillsDir())
				So(readErr, ShouldBeNil)
				So(trees, ShouldContainKey, "plain")
			})
		})
	})
}
