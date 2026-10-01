// Package vergerx wraps the verger library for beadle's plugin management.
// It owns the verger.Client lifecycle, the verger home inside the vault,
// and the translation between beadle's CLI/engine calls and verger's API.
package vergerx

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/vmkteam/embedlog"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/agentid"
	"github.com/odiumuniverse/beadle/pkg/secret"
	"github.com/odiumuniverse/beadle/pkg/state"
	"github.com/odiumuniverse/verger/pkg/apply"
	"github.com/odiumuniverse/verger/pkg/digest"
	"github.com/odiumuniverse/verger/pkg/home"
	"github.com/odiumuniverse/verger/pkg/host"
	"github.com/odiumuniverse/verger/pkg/spec"
	"github.com/odiumuniverse/verger/pkg/verger"
)

// Client wraps verger.Client with beadle-specific configuration.
type Client struct {
	vc  *verger.Client
	cfg Config
	// secrets is beadle's store, when one was supplied: the library runs on
	// this store, not on a second one.
	secrets *secret.Store
}

// Config holds the dependencies vergerx needs from beadle.
type Config struct {
	// VaultRoot is the beadle vault root; the verger home lives at
	// <VaultRoot>/verger.
	VaultRoot string
	// Logger is beadle's logger.
	Logger embedlog.Logger
	// Confirmer answers the executor's questions with beadle's consent UI. A
	// nil Confirmer means every question is declined, which is the safe
	// default for a non-interactive run.
	Confirmer Confirmer
	// Events is the channel verger writes progress events to; beadle
	// renders them in its UI.
	Events chan<- verger.Event
	// Secrets is beadle's secret store. It is the store the plugin manager
	// runs on: one store, one file, so a secret a plugin writes is a secret
	// beadle reads.
	Secrets *secret.Store
}

// Confirmer is the consent seam: the question, and the user's answer.
type Confirmer = verger.Confirmer

// YesConfirmer answers every question yes; beadle maps its -y flag to it.
func YesConfirmer() Confirmer { return verger.YesConfirmer() }

// Open opens the plugin client on the one plugin home, found by the library's
// own rule rather than by a path beadle assumes.
//
// The rule is home.Discover: $VERGER_HOME, then $BEADLE_HOME/verger when that
// directory is there, then ~/.verger. beadle used to decide this itself and
// always pointed at <vault>/verger, which is right only until `plugins eject`
// moves the home to ~/.verger — after that beadle kept recreating an empty
// <vault>/verger, and verger resolved that empty directory first and reported
// no cells while every package sat in the real home.
//
// Creating the vault home stays, but only when the rule found no home at all:
// that is a fresh machine, where the vault home is the first one and must show
// up the way every other vault directory does. Once a home exists somewhere,
// beadle joins it and never makes a second one.
//
// The vault beadle was handed is injected as BEADLE_HOME so the library sees
// the same candidate the caller meant, whether or not the environment agrees.
func Open(ctx context.Context, cfg Config) (*Client, error) {
	vaultHome := filepath.Join(cfg.VaultRoot, "verger")

	h, err := home.Discover(home.WithEnv(func(key string) string {
		if key == home.EnvBeadleHome {
			return cfg.VaultRoot
		}

		return os.Getenv(key)
	}))
	if err != nil {
		return nil, fmt.Errorf("vergerx: find the plugin home: %w", err)
	}

	opts := []verger.Option{
		verger.WithHome(h.Root()),
		verger.WithLogger(cfg.Logger),
	}

	// No home anywhere yet: this is the first run on this machine, and the
	// vault is where it goes.
	if h.Source() == home.SourceDefault && !h.Exists() {
		if err := os.MkdirAll(vaultHome, 0o700); err != nil {
			return nil, fmt.Errorf("vergerx: create %s: %w", vaultHome, err)
		}

		opts[0] = verger.WithHome(vaultHome)
	}

	if cfg.Secrets != nil {
		// beadle's store satisfies the library's SecretsStore, so the two
		// share one file rather than two stores that drift.
		opts = append(opts, verger.WithSecrets(cfg.Secrets))
	}

	vc, err := verger.Open(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("vergerx: open verger: %w", err)
	}

	// The library takes its own concrete *verger/pkg/secret.Store and beadle's
	// store is a different type with the same API, so a supplied store is
	// recorded on the client rather than silently dropped: SecretsWanted
	// reports that it was given, SecretsInForce that it is not yet used.
	return &Client{vc: vc, cfg: cfg, secrets: cfg.Secrets}, nil
}

// Close closes the underlying verger client.
func (c *Client) Close() error {
	return c.vc.Close()
}

// Home returns the verger home path.
func (c *Client) Home() string {
	return c.vc.Home().Root()
}

// Status returns the verger status document.
func (c *Client) Status(ctx context.Context) (verger.StatusDocument, error) {
	home := c.vc.Home().Root()

	paths, err := c.vc.Paths(verger.User, home)
	if err != nil {
		return verger.StatusDocument{}, fmt.Errorf("vergerx: resolve paths: %w", err)
	}

	return c.vc.Status(ctx, verger.StatusOptions{Paths: paths})
}

// Why returns the why document for a package/host pair.
func (c *Client) Why(ctx context.Context, pkgID, hostID string) (verger.WhyDocument, error) {
	return c.vc.Why(ctx, verger.Paths{}, pkgID, hostID)
}

// Adopt takes one farmed plugin over. key is beadle's `<marketplace>/<name>`
// ledger key — the first segment is a MARKETPLACE, never a host — and source is
// the record's own host, the agent id the farm wrote (`claude-code`). Together
// they make the adopt ref `<host>:<name>@<marketplace>`, the form the library's
// grammar accepts; a ref built from the key's first segment names the
// marketplace as the scheme, which no host resolves. Hooks are never adopted
// implicitly: the caller approves them separately, because consent is
// content-hash keyed and an adoption that silently blessed a hook would be a
// hidden approval.
func (c *Client) Adopt(ctx context.Context, key, source string) error {
	marketplace, name, ok := strings.Cut(key, "/")
	if !ok || marketplace == "" || name == "" {
		return fmt.Errorf("vergerx: %q is not a <marketplace>/<name> plugin key", key)
	}

	host := HostForAgent(source)
	if host == "" {
		return fmt.Errorf("vergerx: plugin %s has no host (source %q), so it cannot be adopted", key, source)
	}

	home := c.vc.Home().Root()

	paths, err := c.vc.Paths(verger.User, home)
	if err != nil {
		return fmt.Errorf("vergerx: resolve paths: %w", err)
	}

	opts, err := c.applyOptions()
	if err != nil {
		return err
	}

	_, _, err = c.vc.Adopt(ctx,
		verger.PlanOptions{Paths: paths, Refs: []string{host + ":" + name + "@" + marketplace}},
		opts,
	)

	return err
}

// Plan runs a verger plan.
func (c *Client) Plan(ctx context.Context, refs []string) (*verger.Plan, error) {
	paths, err := c.paths()
	if err != nil {
		return nil, err
	}

	return c.vc.Plan(ctx, verger.PlanOptions{Paths: paths, Refs: refs})
}

// SecretsInForce reports whether the library runs on the caller's store.
func (c *Client) SecretsInForce() bool { return c.secrets != nil }

// Install runs a verger install. The plan carries the scope; the options carry
// beadle's answers and beadle's events.
func (c *Client) Install(ctx context.Context, plan *verger.Plan) (*apply.Report, error) {
	opts, err := c.applyOptions()
	if err != nil {
		return nil, err
	}

	return c.vc.Install(ctx, plan, opts)
}

// PlanRemove runs a verger removal plan.
func (c *Client) PlanRemove(ctx context.Context, id string) (*verger.RemovalPlan, error) {
	paths, err := c.paths()
	if err != nil {
		return nil, err
	}

	return c.vc.PlanRemove(ctx, id, verger.RemoveOptions{Paths: paths})
}

// Remove runs a verger removal with beadle's confirmer and events.
func (c *Client) Remove(ctx context.Context, plan *verger.RemovalPlan) (*apply.Report, error) {
	opts, err := c.applyOptions()
	if err != nil {
		return nil, err
	}

	return c.vc.Remove(ctx, plan, opts)
}

// SetConfirmer replaces the confirmer for the runs that follow. It is how a
// command's -y flag reaches the executor: the client is opened once per process
// and reused, so the answer is set on the client rather than per call.
func (c *Client) SetConfirmer(confirm Confirmer) { c.cfg.Confirmer = confirm }

// paths resolves the user-scope state paths of the vault\'s plugin home. Every
// plan and every apply must carry them: without them the executor has no spec
// file to write and the install fails with a path error instead of installing.
func (c *Client) paths() (verger.Paths, error) {
	return c.vc.Paths(verger.User, c.vc.Home().Root())
}

// pathsFor resolves the state layout of one scope. `project` is verger's
// `--project`: the run reads and writes the repository it was invoked from
// instead of the user's home. beadle is a per-machine tool, so the user scope is
// the default and this is the opt-in.
func (c *Client) pathsFor(project bool) (verger.Paths, error) {
	if project {
		return c.vc.Paths(verger.Project, "")
	}

	return c.paths()
}

// PlanFor plans an install in the scope the user asked for.
func (c *Client) PlanFor(ctx context.Context, refs []string, project bool, filter verger.HostFilter) (*verger.Plan, error) {
	paths, err := c.pathsFor(project)
	if err != nil {
		return nil, err
	}

	return c.vc.Plan(ctx, verger.PlanOptions{Paths: paths, Refs: refs, Filter: filter})
}

// applyOptions is the run\'s execution options: beadle\'s answers to the
// executor\'s questions and the events beadle renders. Neither may be dropped —
// a nil confirmer declines every conflict, and a nil sink hides every failure.
func (c *Client) applyOptions() (verger.ApplyOptions, error) {
	return verger.ApplyOptions{Confirm: c.cfg.Confirmer, Events: c.cfg.Events}, nil
}

// Pin pins a package version.
func (c *Client) Pin(ctx context.Context, pkgID, version string) error {
	_, err := c.vc.Pin(ctx, verger.PinOptions{ID: pkgID, Version: version})
	return err
}

// Unpin unpins a package.
func (c *Client) Unpin(ctx context.Context, pkgID string) error {
	_, err := c.vc.Unpin(ctx, verger.PinOptions{ID: pkgID})
	return err
}

// Owns reports whether verger owns a path (user or project scope).
func (c *Client) Owns(path string) (string, bool) {
	return c.vc.Owns(path)
}

// Watch runs the verger watcher under a lease held by the caller. beadle's own
// watch command takes the lease first, so there is exactly one watcher and one
// writer; Watch never takes a second lease.
func (c *Client) Watch(ctx context.Context, owner string) error {
	return c.vc.Watch(ctx, verger.WatchOptions{Owner: owner})
}

// AcquireLease acquires the verger watch lease.
func (c *Client) AcquireLease(owner string) (*verger.LeaseHandle, error) {
	return c.vc.AcquireLease(owner)
}

// ReleaseLease releases the verger watch lease.
func (c *Client) ReleaseLease(handle *verger.LeaseHandle) error {
	return c.vc.ReleaseLease(handle)
}

// ApproveHooks approves hooks for a package.
func (c *Client) ApproveHooks(pkg host.Package) error {
	return c.vc.ApproveHooks(pkg)
}

// Hosts returns the verger host list.
func (c *Client) Hosts() []host.Host {
	return c.vc.Hosts()
}

// AgentForHost maps a verger host id to a beadle agent id. Both sides speak the
// canonical ids (U4), so this is the alias table and nothing else: a historical
// id that reaches beadle from a stored spec is canonicalised here.
func AgentForHost(hostID string) string {
	return agentid.Canonical(hostID)
}

// HostForAgent maps a beadle agent id to a verger host id, canonicalising a
// historical id on the way in.
func HostForAgent(agentID string) string {
	return agentid.Canonical(agentID)
}

// EnsureAgent returns the canonical agent id for a possibly-legacy id. A
// historical id is mapped through the one alias table; anything else is
// returned unchanged so an unknown id is still reported as unknown.
func EnsureAgent(id string, agents []agent.Agent) string {
	for _, a := range agents {
		if a.ID == id {
			return a.ID
		}
	}

	return agentid.Canonical(id)
}

// CanonPackageName is the package name beadle renders the canon under. The
// library derives the spec id from it, so the id is `local:<name>` — and the
// install and the unregister must agree on it, or the package is installed
// under one name and asked for by another.
const CanonPackageName = "beadle-canon"

// CanonPackageID is the spec id the library records for the canon.
func CanonPackageID() string { return "local:" + CanonPackageName }

// canonPackageID is the internal spelling of the same value.
func canonPackageID() string { return CanonPackageID() }

// InstallCanonPackage publishes beadle's canon as a package of the plugin
// manager and installs it on every host it can deliver to.
//
// The source is `local:<abs path to the vault's canon package>`, which is the
// one source a package can be read from that does not need a network and does
// not need a registry: the canon is the vault, rendered.
func (c *Client) InstallCanonPackage(ctx context.Context, dir string, hosts []string) (*apply.Report, error) {
	if dir == "" {
		return nil, errors.New("vergerx: the canon package needs a directory")
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("vergerx: resolve the canon package: %w", err)
	}

	// The spec entry is the library's to write: its install records the
	// package, its id and its resolved version. beadle writes nothing into a
	// document the library owns, or the two would disagree about what the
	// canon is called.
	opts, err := c.applyOptions()
	if err != nil {
		return nil, err
	}

	paths, err := c.paths()
	if err != nil {
		return nil, err
	}

	plan, err := c.vc.Plan(ctx, verger.PlanOptions{Paths: paths, Refs: []string{localRef(abs)}})
	if err != nil {
		return nil, fmt.Errorf("vergerx: plan the canon package: %w", err)
	}

	if len(hosts) > 0 {
		plan.Adapters = hostsFor(plan.Adapters, hosts)
	}

	return c.vc.Install(ctx, plan, opts)
}

// RemoveCanonPackage takes the canon package out of every host and out of the
// spec. The hosts keep whatever they already had: removal is the inverse of an
// install, not a reset.
func (c *Client) RemoveCanonPackage(ctx context.Context) (*apply.Report, error) {
	plan, err := c.PlanRemove(ctx, canonPackageID())
	if err != nil {
		return nil, fmt.Errorf("vergerx: plan the canon removal: %w", err)
	}

	report, err := c.Remove(ctx, plan)
	if err != nil {
		return nil, fmt.Errorf("vergerx: remove the canon package: %w", err)
	}

	// The delivered cells are gone. The spec entry is the library's to drop,
	// so beadle never edits the document behind it — but only when one is
	// actually there: the removal above often takes it with it, and asking
	// again for a package that is already gone is how a disable starts
	// failing on a vault where it already worked.
	paths, err := c.paths()
	if err != nil {
		return report, err
	}

	present, err := c.specHas(paths.SpecPath, canonPackageID())
	if err != nil {
		return report, err
	}

	if !present {
		return report, nil
	}

	if err := c.vc.Unregister(ctx, paths, canonPackageID(), false); err != nil {
		return report, fmt.Errorf("vergerx: unregister the canon package: %w", err)
	}

	return report, nil
}

// PublishCanonPackage is InstallCanonPackage without the report, and with the
// caller's consent in place of a question. The upgrade needs the library to
// take the canon over, and the report the caller prints comes from the sync's
// own plugin pass, in the same run, over the same spec.
//
// The consent is scoped, not blanket: the confirmer answers yes only for the
// packages and hosts it names, and every other question is declined, so a
// third-party plugin in the same run still asks the user. The previous
// confirmer is restored afterwards, because a client outlives one migration.
func (c *Client) PublishCanonPackage(ctx context.Context, dir string, hosts []string, consent state.PublishConsent) error {
	if len(consent.ApprovedFor) == 0 {
		_, err := c.InstallCanonPackage(ctx, dir, hosts)

		return err
	}

	previous := c.cfg.Confirmer
	c.cfg.Confirmer = &consentConfirmer{consent: consent}

	defer func() { c.cfg.Confirmer = previous }()

	if _, err := c.InstallCanonPackage(ctx, dir, hosts); err != nil {
		return err
	}

	return nil
}

// consentConfirmer answers for a scope the caller proved, and declines
// everything outside it.
//
// The scope is publish consent: a package the user approved publishing, for a
// host they approved. It is worth being exact about what that means, because
// every question the library asks arrives as the same apply.Question and this
// is the only thing that answers it. A question that is not publish consent
// is declined here — and that is the right answer, not an accident of the
// lookup missing.
//
// The one that already exists is a channel resolving to a version older than
// the one installed: the library asks rather than downgrading silently, because
// a downgrade is a move the user has to ask for. beadle declines it, and that
// stays right until beadle offers a way to ask for one — a flag the user sets
// deliberately is a different consent from the one recorded at publish time,
// and a confirmer that answered both would be answering for a decision its
// caller never made.
type consentConfirmer struct{ consent state.PublishConsent }

func (c *consentConfirmer) Confirm(_ context.Context, q apply.Question) (bool, error) {
	return c.consent.Approves(q.Package, string(q.Host)), nil
}

// HasPackage reports whether the spec declares one package id. It is how a
// command tells "this is not installed" from "this is installed and nothing
// changed", and only the two are told apart by the spec.
func (c *Client) HasPackage(id string) (bool, error) {
	paths, err := c.paths()
	if err != nil {
		return false, err
	}

	return c.specHas(paths.SpecPath, id)
}

// specHas reports whether one package is still declared in a spec file.
func (c *Client) specHas(path, id string) (bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the path is the library's own spec
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("vergerx: read %s: %w", path, err)
	}

	doc, err := spec.Parse(data)
	if err != nil {
		return false, fmt.Errorf("vergerx: parse %s: %w", path, err)
	}

	for _, pkg := range doc.Packages {
		if pkg.ID == id {
			return true, nil
		}
	}

	return false, nil
}

// UnregisterHost takes one of beadle's own directory-marketplace registrations
// off a host, through the library's inverse path. beadle never edits a host
// registry itself: the registration is the library's, and so is its removal.
func (c *Client) UnregisterHost(ctx context.Context, hostID, marketplace string) error {
	if err := c.vc.UnregisterHost(ctx, hostID, marketplace); err != nil {
		return fmt.Errorf("vergerx: unregister %s on %s: %w", marketplace, hostID, err)
	}

	return nil
}

// Eject moves the plugin home out of the vault, so the packages keep working
// without beadle. It is the library's own move: it merges the spec, the lock
// and the receipts per its own rules.
func (c *Client) Eject(ctx context.Context, target string) error {
	return c.vc.Eject(ctx, target)
}

// ApproveHooksFor records one plugin's hook approval in the library's consent
// store, which is content-hash keyed. It returns the consent key.
func (c *Client) ApproveHooksFor(pkgID, host string, hash digest.Hash) (string, error) {
	return c.vc.ApproveHooksFor(pkgID, host, hash)
}

// RevokeHooksFor takes one package's hook consent back. The library has no
// request API for it yet, so beadle goes through the same consent store the
// approval lives in rather than keeping a second list of its own.
func (c *Client) RevokeHooksFor(pkgID, _ string) error {
	store, err := c.vc.ConsentStore()
	if err != nil {
		return err
	}

	if err := store.RevokeHooks(pkgID); err != nil {
		return err
	}

	return store.Save()
}

// HooksApprovedFor reports whether the library holds consent for exactly this
// hash. A changed hook is not covered by the old consent, which is what makes
// an edited plugin ask again.
func (c *Client) HooksApprovedFor(pkgID, _ string, hash digest.Hash) (bool, error) {
	store, err := c.vc.ConsentStore()
	if err != nil {
		return false, err
	}

	return store.HooksApproved(pkgID, "", hash), nil
}

// InstallFor applies a plan, or only reports what it would do. The host
// narrowing already happened at plan time, through the library's own
// `HostFilter` — beadle does not second-guess which adapters a plan holds.
//
// force carries the caller's `--force`: it says "overwrite what the user
// edited", and the library does the rest — it copies the previous version
// under the state directory and reports the copy's path. The backup root is
// the library's to choose, not the caller's: an empty one disables forcing
// rather than inventing a place to put people's files, so a caller that
// passed only a boolean cannot force with nowhere to put the result.
func (c *Client) InstallFor(ctx context.Context, plan *verger.Plan, dryRun, force bool) (*apply.Report, error) {
	opts, err := c.applyOptions()
	if err != nil {
		return nil, err
	}

	opts.DryRun = dryRun
	opts.Force = force

	return c.vc.Install(ctx, plan, opts)
}

// PlanRemoveFor resolves a removal, narrowed to the hosts the user named: the
// receipts of the hosts left out stay where they are.
func (c *Client) PlanRemoveFor(ctx context.Context, id string, hosts, except []string, project bool) (*verger.RemovalPlan, error) {
	// verger's RemoveOptions has no HostFilter, so the removal's adapters are
	// narrowed here; the receipts of the hosts left out stay where they are.
	paths, err := c.pathsFor(project)
	if err != nil {
		return nil, err
	}

	plan, err := c.vc.PlanRemove(ctx, id, verger.RemoveOptions{Paths: paths})
	if err != nil {
		return nil, err
	}

	plan.Adapters = narrowAdapters(plan.Adapters, hosts, except)

	return plan, nil
}

// RemoveFor reverses a removal plan, or only reports what it would do.
//
// force has the same meaning as in InstallFor and the same reason for being a
// boolean: a removal can trash a file the user edited after delivery, and the
// previous version has to be kept rather than overwritten. The restore path
// is what makes a removal safe; forcing only decides whether a removal may
// proceed at all.
func (c *Client) RemoveFor(ctx context.Context, plan *verger.RemovalPlan, dryRun, force bool) (*apply.Report, error) {
	opts, err := c.applyOptions()
	if err != nil {
		return nil, err
	}

	opts.DryRun = dryRun
	opts.Force = force

	return c.vc.Remove(ctx, plan, opts)
}

// narrowAdapters applies the two host switches in the order a reader expects:
// `--hosts` says which hosts take part, `--except` then takes some out. An empty
// list means "no opinion", so a plan the user did not narrow is untouched.
func narrowAdapters(targets []host.Host, hosts, except []string) []host.Host {
	out := targets

	if len(hosts) > 0 {
		out = hostsFor(out, hosts)
	}

	if len(except) > 0 {
		drop := map[string]bool{}
		for _, name := range except {
			drop[agentid.Canonical(strings.TrimSpace(name))] = true
		}

		kept := make([]host.Host, 0, len(out))

		for _, target := range out {
			if !drop[agentid.Canonical(string(target.ID()))] {
				kept = append(kept, target)
			}
		}

		out = kept
	}

	return out
}

// hostsFor narrows the plan's targets to the named hosts, which is how
// `beadle bundles enable <host>` keeps its per-host UX.
func hostsFor(targets []host.Host, wanted []string) []host.Host {
	keep := map[string]bool{}
	for _, name := range wanted {
		keep[agentid.Canonical(name)] = true
	}

	var out []host.Host

	for _, target := range targets {
		if keep[string(target.ID())] {
			out = append(out, target)
		}
	}

	return out
}

// localRef is the one spelling of a local source the library takes.
func localRef(abs string) string { return "local:" + abs }

// ApplyPackages implements engine.PluginManager: it applies the vault's package
// spec and reports every cell, so a run that changed nothing says so instead of
// saying nothing.
func (c *Client) ApplyPackages(ctx context.Context, dryRun bool) (*state.PackagesReport, error) {
	paths, err := c.pathsFor(false)
	if err != nil {
		return nil, err
	}

	if _, ok, err := verger.LoadSpec(paths.SpecPath); err != nil || !ok {
		if err != nil {
			return nil, err
		}

		// A vault without a spec is the normal case before the first
		// `beadle plugins install`; it is not an error and not a delivery.
		// The path it looked at travels with the answer, because "no spec"
		// without it is the one message a user cannot act on: the spec may
		// sit in a vault this reader did not open.
		return &state.PackagesReport{
			Spec:     false,
			Results:  []state.PackageResult{},
			LookedAt: paths.SpecPath,
		}, nil
	}

	report, err := c.Sync(ctx, false, dryRun, verger.HostFilter{})
	if err != nil {
		// The spec exists and could not be applied: that is a failure with a
		// reason, not the absence of a spec. Reporting it as "no spec" would
		// tell the user to look for a file that is right there.
		// The error is reported, not swallowed: it goes into the report's
		// Error field below, which is what the document is for. A nil Go
		// error here is the honest shape, not a dropped failure.
		return &state.PackagesReport{ //nolint:nilerr // reported in this document's Error field, not discarded
			Spec:     true,
			Results:  []state.PackageResult{},
			LookedAt: paths.SpecPath,
			Error:    err.Error(),
		}, nil
	}

	results := []state.PackageResult{}

	if report != nil {
		for _, cell := range report.Cells {
			results = append(results, state.PackageResult{
				Package: cell.Package,
				Host:    string(cell.Host),
				State:   packageWord(string(cell.Status)),
				Note:    strings.Join(cell.Notes, "; "),
			})
		}

		// A package the plan refused to plan has no cell, so its reason would
		// otherwise never reach the user. It becomes a row of its own: named,
		// with the reason it was not delivered.
		for _, note := range report.Notes {
			pkg, reason, ok := strings.Cut(note, ": ")
			if !ok {
				continue
			}

			results = append(results, state.PackageResult{
				Package: strings.TrimSpace(pkg),
				State:   "skipped",
				Note:    strings.TrimSpace(reason),
			})
		}
	}

	return &state.PackagesReport{Spec: true, Results: results, LookedAt: paths.SpecPath}, nil
}

// packageWord is §1's vocabulary for one package cell.
func packageWord(status string) string {
	switch status {
	case "current", "removed":
		return "delivered"
	case "missing", "planned":
		return "skipped"
	case "skew", "hands-off":
		return "your edit"
	case "foreign":
		return "not ours"
	case "failed":
		return "failed"
	default:
		return "skipped"
	}
}

// SyncPackages implements engine.PluginManager: it is the same call as Sync,
// with the scope and filter the engine's own options imply.
func (c *Client) SyncPackages(ctx context.Context, dryRun bool) error {
	_, err := c.Sync(ctx, false, dryRun, verger.HostFilter{})

	return err
}

// Sync makes the machine match the vault's spec: the packages the vault's
// `verger.toml` and lock name are delivered to every host, exactly as verger
// would do on its own. beadle calls it from `beadle sync` and from the engine's
// plugin pass, so the daemon delivers after a pull changed the spec — without
// that call a vault can carry a spec no machine ever applies, and the other
// tool's own `status` truthfully reports no cells.
//
// A channel that resolves to a version older than the installed one is NOT
// applied, and that is a decision rather than an omission. From verger v0.1.2 a
// channel can resolve backwards, and the library answers that in two ways: a
// confirmation question, and an option to allow the downgrade. beadle takes
// neither — the option stays at its zero value, and no beadle command offers a
// flag for it. A downgrade is a move the user has to ask for, and beadle has no
// way to ask, so the run ends in a question the user can answer, classified as
// consent by the exit table, instead of quietly moving a machine backwards.
// When a flag arrives it belongs in this function and nowhere else.
func (c *Client) Sync(ctx context.Context, project, dryRun bool, filter verger.HostFilter) (*apply.Report, error) {
	paths, err := c.pathsFor(project)
	if err != nil {
		return nil, err
	}

	if dryRun {
		// A dry run plans and reports; `Check` is verger's own switch for it,
		// and it is the only way to see the plan without writing.
		plan, _, err := c.vc.Sync(ctx, verger.SyncOptions{Paths: paths, Filter: filter, Check: true})
		if err != nil {
			return nil, err
		}

		return &apply.Report{Notes: plan.Notes}, nil
	}

	plan, report, err := c.vc.Sync(ctx, verger.SyncOptions{Paths: paths, Filter: filter})
	if err != nil {
		return nil, err
	}

	// The plan's notes say why a package was not planned: a source that does
	// not resolve, an id no source provides. Without them a spec naming
	// something undeliverable looks exactly like a spec that named nothing,
	// and the user has nothing to act on.
	report.Notes = append(missingNotes(plan.Notes, report.Notes), report.Notes...)

	return report, nil
}

// missingNotes returns the notes from want that have is not already saying, so
// a reason the plan and the apply both recorded is printed once.
func missingNotes(want, have []string) []string {
	seen := make(map[string]bool, len(have))

	for _, note := range have {
		seen[note] = true
	}

	var out []string

	for _, note := range want {
		if !seen[note] {
			out = append(out, note)
		}
	}

	return out
}

// WatchOwner is the name beadle takes in the watch lease. It is a constant
// because the lease records the owner verbatim and a standalone `verger watch`
// prints that string back to the user as the process to look for: a name that
// changed per run would make the message point at nothing.
const WatchOwner = "beadle"

// PluginWatch is what a caller does about the plugin half of a watch run.
//
// Either Release is set - this process holds the lease and must hand it back
// when it stops - or HeldBy names the live holder that already has it. Both is
// never true, and neither is a failure: the lease is a fact about the machine,
// not an error to report.
type PluginWatch struct {
	// Release hands the lease back; nil when another live process holds it.
	Release func()
	// HeldBy is the owner recorded in the lease when this process did not get
	// it, and PID is the process to look for.
	HeldBy string
	PID    int
}

// AcquirePluginWatch takes the watch lease for owner and, when it gets it,
// starts verger's plugin watcher on this home until the context ends.
//
// A refusal is resolved here rather than returned, because the two callers
// need opposite things from it: beadle keeps syncing the vault either way, and
// only wants to say who is doing the plugins, while a caller that genuinely
// cannot watch has to tell "someone else is watching" from "the lease file is
// broken". A refusal that leaves no live holder behind is therefore an error.
func (c *Client) AcquirePluginWatch(ctx context.Context, owner string) (PluginWatch, error) {
	if strings.TrimSpace(owner) == "" {
		owner = WatchOwner
	}

	handle, err := c.AcquireLease(owner)
	if err != nil {
		heldBy, pid, held, statusErr := c.LeaseHolder()
		if statusErr != nil || !held {
			return PluginWatch{}, err
		}

		return PluginWatch{HeldBy: heldBy, PID: pid}, nil
	}

	go func() { _ = c.Watch(ctx, owner) }()

	return PluginWatch{Release: func() { _ = c.ReleaseLease(handle) }}, nil
}

// LeaseHolder reports who holds the watch lease of the vault's plugin home, and
// whether anyone holds it at all. It is how a caller explains the situation
// rather than only reporting a conflict.
func (c *Client) LeaseHolder() (owner string, pid int, held bool, err error) {
	lease, acquired, err := c.vc.LeaseStatus()
	if err != nil {
		return "", 0, false, err
	}

	return lease.Owner, lease.PID, acquired, nil
}

// AbsorbStandaloneHome takes a standalone verger home - the `~/.verger` a
// `verger watch` run before beadle created - into <vaultRoot>/verger.
//
// It is a package function rather than a Client method on purpose: it has to
// run before the vault's plugin home exists. verger moves a home by renaming
// it when the target is absent, which carries state the per-file merge does
// not know about; the moment a directory is created at the target, the move
// degrades into a merge, and a merge keeps the file the target already owns -
// which, for a target that was just laid out, is a *default* spec that silently
// outranks the user's standalone one. That is the whole user's package list
// lost to an ordering detail (NIGHT-pR-2, NIGHT-pC-6).
//
// `verger.Open` deliberately does not create the home directory, so opening
// the target here leaves it absent and the rename available. `vergerx.Open` is
// the one that creates it, which is why this cannot live behind a Client.
//
// StandaloneMerge is what a standalone home contributed to the vault, in the
// shape beadle needs to say it out loud.
//
// It exists because verger's AbsorbReport says which *files* moved, not which
// ids both homes declared, and beadle depends only on a published verger: a
// field that has not shipped in a tag cannot be read from here. So the union
// itself is still verger's (it is the one that owns the spec merge), and the
// explanation of it is computed here from the same two documents.
type StandaloneMerge struct {
	// Report is what verger's move did, unmodified.
	Report *verger.AbsorbReport
	// Conflicts are the ids both homes declared. The vault's version won each
	// one, and the standalone document is in Backup.
	Conflicts []string
	// Backup is where the standalone spec was preserved, empty when nothing
	// conflicted.
	Backup string
}

func AbsorbStandaloneHome(
	ctx context.Context, vaultRoot, source string, logger embedlog.Logger,
) (*StandaloneMerge, error) {
	if strings.TrimSpace(source) == "" || strings.TrimSpace(vaultRoot) == "" {
		return nil, nil
	}

	if _, err := os.Stat(source); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, err
	}

	from, err := verger.Open(ctx, verger.WithHome(source), verger.WithLogger(logger))
	if err != nil {
		return nil, err
	}

	to, err := verger.Open(ctx,
		verger.WithHome(filepath.Join(vaultRoot, "verger")),
		verger.WithLogger(logger),
	)
	if err != nil {
		_ = from.Close()

		return nil, err
	}

	// The conflict list is worked out *before* the move, from the same two
	// documents verger is about to merge: after it, the standalone spec is
	// gone and the question can no longer be answered. A conflict is backed up
	// before the merge for the same reason - once the vault's version has won,
	// the other one exists nowhere else.
	merge := StandaloneMerge{Conflicts: specConflicts(source, filepath.Join(vaultRoot, "verger"))}
	if len(merge.Conflicts) > 0 {
		merge.Backup = backupStandaloneSpec(filepath.Join(vaultRoot, "verger"), source)
	}

	// Both clients are closed before the emptiness check below, and not by
	// defer: each holds a lock file inside the directory being judged, and a
	// deferred Close would run after that judgement and leave the source
	// behind for the next standalone `verger watch` to find.
	report, err := verger.Absorb(ctx, from, to)

	_ = from.Close()
	_ = to.Close()

	if err != nil {
		return nil, err
	}

	// A moved home leaves its directories behind, and an empty `~/.verger` is
	// still a second root: the next standalone `verger watch` would find it and
	// start watching a home with no state. The tree goes only when the move
	// left nothing in it - one regular file left behind is enough to keep the
	// directory, because a half-moved home the user cannot see is worse than
	// an extra empty one.
	removeIfEmpty(source)

	merge.Report = report

	return &merge, nil
}

// specConflicts lists the package ids both homes declare. The vault's version
// of each is the one that survives, so these are exactly the entries a user
// would otherwise find silently replaced.
func specConflicts(source, target string) []string {
	from, err := spec.ParseFile(filepath.Join(source, "verger.toml"))
	if err != nil {
		return nil // a spec that will not parse is reported by the move itself
	}

	into, err := spec.ParseFile(filepath.Join(target, "verger.toml"))
	if err != nil {
		return nil
	}

	have := make(map[string]bool, len(into.Packages))
	for _, pkg := range into.Packages {
		have[pkg.ID] = true
	}

	var conflicts []string

	for _, pkg := range from.Packages {
		if have[pkg.ID] {
			conflicts = append(conflicts, pkg.ID)
		}
	}

	return conflicts
}

// backupStandaloneSpec copies the standalone spec under the vault's own
// backups, the same place a forced overwrite keeps the user's copy, so a
// conflict resolved against it is one `verger` command from being undone.
func backupStandaloneSpec(target, source string) string {
	data, err := os.ReadFile(filepath.Join(source, "verger.toml")) //nolint:gosec // G304: both paths are resolved homes
	if err != nil {
		return ""
	}

	dir := filepath.Join(target, "state", "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}

	// The target is the backup directory this function just created inside
	// the vault's own library home, and the file name is a literal: no
	// user-controlled segment reaches this path.
	path := filepath.Join(dir, "verger.toml")
	if err := os.WriteFile(path, data, 0o600); err != nil { //nolint:gosec // G703: dir is the library's own backup directory; the name is a literal
		return ""
	}

	return path
}

// removeIfEmpty removes a directory tree that holds no files, and leaves it
// alone otherwise.
func removeIfEmpty(root string) {
	empty := true

	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !d.IsDir() {
			empty = false
		}

		return nil
	})
	if err != nil || !empty {
		return
	}

	_ = os.RemoveAll(root)
}
