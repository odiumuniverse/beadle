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

	report := &Report{DryRun: opts.DryRun}
	e.warnKeyring(report)
	e.noteConfigMigration(report, opts)
	e.beginRulings(opts)

	if !opts.DryRun {
		report.Warnings = append(report.Warnings, e.recordHostCLIs()...)
		e.syncPluginSurfaces(ctx, report, active, opts)
	}

	for _, spec := range kind.All() {
		if !e.config.KindEnabled(spec.ID) || !selected(opts.Kinds, spec.ID) {
			continue
		}

		report.Kinds = append(report.Kinds, e.syncKind(ctx, spec, active, st, opts))
	}

	e.liftRulings(report)

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

	e.noteSkillReferencesIfFull(st, active, report, opts)

	e.presentHooks(st, report, active, opts)

	e.syncDigest(ctx, report, active, st, opts)

	report.Conflicts = st.OpenConflicts()

	if opts.DryRun {
		return report, nil
	}

	return e.commitSync(ctx, st, report, opts)
}

func (e *Engine) commitSync(ctx context.Context, st *state.State, report *Report, opts SyncOptions) (*Report, error) {
	if err := e.secrets.Save(); err != nil {
		return report, err
	}

	e.pruneSkillTrees(st)

	if err := st.Save(e.vault.StatePath()); err != nil {
		return report, err
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

	for name := range canon {
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
