package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/vmkteam/embedlog"

	"github.com/odiumuniverse/agents-sync/pkg/agent"
	"github.com/odiumuniverse/agents-sync/pkg/cas"
	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/history"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
	"github.com/odiumuniverse/agents-sync/pkg/lock"
	"github.com/odiumuniverse/agents-sync/pkg/secret"
	"github.com/odiumuniverse/agents-sync/pkg/state"
	"github.com/odiumuniverse/agents-sync/pkg/vault"
)

const lockWait = 30 * time.Second

type Engine struct {
	vault   *vault.Vault
	store   *cas.Store
	config  *config.Config
	agents  []*agent.Agent
	secrets *secret.Store
	log     embedlog.Logger
	now     func() time.Time
	home    string
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

func New(v *vault.Vault, cfg *config.Config, agents []*agent.Agent, opts ...Option) (*Engine, error) {
	secrets, err := secret.Load(v.SecretsPath())
	if err != nil {
		return nil, err
	}

	e := &Engine{
		vault:   v,
		store:   cas.NewStore(v.ObjectsDir()),
		config:  cfg,
		agents:  agents,
		secrets: secrets,
		now:     time.Now,
	}

	for _, opt := range opts {
		opt(e)
	}

	return e, nil
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
	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		return nil, err
	}

	active, err := e.activeAgents(ctx)
	if err != nil {
		return nil, err
	}

	report := &Report{DryRun: opts.DryRun}

	if !opts.DryRun {
		e.syncPluginSurfaces(ctx, report, active, opts)
	}

	for _, spec := range kind.All() {
		if !e.config.KindEnabled(spec.ID) || !selected(opts.Kinds, spec.ID) {
			continue
		}

		report.Kinds = append(report.Kinds, e.syncKind(ctx, spec, active, st, opts))
	}

	report.Conflicts = st.OpenConflicts()

	if opts.DryRun {
		return report, nil
	}

	if err := e.secrets.Save(); err != nil {
		return report, err
	}

	if err := st.Save(e.vault.StatePath()); err != nil {
		return report, err
	}

	if err := e.writeConflictFiles(st); err != nil {
		report.Warnings = append(report.Warnings, "conflict files: "+err.Error())
	}

	if report.VaultChanged() {
		e.commitHistory(ctx)
	}

	return report, nil
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

	message := "agentsync: sync " + e.now().Format("2006-01-02 15:04:05")

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
		e.vault.PermissionsPath(),
	}

	active, err := e.activeAgents(ctx)
	if err != nil {
		return nil, err
	}

	for _, a := range active {
		for _, surface := range a.Surfaces {
			if !e.config.KindEnabled(surface.Kind()) {
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
