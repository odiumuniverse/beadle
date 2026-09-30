package engine_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/state"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

// The two properties the mutation batch found unpinned (M3, M4).
//
// M3: a plugin the library refused is invisible unless the doctor says so, and a
// live observation is not a test - deleting the finding leaves the suite green.
// M4: "one backup per migration" is the property; the stage machine skips the
// backup stage on a re-run, so inverting the idempotence check in `backup()`
// is invisible to every test that only ever runs a completed migration.

// skippedFinding is one subject of a doctor report, so the assertions name the
// fields a consumer reads.
func skippedFinding(t *testing.T, st *state.State) *engine.Issue {
	t.Helper()

	issues, err := doctorFor(t, st)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}

	for i := range issues {
		if issues[i].Subject == "plugin.skipped.acme/broken" {
			return &issues[i]
		}
	}

	return nil
}

func doctorFor(t *testing.T, st *state.State) ([]engine.Issue, error) {
	t.Helper()

	root := filepath.Join(t.TempDir(), "vault")
	v := vault.New(root)

	if err := v.Init(); err != nil {
		t.Fatalf("init vault: %v", err)
	}

	cfg, err := config.Load(v.ConfigPath())
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	e, err := engine.New(v, cfg, nil)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	if err := st.Save(v.StatePath()); err != nil {
		t.Fatalf("save state: %v", err)
	}

	return e.Doctor(t.Context())
}

func TestSkippedPluginsAreNamedByDoctor(t *testing.T) {
	Convey("Given a state that records one plugin the library could not resolve", t, func() {
		st := &state.State{FarmMigration: &state.FarmMigration{
			Skipped: map[string]string{"acme/broken": "not listed by the oracle"},
		}}

		Convey("When doctor runs", func() {
			finding := skippedFinding(t, st)

			Convey("Then the plugin is named, with the library's own reason", func() {
				So(finding, ShouldNotBeNil)
				So(finding.Message, ShouldContainSubstring, "acme/broken")
				So(finding.Message, ShouldContainSubstring, "not listed by the oracle")
			})

			Convey("Then the fix is a printed command and never an auto-fix", func() {
				So(finding.Fix, ShouldResemble, [][]string{{"beadle", "plugins", "install", "acme/broken"}})
				So(finding.SafeToAutofix, ShouldBeFalse)
			})
		})

		Convey("When the state records nothing skipped", func() {
			empty := &state.State{FarmMigration: &state.FarmMigration{}}

			Convey("Then there is no such finding", func() {
				So(skippedFinding(t, empty), ShouldBeNil)
			})
		})
	})
}

func TestASecondRunTakesNoSecondBackup(t *testing.T) {
	Convey("Given a farm whose migration has already run", t, func() {
		e, v, farmHome, _ := farmMachine(t, true)

		countBackups := func() int {
			entries, err := os.ReadDir(filepath.Join(filepath.Dir(v.StatePath()), "state", "backups"))
			So(err, ShouldBeNil)

			return len(entries)
		}

		_, err := e.Sync(t.Context(), engine.SyncOptions{})
		So(err, ShouldBeNil)

		Convey("Then it backed the vault up exactly once", func() {
			So(countBackups(), ShouldEqual, 1)
		})

		Convey("When the stages are rewound and the run repeats", func() {
			// The stage machine skips the backup stage on a re-run, so the
			// property has to be asked directly: re-enter the migration and
			// count again. Inverting the idempotence check makes this two.
			st, err := state.Load(v.StatePath())
			So(err, ShouldBeNil)

			st.FarmMigration.Stage = ""
			st.FarmMigration.Done = false
			So(st.Save(v.StatePath()), ShouldBeNil)

			// The finished run retired the ledger entry, and a migration with
			// no farm does not start. Restoring the ledger is what a user does
			// with an old backup, and it is what puts the backup stage back in
			// reach.
			ledger := map[string]any{"version": 1, "plugins": map[string]any{
				farmPlugin: map[string]any{
					"version":    "0.1.0",
					"target":     filepath.Join(farmHome, ".claude", "plugins", "cache", "acme", "caveman", "0.1.0"),
					"source":     "claude-code",
					"updated_at": "2026-01-01T00:00:00Z",
				},
			}}

			data, err := json.Marshal(ledger)
			So(err, ShouldBeNil)
			So(os.WriteFile(filepath.Join(v.Root(), "plugins", "ledger.json"), data, 0o600), ShouldBeNil)

			_, err = e.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			after, err := state.Load(v.StatePath())
			So(err, ShouldBeNil)
			// The rewind has to re-enter the migration, or the assertion below
			// would pass for the wrong reason.
			So(after.FarmMigration.Stage, ShouldNotBeEmpty)
			So(after.FarmMigration.Backup, ShouldNotBeEmpty)

			So(countBackups(), ShouldEqual, 1)

			// And the snapshot is still the one taken before the migration: a
			// re-entered migration must not overwrite the backup with the
			// vault's later state, which is what inverting the idempotence
			// check does.
			edited := `{"version":1,"edited_after_the_backup":true}`
			So(os.WriteFile(filepath.Join(v.Root(), "config.json"), []byte(edited), 0o600), ShouldBeNil)

			st, err = state.Load(v.StatePath())
			So(err, ShouldBeNil)

			st.FarmMigration.Stage = ""
			st.FarmMigration.Done = false
			So(st.Save(v.StatePath()), ShouldBeNil)

			_, err = e.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			kept, err := os.ReadFile(filepath.Join(after.FarmMigration.Backup, "config.json"))
			So(err, ShouldBeNil)
			So(string(kept), ShouldNotContainSubstring, "edited_after_the_backup")
		})
	})
}
