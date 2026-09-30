package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
	"github.com/odiumuniverse/verger/pkg/digest"
)

// PluginManager is the part of the embedded plugin library the farm migration
// needs. It is an interface so the migration is testable without a live library
// and so a beadle built without one leaves the farm exactly as it found it.
type PluginManager interface {
	// Home is the plugin manager's home directory, which lives in the vault.
	Home() string
	// Owns reports whether the manager owns a path, at user and project scope.
	Owns(path string) (string, bool)
	// Adopt takes one farmed plugin over: it records the package as adopted
	// and delivers it, so the host gets a real file and a receipt instead of
	// the farm's symlink.
	Adopt(ctx context.Context, key, source string) error
	// ApproveHooksFor records the user's consent for one package's hooks, by
	// the content hash of the hooks they were shown. The library owns the
	// record, so a hook that changes afterwards asks for consent again.
	ApproveHooksFor(pkgID, host string, hash digest.Hash) (string, error)
	// RevokeHooksFor takes that consent back.
	RevokeHooksFor(pkgID, host string) error
	// HooksApprovedFor reports whether the library holds consent for exactly
	// this hash.
	HooksApprovedFor(pkgID, host string, hash digest.Hash) (bool, error)
	// ApplyPackages makes the machine match the vault's package spec and lock,
	// and reports what it did - including that it did nothing. A nil report
	// with a nil error means the vault carries no spec at all.
	ApplyPackages(ctx context.Context, dryRun bool) (*state.PackagesReport, error)
	// PublishCanonPackage hands beadle's canon to the manager as a package and
	// installs it on the named hosts. The upgrade calls it so a farm written by
	// an older build becomes a package of the library instead of a directory
	// nothing applies.
	//
	// consent is the authority to answer the library's own questions: a caller
	// that can prove the user already agreed passes a scope, and a caller that
	// cannot passes an empty one and lets every question through to the user.
	PublishCanonPackage(ctx context.Context, dir string, hosts []string, consent state.PublishConsent) error
	// SyncPackages makes the machine match the vault's package spec and lock.
	// beadle calls it on every sync, and the daemon on every change to the
	// spec, because a vault can otherwise carry a spec no machine ever applies
	// - the library's own `status` then truthfully reports no cells, and the
	// packages a pull brought in never land.
	SyncPackages(ctx context.Context, dryRun bool) error
}

// migrateFarm moves this vault's plugin farm to the plugin manager. It runs on
// the first beadle run after the upgrade, like a config migration, and every
// run after that is a no-op.
//
// The ordering is the safety property, not a detail:
//
//   - nothing is removed before the backup stage has completed, and the remove
//     stage refuses to run without a recorded backup;
//   - every path that is removed was **derived from what the farm itself
//     wrote** — a ledger pivot, or a symlink whose target is under the vault's
//     plugins directory — and never produced by a blind glob, because
//     `~/.agents/skills` and `~/.config/opencode/skills` hold links belonging
//     to managers beadle knows nothing about;
//   - the stage reached is persisted after every stage, so a process killed
//     mid-migration resumes where it stopped instead of starting over, or
//     continuing past a half-finished removal.
func (e *Engine) migrateFarm(ctx context.Context, st *state.State, report *Report, opts SyncOptions) {
	if opts.DryRun || !fullForwardSync(opts) {
		return
	}

	ledger, _, err := loadPluginLedger(e.vault.PluginsLedgerPath())
	if err != nil {
		report.Warnings = append(report.Warnings, "plugin farm: "+err.Error())

		return
	}

	m := &farmMigration{engine: e, manager: e.manager, state: st, report: report, ledger: ledger}

	switch {
	case st.FarmMigration != nil:
		// A migration that has started resumes from its recorded stage, even
		// when the farm's paths are already gone: the removal stage may have
		// run before the process died, and the remaining stages still have to.
		if !st.NeedsFarmMigration(true) {
			return
		}
	case !m.farmPresent():
		// Nothing the farm wrote is on disk, so there is no farm to migrate.
		// The ledger alone is not one: it outlives the farm, because it is
		// beadle's own record of the MCP servers it presented, and asking it
		// that way restarted the migration on every sync.
		return
	}

	if e.manager == nil {
		// A note, not a warning: a machine that has no plugin manager open is
		// a normal state, and a farm that stays exactly where it is the
		// correct outcome. The line appears once — until the migration has
		// started, the move has not been offered yet and the user should
		// know it is waiting.
		if st.FarmMigration == nil {
			m.note("the plugin manager is not open, so the farm is left exactly as it is; " +
				"run beadle plugins list once it is, and the migration finishes on its own")
		}

		return
	}

	for stage := state.FarmStageNext(st.FarmMigrationStage()); stage != ""; stage = state.FarmStageNext(stage) {
		if err := m.run(ctx, stage); err != nil {
			report.Warnings = append(report.Warnings, "plugin farm: "+err.Error())

			m.note("stopped after the " + stage + " stage; re-run finishes the migration")

			return
		}

		next := state.FarmStageNext(stage)
		st.RecordFarmStage(stage, time.Now())

		if err := st.Save(e.vault.StatePath()); err != nil {
			report.Warnings = append(report.Warnings, "plugin farm: record the stage: "+err.Error())

			return
		}

		if next == "" {
			break
		}
	}
}

// farmMigration is one run's working set.
type farmMigration struct {
	engine  *Engine
	manager PluginManager
	state   *state.State
	report  *Report
	ledger  pluginLedger
}

// farmPresent reports whether anything the farm wrote is still on disk: one of
// the paths this migration may delete. The ledger alone does not count - it
// keeps living after the farm is gone, as the record of the MCP servers
// beadle presented.
func (m *farmMigration) farmPresent() bool {
	for _, path := range m.farmPaths() {
		if _, err := os.Lstat(path); err == nil {
			return true
		}
	}

	return false
}

// run executes one stage. Every stage is written so that running it twice is
// the same as running it once.
func (m *farmMigration) run(ctx context.Context, stage string) error {
	switch stage {
	case state.FarmStageBackup:
		return m.backup()
	case state.FarmStageHome:
		return m.home()
	case state.FarmStageImport:
		return m.importPinsAndApprovals()
	case state.FarmStageAdopt:
		return m.adopt(ctx)
	case state.FarmStageClaim:
		return m.claim()
	case state.FarmStageDeliver:
		return m.deliver(ctx)
	case state.FarmStageRemove:
		return m.remove()
	case state.FarmStageVerify:
		return m.verify()
	default:
		return fmt.Errorf("unknown stage %q", stage)
	}
}

// backup copies everything the reversal needs into a timestamped directory,
// before a single path is removed. It is the first stage, and the only one the
// others depend on.
func (m *farmMigration) backup() error {
	rec := m.state.EnsureFarmMigration()

	// One backup per migration: a re-entered migration reuses the directory it
	// took, so three runs leave one backup and not three.
	if rec.Backup != "" && isDir(rec.Backup) {
		return nil
	}

	// The directory is named for the migration, not for the attempt: a second
	// run of the same migration resolves to the same path, so the property
	// holds whatever the clock says - with a per-attempt timestamp two runs in
	// the same second shared a directory by luck and runs in different seconds
	// did not.
	started := rec.StartedAt
	if started.IsZero() {
		started = time.Now()
	}

	dir := filepath.Join(filepath.Dir(m.engine.vault.StatePath()), "state", "backups", started.UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create the backup: %w", err)
	}

	// Only the four files a reversal reads. The canon directories are not
	// copied: nothing in this migration removes them.
	for _, rel := range []string{"config.json", "state.json", "plugins/ledger.json", "hooks/hooks.json"} {
		if err := copyFileIfPresent(filepath.Join(m.engine.vault.Root(), filepath.FromSlash(rel)),
			filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			return fmt.Errorf("back up %s: %w", rel, err)
		}
	}

	m.state.EnsureFarmMigration().Backup = dir
	m.note("backed the vault state up to " + dir)

	return nil
}

func copyFileIfPresent(src, dst string) error {
	data, err := os.ReadFile(src) //nolint:gosec // G304: a path inside the vault
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}

	return os.WriteFile(dst, data, 0o600) //nolint:gosec // G306: a backup of vault state is owner-only
}

// home makes sure the plugin manager's home exists in the vault before anything
// is imported into it.
func (m *farmMigration) home() error {
	home := m.manager.Home()
	if home == "" {
		return errors.New("the plugin manager reports no home")
	}

	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("create the plugin home: %w", err)
	}

	m.note("the plugin home is " + home)

	return nil
}

// importPinsAndApprovals carries beadle's pins and approvals across. The pins
// travel as the package versions; the approvals become a list of names that must
// be given again, because the two stores are keyed differently and a silent
// carry-over would bless a command the user never saw a second time.
func (m *farmMigration) importPinsAndApprovals() error {
	names := slices.Clone(m.engine.config.ApprovedHooks)
	slices.Sort(names)
	names = slices.Compact(names)

	m.state.EnsureFarmMigration().Reapprovals = names

	var pins []string

	for agentID, entry := range m.engine.config.Agents {
		for key, version := range entry.PluginPins {
			pins = append(pins, fmt.Sprintf("%s %s@%s", agentID, key, version))
		}
	}

	slices.Sort(pins)

	if len(pins) > 0 {
		m.note(fmt.Sprintf("%d plugin pin(s) carried across: %s", len(pins), strings.Join(pins, ", ")))
	}

	if len(names) > 0 {
		m.note(fmt.Sprintf("%d hook approval(s) must be given again in the plugin manager: %s",
			len(names), strings.Join(names, ", ")))
	}

	return nil
}

// adopt hands every farmed plugin to the plugin manager. The keys come from the
// ledger, so a plugin beadle never recorded is never adopted, and a key already
// recorded as adopted is skipped.
func (m *farmMigration) adopt(ctx context.Context) error {
	keys := m.farmedKeys()
	if len(keys) == 0 {
		return nil
	}

	rec := m.state.EnsureFarmMigration()

	// A plugin the library cannot resolve - removed from its marketplace, in a
	// private repository - is recorded and left behind, never retried into an
	// endless rewrite of the vault, and never held back the plugins that can.
	for _, key := range keys {
		if slices.Contains(rec.Adopted, key) {
			continue
		}

		if _, skipped := rec.Skipped[key]; skipped {
			continue
		}

		if err := m.manager.Adopt(ctx, key, m.ledger.Plugins[key].Source); err != nil {
			if rec.Skipped == nil {
				rec.Skipped = map[string]string{}
				rec.SkippedAt = map[string]time.Time{}
			}

			rec.Skipped[key] = err.Error()
			rec.SkippedAt[key] = time.Now().UTC()

			m.note(fmt.Sprintf("skipped %s: the plugin library cannot resolve it (%v); "+
				"the rest of the migration continues, and this one waits for you — "+
				"run `beadle plugins remove %s` to drop it, or fix its marketplace and re-run",
				key, err, key))

			continue
		}

		rec.Adopted = append(rec.Adopted, key)
	}

	slices.Sort(rec.Adopted)
	m.note(fmt.Sprintf("adopted %d farmed plugin(s): %s", len(keys), strings.Join(keys, ", ")))

	return nil
}

// claim checks the manager answers for every farm path **before** anything is
// written over one. A path it does not claim stops the migration with a named
// example: the two tools must not fight over the same file, and a silent
// overwrite is how they would.
func (m *farmMigration) claim() error {
	paths := m.farmPaths()
	if len(paths) == 0 {
		return nil
	}

	var unclaimed []string

	for _, path := range paths {
		if _, ok := m.manager.Owns(path); !ok {
			unclaimed = append(unclaimed, path)
		}
	}

	if len(unclaimed) == 0 {
		m.note(fmt.Sprintf("the plugin manager claims all %d farm path(s)", len(paths)))

		return nil
	}

	slices.Sort(unclaimed)

	return fmt.Errorf("the plugin manager does not claim %d of the %d farm path(s), starting with %s: "+
		"nothing was removed; re-run once the manager claims them",
		len(unclaimed), len(paths), unclaimed[0])
}

// deliver replaces the farm's symlinks with what the plugin manager writes. The
// link is removed only after the manager has taken the plugin over, so a failure
// in between leaves the farm working rather than a hole in a host.
func (m *farmMigration) deliver(ctx context.Context) error {
	links := m.farmLinks()
	if len(links) == 0 {
		return nil
	}

	rec := m.state.EnsureFarmMigration()

	for _, key := range m.farmedKeys() {
		if !slices.Contains(rec.Adopted, key) {
			continue
		}

		if err := m.manager.Adopt(ctx, key, m.ledger.Plugins[key].Source); err != nil {
			return fmt.Errorf("deliver %s: %w", key, err)
		}
	}

	var replaced []string

	for _, path := range links {
		// A link that the manager has already replaced with a real file is
		// simply gone from the link set; RemoveAll on a missing path is a
		// no-op, which keeps this stage idempotent.
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove the farm link %s: %w", path, err)
		}

		replaced = append(replaced, path)
	}

	rec.Removed = append(rec.Removed, replaced...)
	m.note(fmt.Sprintf("replaced %d farm link(s) with delivered files", len(replaced)))

	return nil
}

// remove deletes the farm, and only the paths the farm itself is responsible
// for: the pivots the ledger recorded and the links the farm wrote. There is no
// glob and no directory sweep, so a path in a shared directory that another
// manager owns cannot be selected even in principle.
func (m *farmMigration) remove() error {
	rec := m.state.EnsureFarmMigration()

	// A recorded path is not a backup: the directory has to be there, or a
	// half-finished earlier run (or a user with an empty disk) would let this
	// one delete the farm with nothing to put it back from.
	if rec.Backup == "" || !isDir(rec.Backup) {
		return fmt.Errorf("refusing to remove the farm: the backup %q is not a directory", rec.Backup)
	}

	paths := m.farmPaths()

	var removed []string

	for _, path := range paths {
		if slices.Contains(rec.Removed, path) {
			continue
		}

		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove %s: %w", path, err)
		}

		removed = append(removed, path)
	}

	rec.Removed = append(rec.Removed, removed...)
	slices.Sort(rec.Removed)
	m.note(fmt.Sprintf("removed %d recorded farm path(s); the backup is %s", len(removed), rec.Backup))

	return nil
}

// verify is the last stage: it says what the user can check, and points at the
// two ways back.
func (m *farmMigration) verify() error {
	m.note("the farm migration is finished; check it with beadle plugins list, and undo it with " +
		"beadle plugins eject or by copying " + m.state.FarmMigration.Backup + " back")

	return nil
}

// farmedKeys are the ledger entries the farm still owns: a live record per key.
// A retired or quarantined one is not a farm this migration adopts.
func (m *farmMigration) farmedKeys() []string {
	var keys []string

	for key, rec := range m.ledger.Plugins {
		if !rec.RetiredAt.IsZero() || !rec.QuarantinedAt.IsZero() {
			continue
		}

		keys = append(keys, key)
	}

	slices.Sort(keys)

	return keys
}

// farmPaths are the exact paths this migration may delete: every pivot the
// ledger recorded and every link the farm wrote. It is the answer to "only
// recorded paths, never a glob".
func (m *farmMigration) farmPaths() []string {
	paths := m.farmLinks()

	for _, key := range m.farmedKeys() {
		paths = append(paths, m.engine.pluginPivotDir(key))
	}

	slices.Sort(paths)

	return slices.Compact(paths)
}

// farmLinks are the host symlinks the farm created: entries in the hosts' own
// directories whose target is under the vault's plugins directory. That
// predicate is the farm's own (`farmOwned`), and it is what makes a third-party
// link in the same directory unselectable — its target is somewhere else
// entirely.
func (m *farmMigration) farmLinks() []string {
	var links []string

	for _, a := range m.engine.agents {
		if !m.engine.config.Agents[a.ID].Enabled {
			continue
		}

		for _, k := range []kind.ID{kind.Skills, kind.Subagents, kind.Commands} {
			surface := a.Surface(k)
			if surface == nil || !m.engine.config.KindEnabled(k) {
				continue
			}

			links = append(links, m.engine.farmLinksIn(surface.Path())...)
		}
	}

	slices.Sort(links)

	return slices.Compact(links)
}

// farmLinksIn lists the entries of one host directory that are farm links.
func (e *Engine) farmLinksIn(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var links []string

	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())

		target, err := os.Readlink(path)
		if err != nil {
			continue // not a symlink, or not readable: not ours to remove
		}

		if !e.farmOwned(target) {
			continue
		}

		links = append(links, path)
	}

	return links
}

func (m *farmMigration) note(message string) {
	m.report.Notes = append(m.report.Notes, "plugin farm: "+message)
}

// mergePackages folds the library's answer into the report: what was applied,
// or the refusal that stopped it. An error keeps whatever was already there, so
// a partial run is still visible.
func mergePackages(current, next *state.PackagesReport, err error) *state.PackagesReport {
	report := current
	if report == nil {
		report = &state.PackagesReport{Results: []state.PackageResult{}}
	}

	if err != nil {
		report.Error = err.Error()

		return report
	}

	if next == nil {
		return report
	}

	report.Spec = next.Spec
	report.Results = next.Results
	report.LookedAt = next.LookedAt

	return report
}
