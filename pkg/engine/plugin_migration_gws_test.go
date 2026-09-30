package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/state"
	"github.com/odiumuniverse/beadle/pkg/vault"
	"github.com/odiumuniverse/verger/pkg/digest"
)

// recordingManager claims every path under the vault's plugins directory —
// which is exactly what a real farm link is — and remembers what it was asked
// to adopt.
type recordingManager struct {
	// claims are the prefixes a real manager owns: the cell paths it will
	// write in each host, and the vault tree the farm pointed at.
	claims  []string
	home    string
	adopted []string
	// adoptedAs is the record's own source each key was adopted under.
	adoptedAs []string
	// unresolvable names the keys the library cannot resolve, as a plugin that
	// left its marketplace or lives in a private repository does.
	unresolvable map[string]bool
	ownsCalls    int
	// consents is the library's consent store as beadle sees it: package id to
	// the hook content hash the user approved.
	consents map[string]digest.Hash
	// revoked is every package id the user took consent back from.
	revoked []string
	// applyAfter is a path whose existence means the kinds have already run
	// this sync; appliedAfterKinds records what ApplyPackages saw, and
	// applyCalls how often it was asked.
	applyAfter        string
	appliedAfterKinds bool
	applyCalls        int
	// published is every canon package the engine handed the manager, with the
	// directory it was rendered into and the hosts it was for.
	published []publishedCanon
	// publishErr is what the manager answers with. A real library asks the
	// user before it replaces what a farm wrote, and this fixture can be told
	// to behave as if nothing proved the user's consent.
	publishErr error
	// publishConsents is the authority each publish carried, so a test can see
	// that the upgrade proved one instead of waving every question through.
	publishConsents []state.PublishConsent
}

// publishedCanon is one `PublishCanonPackage` call as the manager saw it.
type publishedCanon struct {
	dir   string
	hosts []string
}

func (m *recordingManager) Home() string { return m.home }

// PublishCanonPackage records what the engine asked the library to publish.
// The real client writes a spec and registers the package; a fixture only has to
// know that it was asked, with what, and for whom.
func (m *recordingManager) PublishCanonPackage(_ context.Context, dir string, hosts []string, consent state.PublishConsent) error {
	if m.publishErr != nil {
		return m.publishErr
	}

	if !consent.Approves("local:beadle-canon", hosts[0]) {
		m.publishErr = errors.New("the fixture refuses a publish with no consent for the canon package")

		return m.publishErr
	}

	m.published = append(m.published, publishedCanon{dir: dir, hosts: hosts})
	m.publishConsents = append(m.publishConsents, consent)

	return nil
}

func (m *recordingManager) Owns(path string) (string, bool) {
	m.ownsCalls++

	for _, claim := range m.claims {
		if strings.HasPrefix(path, claim) {
			return "verger", true
		}
	}

	return "", false
}

// Adopt records the key and the host it was asked to adopt under, because the
// ref the library builds out of them is the thing that decides whether the
// migration can move at all.
func (m *recordingManager) Adopt(_ context.Context, key, source string) error {
	if m.unresolvable[key] {
		return fmt.Errorf("%s: not listed by the oracle", key)
	}

	m.adopted = append(m.adopted, key)
	m.adoptedAs = append(m.adoptedAs, source)

	return nil
}

// farmMachine builds a vault that looks like a machine with a live farm: one
// plugin in the ledger, its pivot, and a link into a host. It returns the
// engine, the vault and the manager.
func farmMachine(t *testing.T, withManager bool) (*engine.Engine, *vault.Vault, string, *recordingManager) {
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

	installDir := filepath.Join(home, ".claude", "plugins", "cache", "acme", "caveman", "0.1.0")
	if err := os.MkdirAll(installDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// The ledger entry the farm wrote, verbatim in shape.
	ledger := map[string]any{
		"version": 1,
		"plugins": map[string]any{
			"acme/caveman": map[string]any{
				"version": "0.1.0",
				"target":  installDir,
				"source":  "claude",
			},
		},
	}

	data, err := json.Marshal(ledger)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Dir(v.PluginsLedgerPath()), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(v.PluginsLedgerPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}

	// The pivot the farm parks the plugin at, with a payload directory the
	// link can point into.
	payload := filepath.Join(v.PluginsDir(), "acme", "caveman", "current", "skills", "caveman")
	if err := os.MkdirAll(payload, 0o700); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(home, ".claude", "skills", "caveman")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(payload, link); err != nil {
		t.Fatal(err)
	}

	opts := []engine.Option{engine.WithHome(home)}

	manager := &recordingManager{
		claims: []string{filepath.Join(v.Root(), "plugins"), filepath.Join(home, ".claude")},
		home:   filepath.Join(v.Root(), "verger"),
	}

	if withManager {
		opts = append(opts, engine.WithPluginManager(manager), engine.WithVergerOwns(manager.Owns))
	}

	e, err := engine.New(v, cfg, agent.All(home, t.TempDir()), opts...)
	if err != nil {
		t.Fatal(err)
	}

	return e, v, home, manager
}

// TestTheFarmMigrationRunsOnceAndIsResumable is the whole of W3-B3 Part A's
// safety claim: the move happens on the first sync, it does not happen twice,
// and a second sync changes nothing.
func TestTheFarmMigrationRunsOnceAndIsResumable(t *testing.T) {
	Convey("Given a machine with a live farm and a plugin manager", t, func() {
		e, v, _, manager := farmMachine(t, true)

		Convey("When the first sync runs", func() {
			report, err := e.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then it adopted the farmed plugin", func() {
				So(manager.adopted, ShouldContain, "acme/caveman")
			})

			Convey("Then it took a backup before removing anything", func() {
				backups, readErr := os.ReadDir(filepath.Join(v.Root(), "state", "backups"))
				So(readErr, ShouldBeNil)
				So(backups, ShouldNotBeEmpty)
			})

			Convey("Then it created the plugin home in the vault", func() {
				info, statErr := os.Stat(manager.home)
				So(statErr, ShouldBeNil)
				So(info.IsDir(), ShouldBeTrue)
			})

			Convey("Then it said what it did", func() {
				So(strings.Join(report.Notes, "\n"), ShouldContainSubstring, "plugin farm")
			})

			Convey("Then the record says the migration is done", func() {
				// GoConvey re-runs the parent closure for every leaf, so this
				// block syncs for itself rather than trusting an outer run.
				_, err := e.Sync(t.Context(), engine.SyncOptions{})
				So(err, ShouldBeNil)

				st, loadErr := state.Load(v.StatePath())
				So(loadErr, ShouldBeNil)
				So(st.FarmMigration, ShouldNotBeNil)
				So(st.FarmMigration.Done, ShouldBeTrue)
				So(st.FarmMigration.Backup, ShouldNotBeEmpty)
				So(st.FarmMigration.Removed, ShouldNotBeEmpty)
			})

			Convey("Then a second sync changes nothing", func() {
				_, err := e.Sync(t.Context(), engine.SyncOptions{})
				So(err, ShouldBeNil)

				before, readErr := os.ReadFile(v.StatePath())
				So(readErr, ShouldBeNil)

				_, err = e.Sync(t.Context(), engine.SyncOptions{})
				So(err, ShouldBeNil)

				after, readErr := os.ReadFile(v.StatePath())
				So(readErr, ShouldBeNil)
				So(string(after), ShouldEqual, string(before))
			})
		})
	})
}

// TestTheFarmIsUntouchedWithoutAManager is the refusal that matters most: a
// beadle that could not open the plugin manager must leave the farm exactly as
// it found it, not half-move it.
func TestTheFarmIsUntouchedWithoutAManager(t *testing.T) {
	Convey("Given a machine with a live farm and no manager", t, func() {
		e, v, _, _ := farmMachine(t, false)

		Convey("When a sync runs", func() {
			report, err := e.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then it says, once, why nothing happened", func() {
				// A note rather than a warning: a machine with no plugin
				// manager is a normal state, and a farm left untouched is the
				// correct outcome — the user is told, not warned.
				So(strings.Join(report.Notes, "\n"), ShouldContainSubstring, "plugin manager is not open")
				So(strings.Join(report.Warnings, "\n"), ShouldNotContainSubstring, "plugin farm")
			})

			Convey("Then no backup was taken and no record was written", func() {
				// These are the migration's own artefacts. Whether the farm
				// pass then retires a plugin it can no longer find is the
				// farm's behaviour and is covered by its own tests; what must
				// be true here is that the migration itself did not run.
				_, backupsErr := os.ReadDir(filepath.Join(v.Root(), "state", "backups"))
				So(os.IsNotExist(backupsErr), ShouldBeTrue)

				st, loadErr := state.Load(v.StatePath())
				So(loadErr, ShouldBeNil)
				So(st.FarmMigration, ShouldBeNil)
			})
		})
	})
}

// A plugin the library cannot resolve - removed from its marketplace, or in a
// private repository - must not make the migration write a fresh backup on
// every run forever, and must not hold the rest of the migration back.
func TestTheFarmSurvivesAPluginTheLibraryCannotResolve(t *testing.T) {
	Convey("Given a farm whose plugin the library refuses to adopt", t, func() {
		e, v, _, manager := farmMachine(t, true)
		manager.unresolvable = map[string]bool{farmPlugin: true}

		countBackups := func() int {
			entries, err := os.ReadDir(filepath.Join(filepath.Dir(v.StatePath()), "state", "backups"))
			So(err, ShouldBeNil)

			return len(entries)
		}

		first, err := e.Sync(t.Context(), engine.SyncOptions{})
		So(err, ShouldBeNil)

		for range 2 {
			_, err := e.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)
		}

		Convey("Then the migration backed the vault up exactly once", func() {
			So(countBackups(), ShouldEqual, 1)
		})

		Convey("Then the plugin is skipped by name, with a reason a user can act on", func() {
			So(joinNotes(first), ShouldContainSubstring, "skipped "+farmPlugin)
			So(joinNotes(first), ShouldContainSubstring, "cannot resolve it")
			So(joinNotes(first), ShouldContainSubstring, "beadle plugins remove "+farmPlugin)
		})

		Convey("Then the stages after the refusal still finished", func() {
			So(joinNotes(first), ShouldContainSubstring, "replaced 1 farm link(s)")
			So(joinNotes(first), ShouldContainSubstring, "removed 1 recorded farm path(s)")
			So(joinNotes(first), ShouldContainSubstring, "the farm migration is finished")
		})

		Convey("Then the skip is recorded, so a later run can still name it", func() {
			st, err := state.Load(v.StatePath())
			So(err, ShouldBeNil)
			So(st.FarmMigration.Skipped, ShouldContainKey, farmPlugin)
			So(st.FarmMigration.Skipped[farmPlugin], ShouldNotBeEmpty)
			So(st.FarmMigration.SkippedAt[farmPlugin].IsZero(), ShouldBeFalse)
		})

		Convey("Then a re-run writes nothing new", func() {
			before := countBackups()
			So(before, ShouldEqual, 1)

			_, err := e.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)
			So(countBackups(), ShouldEqual, before)
		})
	})
}

func joinNotes(report *engine.Report) string {
	var out strings.Builder

	for _, note := range report.Notes {
		out.WriteString(note + "\n")
	}

	for _, warn := range report.Warnings {
		out.WriteString(warn + "\n")
	}

	return out.String()
}
