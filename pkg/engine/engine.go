package engine

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vmkteam/embedlog"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/digest"
	"github.com/odiumuniverse/beadle/pkg/history"
	"github.com/odiumuniverse/beadle/pkg/hooks"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/lock"
	"github.com/odiumuniverse/beadle/pkg/memory"
	"github.com/odiumuniverse/beadle/pkg/plugin"
	proj "github.com/odiumuniverse/beadle/pkg/project"
	"github.com/odiumuniverse/beadle/pkg/rulings"
	"github.com/odiumuniverse/beadle/pkg/secret"
	"github.com/odiumuniverse/beadle/pkg/state"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

const lockWait = 30 * time.Second

type Engine struct {
	vault        *vault.Vault
	store        *cas.Store
	config       *config.Config
	agents       []*agent.Agent
	secrets      *secret.Store
	keyring      secret.Keyring
	log          embedlog.Logger
	now          func() time.Time
	home         string
	cwd          string
	policy       proj.Policy
	policyLoaded bool
	rulings      *rulings.Ledger
	rulingsDirty bool
	autoBundles  bool
	// unattended marks the watcher: a service manager may start it without
	// the user's shell PATH, so it resolves host CLIs through the locations
	// attended runs recorded and never records its own.
	unattended bool
	// skillCache keeps the digests one process resolved, on top of the
	// persisted state cache; skillCacheOff disables both, so every scan reads
	// the trees again (tests and diagnosis).
	skillCache    map[string]state.SkillTree
	skillCacheOff bool
	// owns reports the paths the plugin manager owns (verger, user and project
	// scope). It is the DESIGN ownership invariant: an artifact verger delivers
	// is never pulled into beadle's canon. A nil owns means nothing is owned by
	// anyone else, which is the state of a beadle that has not opened the
	// plugin manager.
	owns func(path string) (string, bool)
	// manager is the embedded plugin manager the farm migration needs. A nil
	// manager means the migration does not run and the farm stays exactly as
	// it is.
	manager PluginManager
}

type Option func(*Engine)

func WithLogger(log embedlog.Logger) Option {
	return func(e *Engine) { e.log = log }
}

func WithClock(now func() time.Time) Option {
	return func(e *Engine) { e.now = now }
}

func WithHome(home string) Option {
	return func(e *Engine) { e.home = home }
}

func WithCwd(cwd string) Option {
	return func(e *Engine) { e.cwd = cwd }
}

func WithKeyring(keyring secret.Keyring) Option {
	return func(e *Engine) { e.keyring = keyring }
}

// WithBundleAutoEnable lets a full forward sync make one unattended bundle
// enable attempt per untouched host. The CLI sets it; the plain engine API
// stays explicit, so tests and library callers never register anything with a
// host CLI on their own.
func WithBundleAutoEnable() Option {
	return func(e *Engine) { e.autoBundles = true }
}

// WithUnattended marks a run nobody watches (the watcher): it never records
// where it finds a host CLI, because its PATH is the service manager's, not
// the user's.
// WithPluginManager installs the embedded plugin manager. The farm migration
// runs on the first sync after the upgrade and needs it; without it beadle
// leaves the farm untouched rather than half-moving it.
func WithPluginManager(manager PluginManager) Option {
	return func(e *Engine) { e.manager = manager }
}

// WithVergerOwns installs the plugin manager's ownership predicate. Every
// kind's pull consults it, so a path the plugin manager owns stays out of the
// canon at both user and project scope.
func WithVergerOwns(owns func(path string) (string, bool)) Option {
	return func(e *Engine) { e.owns = owns }
}

func WithUnattended() Option {
	return func(e *Engine) { e.unattended = true }
}

// WithSkillCacheDisabled turns the skill manifest cache off, so every scan
// reads and hashes the trees again. Tests use it to prove the cached and
// uncached scans decide identically.
func WithSkillCacheDisabled() Option {
	return func(e *Engine) { e.skillCacheOff = true }
}

func New(v *vault.Vault, cfg *config.Config, agents []*agent.Agent, opts ...Option) (*Engine, error) {
	e := &Engine{
		vault:  v,
		store:  cas.NewStore(v.ObjectsDir()),
		config: cfg,
		agents: agents,
		now:    time.Now,
	}

	for _, opt := range opts {
		opt(e)
	}

	if e.cwd == "" {
		if cwd, err := os.Getwd(); err == nil {
			e.cwd = cwd
		}
	}

	secrets, err := secret.Load(v.SecretsPath(), secret.WithKeyring(e.keyring))
	if err != nil {
		return nil, err
	}

	e.secrets = secrets

	e.ignorePluginRoots()

	return e, nil
}

// ignorePluginRoots keeps plugin install areas out of the canon sync: a farm
// symlink in a skills directory resolves into the install path of some host,
// and that resolved copy is a read-only plugin presentation, not a skill the
// vault should adopt.
func (e *Engine) ignorePluginRoots() {
	if e.home == "" {
		return
	}

	roots := []string{filepath.Clean(e.vault.PluginsDir())}

	for _, source := range plugin.SourceHosts() {
		roots = append(roots, e.pluginSourceRoots(source)...)
	}

	for _, a := range e.agents {
		surface := a.Surface(kind.Skills)
		if surface == nil {
			continue
		}

		if ignore, ok := surface.(agent.SkillIgnoreRoots); ok {
			ignore.AddSkillIgnoreRoots(roots...)
		}
	}
}

func (e *Engine) Agents() []*agent.Agent {
	return slices.Clone(e.agents)
}

func (e *Engine) Config() *config.Config {
	return e.config
}

func (e *Engine) Vault() *vault.Vault {
	return e.vault
}

type SyncOptions struct {
	DryRun    bool
	Kinds     []kind.ID
	Direction config.Mode
	Refresh   bool
}

func (e *Engine) Sync(ctx context.Context, opts SyncOptions) (*Report, error) {
	release, err := e.lock(ctx)
	if err != nil {
		return nil, err
	}

	defer release()

	return e.sync(ctx, opts)
}

func (e *Engine) sync(ctx context.Context, opts SyncOptions) (*Report, error) {
	if err := e.mergetoolGuard(); err != nil {
		return nil, err
	}

	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		return nil, err
	}

	active, err := e.activeAgents(ctx)
	if err != nil {
		return nil, err
	}

	// "No agent" means no HOST agent: beadle's own shared skills surface is
	// always on and is a directory beadle owns, not a host whose files the user
	// could have removed. Counting it would make every fresh vault look like it
	// had something to be in sync with.
	noActiveAgents := true

	for _, a := range active {
		if a.ID != agent.SharedID {
			noActiveAgents = false

			break
		}
	}

	report := &Report{DryRun: opts.DryRun, NoActiveAgents: noActiveAgents}

	// The delivery record ("beadle wrote this file here") is a fact about THIS
	// machine's host directories, so the state carries the machine it belongs
	// to. The record is machine-local and the vault's .gitignore keeps it out
	// of git — but a vault copied with `cp -a`, or a state file carried by
	// hand, arrives stamped with someone else's home. Reading that record here
	// would turn "this host has no such file yet" into "the user deleted it",
	// and report a mass deletion of beadle's own skills at a user who never
	// had them.
	// The stamp happens at the commit, not here: the kind loop below reads this
	// very record to tell "beadle wrote this here" from "another machine did",
	// so overwriting it first would make the question unaskable.
	if !e.deliveryRecordIsOurs(st) {
		report.Notes = append(report.Notes, fmt.Sprintf(
			"delivery record belongs to another machine (%s): beadle wrote nothing on this one, "+
				"so every file is new here", st.Home))
	}

	// One note for the whole run, because the fact is about the vault and not
	// about any one kind: there is nobody to write to, and the way to change
	// that is a command, not a per-kind exception.
	if report.NoActiveAgents {
		report.Notes = append(report.Notes, "no agent is on for this vault (run `beadle agents enable <agent>`)")
	}

	e.warnKeyring(report)
	e.noteConfigMigration(report, opts)
	e.migrateAgentIDs(report, opts)
	e.beginRulings(opts)

	// A farm an older build wrote is taken FIRST — copied, and stripped of the
	// files beadle wrote — because this build writes `bundles/<host>` too and a
	// stale-file sweep later in the sync would delete a file the user owns before
	// anyone had copied it. The other half of the move, publishing the canon as a
	// package, runs after the canon package is rendered.
	farm := e.takeBundleFarm(st, report, opts)

	e.runSyncMigrations(ctx, st, report, opts)
	e.syncKinds(ctx, report, active, st, opts)

	e.liftRulings(report)

	e.autoEnableAndRefreshBundles(ctx, active, st, report, opts)

	// The plugin library reconciles the machine with the vault's spec and lock,
	// and it runs LAST: after the kinds pulled, adopted and rendered, so a spec
	// or a lock that arrived in this pass is applied by the same pass that
	// reports on it. Run earlier it answered about a vault state the sync had
	// not reached yet - a run that delivered a package still printed "the vault
	// carries no package spec". The result is a section of the report either
	// way, including "nothing to do": a silent no-op is how a vault ends up
	// carrying a spec no machine applies.
	if e.manager != nil {
		pkgReport, err := e.manager.ApplyPackages(ctx, opts.DryRun)

		report.Packages = mergePackages(report.Packages, pkgReport, err)
	}

	e.renderCanonPackage(ctx, report, opts)

	// The canon the farm becomes is published here: the package it comes from
	// is rendered just above, and the library applies it in the same pass.
	if farm != nil {
		if err := farm.publish(ctx); err != nil {
			report.Warnings = append(report.Warnings, "bundles: "+err.Error())
		}
	}

	e.noteSkillReferencesIfFull(st, active, report, opts)

	e.presentHooks(st, report, active, opts)

	e.deliverHookModules(ctx, st, report, active, opts)

	e.syncDigest(ctx, report, active, st, opts)

	report.Conflicts = st.OpenConflicts()

	if opts.DryRun {
		return report, nil
	}

	return e.commitSync(ctx, st, report, opts)
}

// takeBundleFarm copies a farm an older build wrote and strips beadle's own
// files from it, before anything this build renders can prune the directory. It
// returns nil when there is nothing to take, and the second half of the move —
// publishing the canon as a package — runs after the canon package is rendered.
func (e *Engine) takeBundleFarm(st *state.State, report *Report, opts SyncOptions) *bundleFarm {
	if e.manager == nil || opts.DryRun {
		return nil
	}

	farm := &bundleFarm{engine: e, state: st, report: report}
	if err := farm.take(); err != nil {
		report.Warnings = append(report.Warnings, "bundles: "+err.Error())
	}

	return farm
}

// runSyncMigrations is the part of the sync that only reads the vault as the
// user left it. It runs before the plugin pass on purpose: the farm migration
// and the hook-consent migration must both see the pre-pass state, because
// this run's own reconcile rewrites the ledger and retires what it no longer
// finds.
func (e *Engine) runSyncMigrations(ctx context.Context, st *state.State, report *Report, opts SyncOptions) {
	// The farm migration runs BEFORE the plugin pass on purpose: it must see
	// the farm exactly as the user left it, because this run's own reconcile
	// rewrites the ledger and retires what it no longer finds. Migrating after
	// it would migrate a ledger this process had already changed.
	e.migrateFarm(ctx, st, report, opts)

	// Hook consent moves into the plugin library the same way: before the
	// plugin pass reads the canon, so the pass already sees the library's
	// answer, and never before the user is told about it.
	if !opts.DryRun && fullForwardSync(opts) {
		if canon, err := hooks.Load(e.vault.HooksPath()); err == nil {
			if err := e.migratePluginHookConsent(canon, report); err != nil {
				report.Warnings = append(report.Warnings, err.Error())
			}
		}
	}

	// A vault in git carries what another machine needs and nothing that is
	// true of this one. A vault committed before those rules still tracks the
	// machine-local state and the secrets file, and `.gitignore` alone cannot
	// untrack a path - so the migration does it, once, and says what it
	// stopped tracking. Nothing is committed and nothing is deleted: the files
	// stay on this machine, and the history is the user's to deal with (the
	// doctor says so, and names the command).
	if !opts.DryRun && fullForwardSync(opts) {
		untracked, err := e.vault.UntrackMachineLocal(ctx)
		if err != nil {
			report.Warnings = append(report.Warnings, err.Error())
		} else if len(untracked) > 0 {
			report.Notes = append(report.Notes, fmt.Sprintf(
				"stopped tracking %d machine-local file(s) (they stay on this machine): %s",
				len(untracked), strings.Join(untracked, ", ")))
		}
	}

	if !opts.DryRun {
		report.Warnings = append(report.Warnings, e.recordHostCLIs()...)
	}
}

// syncKinds pulls, adopts and renders every selected kind, and reports the
// secrets that moved into the vault store while it did.
func (e *Engine) syncKinds(ctx context.Context, report *Report, active []*agent.Agent, st *state.State, opts SyncOptions) {
	// A literal secret that leaves a host note is something the user has to
	// see without asking for logs: their file was rewritten and the value now
	// lives in the vault store. The note counts values that *entered* the
	// store this run, so a note that is already guarded does not repeat the
	// warning on every sync; the per-run scan stays in the log.
	secretsBefore := len(e.secrets.Names())

	for _, spec := range kind.All() {
		if !e.config.KindEnabled(spec.ID) || !selected(opts.Kinds, spec.ID) {
			continue
		}

		report.Kinds = append(report.Kinds, e.syncKind(ctx, spec, active, st, opts))
	}

	if stored := len(e.secrets.Names()) - secretsBefore; stored > 0 && !opts.DryRun {
		report.Notes = append(report.Notes, fmt.Sprintf("note secrets moved to the vault store count=%d", stored))
	}
}

// autoEnableAndRefreshBundles renders the bundles after the kinds have merged
// the canon, so the rendered version stays stable within one sync.
func (e *Engine) autoEnableAndRefreshBundles(ctx context.Context, active []*agent.Agent, st *state.State, report *Report, opts SyncOptions) {
	var autoHandled map[string]bool

	if e.autoBundles && !opts.DryRun && fullForwardSync(opts) {
		// The unattended attempt runs after the kind loop: the bundle then
		// renders from the merged canon (host edits were adopted first) and
		// from the same host state refresh will see, so the rendered version
		// stays stable within the sync.
		handled, err := e.autoEnableBundles(ctx, active, st, report)
		if err != nil {
			report.Warnings = append(report.Warnings, "bundles: "+err.Error())
		}

		autoHandled = handled
	}

	e.refreshBundles(ctx, st, report, opts, autoHandled)
}

func (e *Engine) commitSync(ctx context.Context, st *state.State, report *Report, opts SyncOptions) (*Report, error) {
	if err := e.secrets.Save(); err != nil {
		return report, err
	}

	e.pruneSkillTrees(st)

	// The record this run is about to save is this machine's: the bases written
	// above describe what this run wrote to this machine's host directories,
	// and the stamp is what makes the next run on this machine able to read
	// them as its own.
	if e.home != "" {
		st.Home = e.home
	}

	if err := st.Save(e.vault.StatePath()); err != nil {
		return report, err
	}

	for _, note := range st.TakeMigrationNotes() {
		report.Warnings = append(report.Warnings, "state: "+note)
	}

	if err := e.finishRulings(report); err != nil {
		report.Warnings = append(report.Warnings, "rulings: "+err.Error())
	}

	if err := e.writeConflictFiles(st); err != nil {
		report.Warnings = append(report.Warnings, "conflict files: "+err.Error())
	}

	if e.memoryCleanupEnabled(opts) {
		e.cleanMemoryIgnore(report)
	}

	if report.VaultChanged() {
		e.commitHistory(ctx)
	}

	return report, nil
}

func (e *Engine) warnKeyring(report *Report) {
	if err := e.secrets.KeyringErr(); err != nil {
		report.Warnings = append(report.Warnings, "keyring unavailable: "+err.Error())
	}
}

// noteConfigMigration persists the in-memory config v3 flips and reports the
// notes that were not rendered yet; a dry run leaves both the file and the
// notes alone.
func (e *Engine) noteConfigMigration(report *Report, opts SyncOptions) {
	if opts.DryRun || !e.config.Migrated() {
		return
	}

	e.approveCanonHooks(report)

	if err := e.config.Save(e.vault.ConfigPath()); err != nil {
		report.Warnings = append(report.Warnings, "config: cannot persist the migrated config: "+err.Error())

		return
	}

	for _, note := range e.config.TakeMigrationNotes() {
		report.Warnings = append(report.Warnings, "config: "+note)
	}
}

// ApproveCanonHooks lifts the one-time trust for hooks that were already in
// the vault canon when beadle started gating approvals: they ran on the host
// before the v3 migration, so approving each one by hand would be busywork.
// It is a no-op unless the config was just migrated; new hooks and
// plugin-sourced hooks still need `beadle hooks approve`.
func (e *Engine) ApproveCanonHooks(report *Report) {
	if !e.config.Migrated() {
		return
	}

	e.approveCanonHooks(report)
}

func (e *Engine) approveCanonHooks(report *Report) {
	canon, err := hooks.Load(e.vault.HooksPath())
	if err != nil {
		report.Warnings = append(report.Warnings, "hooks: cannot read the canon: "+err.Error())

		return
	}

	var approved []string

	for name, hook := range canon {
		// A plugin hook is the library's to approve; beadle's own list never
		// takes it, and the v3 auto-approval must not put it back.
		if _, fromPlugin := hook.PluginKey(); fromPlugin {
			continue
		}

		if e.config.HookApproved(name) {
			continue
		}

		e.config.ApproveHook(name)
		approved = append(approved, name)
	}

	if len(approved) == 0 {
		return
	}

	slices.Sort(approved)

	report.Notes = append(report.Notes, fmt.Sprintf(
		"hooks: approved %d canon hook(s) as part of the one-time migration (%s); revoke with `beadle hooks revoke <name>`",
		len(approved), strings.Join(approved, ", ")))
}

func (e *Engine) memoryCleanupEnabled(opts SyncOptions) bool {
	return !opts.DryRun && e.config.KindEnabled(kind.Memory) && selected(opts.Kinds, kind.Memory)
}

func (e *Engine) cleanMemoryIgnore(report *Report) {
	items, _, err := loadNotesDir(e.vault.MemoryDir())
	if err != nil {
		report.Warnings = append(report.Warnings, "memory gitignore: "+err.Error())

		return
	}

	for _, key := range slices.Sorted(maps.Keys(items)) {
		if len(secret.ScanText(items[key])) > 0 {
			report.Warnings = append(report.Warnings, "memory stays ignored: possible secret in note "+key)

			return
		}
	}

	if err := e.vault.RemoveLegacyIgnore(); err != nil {
		report.Warnings = append(report.Warnings, "memory gitignore: "+err.Error())
	}
}

func (e *Engine) ActiveAgents(ctx context.Context) ([]*agent.Agent, error) {
	return e.activeAgents(ctx)
}

func (e *Engine) activeAgents(ctx context.Context) ([]*agent.Agent, error) {
	var active []*agent.Agent

	for _, a := range e.agents {
		if !e.config.Agents[a.ID].Enabled {
			continue
		}

		detected, err := a.Detect()
		if err != nil {
			return nil, fmt.Errorf("detect %s: %w", a.ID, err)
		}

		if !detected {
			e.log.Print(ctx, "agent enabled but not detected, skipping", "agent", a.ID)

			continue
		}

		active = append(active, a)
	}

	return active, nil
}

func (e *Engine) lock(ctx context.Context) (func(), error) {
	waitCtx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()

	release, err := lock.Acquire(waitCtx, e.vault.LockPath())
	if err != nil {
		return nil, err
	}

	return func() {
		if err := release(); err != nil {
			e.log.Error(ctx, "release vault lock", "err", err)
		}
	}, nil
}

func (e *Engine) commitHistory(ctx context.Context) {
	if e.config.HistoryMode() != config.HistoryGit {
		return
	}

	message := "beadle: sync " + e.now().Format("2006-01-02 15:04:05")

	if err := history.Commit(ctx, e.vault.Root(), message, e.log); err != nil {
		e.log.Error(ctx, "vault history failed", "err", err)
	}
}

func (e *Engine) WatchPaths(ctx context.Context) ([]string, error) {
	paths := []string{
		e.vault.RulesPath(),
		e.vault.ServersPath(),
		e.vault.SkillsDir(),
		e.vault.MemoryDir(),
		e.vault.ProjectsDir(),
		e.vault.PermissionsPath(),
	}

	active, err := e.activeAgents(ctx)
	if err != nil {
		return nil, err
	}

	for _, a := range active {
		for _, surface := range a.Surfaces {
			if !e.config.KindEnabled(surface.Kind()) || !e.surfaceEnabled(surface) {
				continue
			}

			if e.config.ModeFor(a.ID, surface.Kind(), surface.Traits().DefaultMode) == config.ModeOff {
				continue
			}

			paths = append(paths, surface.WatchPaths()...)
		}
	}

	if e.home != "" {
		paths = appendUnique(paths, filepath.Join(e.home, pluginRegistryPath))

		marketplaces, err := filepath.Glob(filepath.Join(e.home, pluginMarketplaceDir, "*"))
		if err != nil {
			return nil, fmt.Errorf("glob plugin marketplaces: %w", err)
		}

		for _, marketplace := range marketplaces {
			paths = appendUnique(paths, filepath.Join(marketplace, ".git", "HEAD"))
		}
	}

	return paths, nil
}

func appendUnique(paths []string, path string) []string {
	if slices.Contains(paths, path) {
		return paths
	}

	return append(paths, path)
}

func selected(kinds []kind.ID, k kind.ID) bool {
	return len(kinds) == 0 || slices.Contains(kinds, k)
}

type DigestAction string

const (
	// DigestCreated marks a fence-only file created by the digest phase.
	DigestCreated DigestAction = "created"
	// DigestRefreshed marks a written or replaced digest block.
	DigestRefreshed DigestAction = "refreshed"
	// DigestRemoved marks a removed digest block.
	DigestRemoved DigestAction = "removed"
	// DigestHeld marks a frozen block with manual edits inside.
	DigestHeld DigestAction = "held"
	// DigestAdopted marks an existing block taken as a baseline.
	DigestAdopted DigestAction = "adopted"
	// DigestPruned marks dropped Renders/Drift entries.
	DigestPruned DigestAction = "pruned"
	// DigestWouldCreate is the dry-run preview of DigestCreated.
	DigestWouldCreate DigestAction = "would-create"
	// DigestWouldRefresh is the dry-run preview of DigestRefreshed.
	DigestWouldRefresh DigestAction = "would-refresh"
	// DigestWouldRemove is the dry-run preview of DigestRemoved.
	DigestWouldRemove DigestAction = "would-remove"
	// DigestWouldPrune is the dry-run preview of DigestPruned.
	DigestWouldPrune DigestAction = "would-prune"
	// DigestWouldHold is the dry-run preview of DigestHeld.
	DigestWouldHold DigestAction = "would-hold"
	DigestSkipped   DigestAction = "skipped"
)

// DigestResult reports one digest-phase decision for one project file.
type DigestResult struct {
	Agent  string       `json:"agent"`
	Path   string       `json:"path"`
	Action DigestAction `json:"action"`
}

type fencedSurface interface {
	WriteFenced(ctx context.Context, block []byte) error
}

type projectTarget struct {
	agent     *agent.Agent
	surface   agent.Surface
	id        string
	rel       string
	notesSlug string
	key       string
	notesDir  string
}

func (t projectTarget) path() string { return t.surface.Path() }

func (t projectTarget) active() bool {
	projector, ok := t.surface.(agent.Projector)
	if !ok {
		return false
	}

	_, _, visible := projector.Project(t.key, nil)

	return visible
}

func (e *Engine) projectTargets(active []*agent.Agent) []projectTarget {
	var targets []projectTarget

	identity := e.projectIdentity()
	// One digest target per file: several hosts read the same project rules
	// (AGENTS.md), and the first eligible surface owns the single writer — the
	// same surface the owners dedup picks for the kind.
	seen := map[string]bool{}

	for _, a := range active {
		for _, surface := range a.SurfacesOf(kind.Projects) {
			file, ok := surface.(agent.ProjectFile)
			if !ok {
				continue
			}

			if _, fenced := surface.(fencedSurface); !fenced {
				continue
			}

			if !e.surfaceEnabled(surface) {
				continue
			}

			if e.config.ModeFor(a.ID, kind.Projects, surface.Traits().DefaultMode) == config.ModeOff {
				continue
			}

			realPath := agent.RealPath(surface.Path())
			if seen[realPath] {
				continue
			}

			seen[realPath] = true

			dir := filepath.Dir(surface.Path())
			slug := memory.Slug(dir)

			targets = append(targets, projectTarget{
				agent:     a,
				surface:   surface,
				id:        identity.ID,
				rel:       file.ProjectRel(),
				notesSlug: slug,
				key:       identity.ID + "/" + file.ProjectRel(),
				notesDir:  filepath.Join(e.home, ".claude", "projects", slug, "memory"),
			})
		}
	}

	return targets
}

func (e *Engine) syncDigest(ctx context.Context, report *Report, active []*agent.Agent, st *state.State, opts SyncOptions) {
	if opts.Direction == config.ModePull || e.home == "" ||
		!e.config.KindEnabled(kind.Projects) || !selected(opts.Kinds, kind.Projects) {
		return
	}

	vaultItems, _, err := e.loadVault(kind.Memory)
	if err != nil {
		report.Warnings = append(report.Warnings, "digest: "+err.Error())

		return
	}

	for _, target := range e.projectTargets(active) {
		e.syncDigestTarget(ctx, report, st, target, vaultItems, opts)
	}
}

func (e *Engine) syncDigestTarget(
	ctx context.Context, report *Report, st *state.State, target projectTarget, vaultItems kind.Items, opts SyncOptions,
) {
	path := target.path()

	if !target.active() {
		e.pruneDigest(st, report, target, opts)

		return
	}

	if !e.targetPublishable(report, target) {
		report.Digest = append(report.Digest, DigestResult{Agent: target.agent.ID, Path: path, Action: DigestSkipped})

		return
	}

	data, present, err := readOptional(path)
	if err != nil {
		report.Warnings = append(report.Warnings, "digest: "+err.Error())

		return
	}

	fence, found, err := digestFence(data, present)
	if err != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("digest: %s: %v", path, err))

		return
	}

	notes := digestNotesFor(vaultItems, target.notesSlug)
	block, receipt := digest.Render(target.notesDir, notes, digest.DefaultBudget)

	e.applyDigest(ctx, report, st, target, block, receipt, fence, found, present, opts)
}

func (e *Engine) applyDigest(
	ctx context.Context, report *Report, st *state.State, target projectTarget,
	block []byte, receipt digest.Receipt, fence []byte, found, present bool, opts SyncOptions,
) {
	if receipt.Notes == 0 {
		e.removeDigest(ctx, report, st, target, found, opts)

		return
	}

	if !present {
		e.writeDigest(ctx, report, st, target, block, DigestCreated, DigestWouldCreate, opts)

		return
	}

	stored, hasStored := st.Renders[target.path()]

	switch {
	case found && hasStored && stored.BlockHash != cas.HashOf(fence):
		if opts.Refresh {
			e.writeDigest(ctx, report, st, target, block, DigestRefreshed, DigestWouldRefresh, opts)
		} else {
			e.holdDigest(report, st, target, opts)
		}
	case found && !hasStored:
		e.adoptDigest(report, st, target, fence, opts)
	case cas.HashOf(block) != cas.HashOf(fence):
		e.writeDigest(ctx, report, st, target, block, DigestRefreshed, DigestWouldRefresh, opts)
	}
}

func (e *Engine) targetPublishable(report *Report, target projectTarget) bool {
	policy, err := e.projectPolicy()
	if err != nil {
		report.Warnings = append(report.Warnings, "digest: "+err.Error())

		return false
	}

	if policy.AllowsSecrets(target.rel) {
		return true
	}

	publishable, err := e.projectPublishable(target.rel)
	if err != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("digest: %s: %v", target.path(), err))

		return false
	}

	if !publishable {
		report.Warnings = append(report.Warnings, fmt.Sprintf(
			"digest: %s is not gitignored; the digest stays out of it (enable it in the policy with --allow-secrets to override)", target.path()))
	}

	return publishable
}

func digestFence(data []byte, present bool) ([]byte, bool, error) {
	if !present {
		return nil, false, nil
	}

	_, fence, found, err := digest.Strip(data)
	if err != nil {
		return nil, false, err
	}

	return fence, found, nil
}

func (e *Engine) removeDigest(
	ctx context.Context, report *Report, st *state.State, target projectTarget, found bool, opts SyncOptions,
) {
	path := target.path()

	if !found {
		e.pruneDigest(st, report, target, opts)

		return
	}

	action := DigestRemoved

	if opts.DryRun {
		action = DigestWouldRemove
	} else if err := e.writeFenced(ctx, target.surface, nil); err != nil {
		report.Warnings = append(report.Warnings, "digest: "+err.Error())

		return
	} else {
		e.resetDigest(st, path)
	}

	report.Digest = append(report.Digest, DigestResult{Agent: target.agent.ID, Path: path, Action: action})
}

func (e *Engine) writeDigest(
	ctx context.Context, report *Report, st *state.State, target projectTarget, block []byte, action, dryAction DigestAction, opts SyncOptions,
) {
	path := target.path()

	if opts.DryRun {
		report.Digest = append(report.Digest, DigestResult{Agent: target.agent.ID, Path: path, Action: dryAction})

		return
	}

	if err := e.writeFenced(ctx, target.surface, block); err != nil {
		report.Warnings = append(report.Warnings, "digest: "+err.Error())

		return
	}

	receipt, _ := digest.Verify(block)

	st.Renders[path] = state.Render{
		BlockHash:  cas.HashOf(block),
		InputsHash: cas.Hash(receipt.Inputs),
		Notes:      receipt.Notes,
		Omitted:    receipt.Omitted,
		At:         e.now().UTC(),
	}
	delete(st.Drift, path)

	report.Digest = append(report.Digest, DigestResult{Agent: target.agent.ID, Path: path, Action: action})
}

func (e *Engine) holdDigest(report *Report, st *state.State, target projectTarget, opts SyncOptions) {
	path := target.path()

	action := DigestHeld

	if opts.DryRun {
		action = DigestWouldHold
	}

	report.Digest = append(report.Digest, DigestResult{Agent: target.agent.ID, Path: path, Action: action})

	if opts.DryRun {
		return
	}

	drift := st.Drift[path]
	if drift.Count == 0 {
		drift.First = e.now().UTC()
	}

	drift.Count++
	drift.Last = e.now().UTC()

	st.Drift[path] = drift

	report.Warnings = append(report.Warnings, fmt.Sprintf(
		"digest: manual edits inside the generated block in %s; the digest is frozen: restore the bytes, delete the block, or run beadle sync --refresh-digest", path))
}

func (e *Engine) adoptDigest(report *Report, st *state.State, target projectTarget, fence []byte, opts SyncOptions) {
	path := target.path()

	receipt, ok := digest.Verify(fence)
	if !ok {
		report.Warnings = append(report.Warnings, fmt.Sprintf("digest: %s: cannot parse the beadle block", path))

		return
	}

	if !opts.DryRun {
		st.Renders[path] = state.Render{
			BlockHash:  cas.HashOf(fence),
			InputsHash: cas.Hash(receipt.Inputs),
			Notes:      receipt.Notes,
			Omitted:    receipt.Omitted,
			At:         e.now().UTC(),
		}
		delete(st.Drift, path)
	}

	report.Digest = append(report.Digest, DigestResult{Agent: target.agent.ID, Path: path, Action: DigestAdopted})
}

func (e *Engine) pruneDigest(st *state.State, report *Report, target projectTarget, opts SyncOptions) {
	path := target.path()

	_, rendered := st.Renders[path]

	_, drifted := st.Drift[path]

	if !rendered && !drifted {
		return
	}

	action := DigestPruned

	if opts.DryRun {
		action = DigestWouldPrune
	} else {
		e.resetDigest(st, path)
	}

	report.Digest = append(report.Digest, DigestResult{Agent: target.agent.ID, Path: path, Action: action})
}

func (e *Engine) resetDigest(st *state.State, path string) {
	delete(st.Renders, path)
	delete(st.Drift, path)
}

func (e *Engine) writeFenced(ctx context.Context, surface agent.Surface, block []byte) error {
	fenced, ok := surface.(fencedSurface)
	if !ok {
		return fmt.Errorf("%s cannot write a digest block", surface.Path())
	}

	return fenced.WriteFenced(ctx, block)
}

func digestNotesFor(items kind.Items, slug string) map[string][]byte {
	prefix := slug + "/"
	notes := map[string][]byte{}

	for key, data := range items {
		if strings.HasPrefix(key, prefix) {
			notes[key] = data
		}
	}

	return notes
}
