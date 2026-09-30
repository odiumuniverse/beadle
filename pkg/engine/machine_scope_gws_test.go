package engine_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

// The delivery record is machine-local: "beadle wrote this file here" is a fact
// about one machine's host directory. A record that travelled with the vault —
// because the vault was copied with `cp -a` rather than cloned, or because a
// state file was carried along by hand — describes what ANOTHER machine wrote to
// ITS host. Reading it here turns "this host has no such file yet" into "the
// user deleted it", and a mass deletion of beadle's own skills is reported at
// the user on a machine that never had them.
func TestADeliveryRecordFromAnotherMachineIsNotThisMachines(t *testing.T) {
	Convey("Given a machine that synced two canon skills to its hosts", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, f.vaultSkill("alpha"), "# alpha\n")
		write(t, f.vaultSkill("beta"), "# beta\n")

		f.sync(t)

		So(read(t, f.claudeSkill("alpha")), ShouldNotBeEmpty)
		So(read(t, f.claudeSkill("beta")), ShouldNotBeEmpty)

		Convey("When the state says another machine wrote them, and this host has none", func() {
			rehomeState(t, f.vault, filepath.Join(t.TempDir(), "another-machine"))
			removeHostSkills(t, f)

			report := f.sync(t)

			Convey("Then the missing files are not a deletion", func() {
				So(f.conflicts(t, kind.Skills, agent.ClaudeCodeID), ShouldBeEmpty)
			})

			Convey("Then they are delivered again on this machine", func() {
				So(pushedKinds(report), ShouldNotBeEmpty)
				So(read(t, f.claudeSkill("alpha")), ShouldNotBeEmpty)
				So(read(t, f.claudeSkill("beta")), ShouldNotBeEmpty)
			})
		})

		Convey("When the state says THIS machine wrote them, and this host has none", func() {
			rehomeState(t, f.vault, f.home)
			removeHostSkills(t, f)

			report := f.sync(t)

			Convey("Then the deletion is still the user's, and still waits", func() {
				// The other half of the guard: a machine-scoped record that
				// fired for its own machine too would make the mass-delete
				// protection unreachable, and a real mass deletion would be
				// adopted as fact.
				conflicts := f.conflicts(t, kind.Skills, agent.ClaudeCodeID)
				So(conflicts, ShouldNotBeEmpty)
				So(conflicts[0].Reason, ShouldEqual, state.ReasonMassDelete)
				So(pushedKinds(report), ShouldBeEmpty)
			})
		})
	})
}

// rehomeState rewrites the machine the state's delivery record belongs to, the
// way a copied vault arrives stamped with its previous machine's home.
func rehomeState(t *testing.T, v *vault.Vault, home string) {
	t.Helper()

	st, err := state.Load(v.StatePath())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}

	st.Home = home

	if err := st.Save(v.StatePath()); err != nil {
		t.Fatalf("save state: %v", err)
	}
}

// removeHostSkills takes away everything beadle wrote to the host surfaces,
// which is what a user's mass deletion looks like to the next sync.
func removeHostSkills(t *testing.T, f *fixture) {
	t.Helper()

	for _, dir := range []string{
		filepath.Join(f.home, ".claude", "skills"),
		filepath.Join(f.home, ".config", "opencode", "skills"),
		filepath.Join(f.home, ".agents", "skills"),
	} {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatalf("remove %s: %v", dir, err)
		}
	}
}
