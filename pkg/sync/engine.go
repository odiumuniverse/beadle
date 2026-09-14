package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/samber/lo"
	"github.com/vmkteam/embedlog"

	"github.com/odiumuniverse/agents-sync/pkg/adapter"
	"github.com/odiumuniverse/agents-sync/pkg/cas"
	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
	"github.com/odiumuniverse/agents-sync/pkg/history"
	"github.com/odiumuniverse/agents-sync/pkg/mcp"
	"github.com/odiumuniverse/agents-sync/pkg/permission"
	"github.com/odiumuniverse/agents-sync/pkg/registry"
	"github.com/odiumuniverse/agents-sync/pkg/secret"
	"github.com/odiumuniverse/agents-sync/pkg/skill"
	"github.com/odiumuniverse/agents-sync/pkg/vault"
)

type Engine struct {
	vault           *vault.Vault
	store           *cas.Store
	state           *registry.State
	config          *config.Config
	adapters        []adapter.Adapter
	log             embedlog.Logger
	secrets         *secret.Store
	sharedSkillsDir string
	prune           bool
}

type Option func(*Engine)

func WithSharedSkillsDir(dir string) Option {
	return func(e *Engine) {
		e.sharedSkillsDir = dir
	}
}

func WithPrune() Option {
	return func(e *Engine) {
		e.prune = true
	}
}

type canon struct {
	rules       []byte
	servers     mcp.Servers
	skills      map[string]skill.Tree
	permissions permission.Rules
	extracted   bool
}

func New(v *vault.Vault, cfg *config.Config, adapters []adapter.Adapter, log embedlog.Logger, opts ...Option) (*Engine, error) {
	state, err := registry.Load(v.RegistryPath())
	if err != nil {
		return nil, err
	}

	secrets, err := secret.Load(v.SecretsPath())
	if err != nil {
		return nil, err
	}

	engine := &Engine{
		vault:    v,
		store:    cas.NewStore(v.ObjectsDir()),
		state:    state,
		config:   cfg,
		adapters: adapters,
		log:      log,
		secrets:  secrets,
	}

	for _, opt := range opts {
		opt(engine)
	}

	return engine, nil
}

func (e *Engine) Run(ctx context.Context, mode Mode) (Report, error) {
	report := Report{Mode: mode, Actions: map[string]AgentActions{}}

	_, snapshots, err := e.exportAll(ctx)
	if err != nil {
		return report, err
	}

	state, err := e.loadCanon()
	if err != nil {
		return report, err
	}

	var base *canon

	merged := false

	if mode != ModePush {
		base, err = e.loadBase()
		if err != nil {
			return report, err
		}

		merged, err = e.merge(base, state, snapshots, &report)
		if err != nil {
			return report, err
		}
	}

	if mode != ModePull {
		if err := e.push(ctx, state, snapshots, &report); err != nil {
			return report, err
		}

		if err := e.syncSharedSkills(state, &report); err != nil {
			return report, err
		}
	}

	if err := e.secrets.Save(); err != nil {
		return report, err
	}

	if err := e.updateRegistry(base, state, snapshots, &report, merged); err != nil {
		return report, err
	}

	if err := e.state.Save(e.vault.RegistryPath()); err != nil {
		return report, err
	}

	e.commitHistory(ctx)

	return report, nil
}

func (e *Engine) exportAll(ctx context.Context) ([]adapter.Adapter, map[string]adapter.Snapshot, error) {
	active, err := e.activeAdapters()
	if err != nil {
		return nil, nil, err
	}

	snapshots := make(map[string]adapter.Snapshot, len(active))

	for _, a := range active {
		snapshot, err := a.Export(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("export %s: %w", a.ID(), err)
		}

		servers, _, err := secret.Extract(snapshot.MCP, e.secrets)
		if err != nil {
			return nil, nil, fmt.Errorf("extract %s secrets: %w", a.ID(), err)
		}

		snapshot.MCP = servers
		snapshots[a.ID()] = snapshot
	}

	return active, snapshots, nil
}

func (e *Engine) commitHistory(ctx context.Context) {
	if e.config.HistoryMode() != config.HistoryGit {
		return
	}

	message := "agentsync: sync " + time.Now().Format("2006-01-02 15:04:05")

	if err := history.Commit(ctx, e.vault.Root(), message, e.log); err != nil {
		e.log.Error(ctx, "vault history failed", "err", err)
	}
}

func (e *Engine) loadCanon() (*canon, error) {
	rules, err := readOptionalFile(filepath.Join(e.vault.Root(), "rules", "base.md"))
	if err != nil {
		return nil, err
	}

	data, err := readOptionalFile(filepath.Join(e.vault.Root(), "mcp", "servers.json"))
	if err != nil {
		return nil, err
	}

	servers, err := mcp.ParseCanonical(data)
	if err != nil {
		return nil, err
	}

	servers, extracted, err := secret.Extract(servers, e.secrets)
	if err != nil {
		return nil, err
	}

	skills, err := skill.ReadDir(filepath.Join(e.vault.Root(), "skills"))
	if err != nil {
		return nil, err
	}

	permissions, err := e.loadPermissions()
	if err != nil {
		return nil, err
	}

	return &canon{
		rules:       rules,
		servers:     servers,
		skills:      skills,
		permissions: permissions,
		extracted:   extracted,
	}, nil
}

func (e *Engine) loadBase() (*canon, error) {
	base := &canon{
		servers:     mcp.Servers{},
		skills:      map[string]skill.Tree{},
		permissions: permission.Rules{},
	}

	rulesData, err := e.baseBlob(ResourceRules)
	if err != nil {
		return nil, err
	}

	base.rules = rulesData

	serversData, err := e.baseBlob(ResourceMCP)
	if err != nil {
		return nil, err
	}

	base.servers, err = mcp.ParseCanonical(serversData)
	if err != nil {
		return nil, err
	}

	base.servers, _, err = secret.Extract(base.servers, e.secrets)
	if err != nil {
		return nil, err
	}

	skillsData, err := e.baseBlob(ResourceSkills)
	if err != nil {
		return nil, err
	}

	if skillsData != nil {
		skills, err := loadSkillsBase(e.store, skillsData)
		if err != nil {
			return nil, err
		}

		base.skills = skills
	}

	permissionsData, err := e.baseBlob(ResourcePermissions)
	if err != nil {
		return nil, err
	}

	base.permissions, err = permission.Parse(permissionsData)
	if err != nil {
		return nil, err
	}

	return base, nil
}

func (e *Engine) baseBlob(resource string) ([]byte, error) {
	entry := e.state.Resources[resource]
	if entry == nil || entry.Base == "" {
		return nil, nil
	}

	data, err := e.store.Get(entry.Base)
	if err != nil {
		return nil, fmt.Errorf("read %s base: %w", resource, err)
	}

	return data, nil
}

func loadSkillsBase(store *cas.Store, data []byte) (map[string]skill.Tree, error) {
	manifests := map[string]skill.Manifest{}

	if err := json.Unmarshal(data, &manifests); err != nil {
		return nil, fmt.Errorf("parse skills base: %w", err)
	}

	skills := make(map[string]skill.Tree, len(manifests))

	for name, manifest := range manifests {
		tree := make(skill.Tree, len(manifest))

		for path, hash := range manifest {
			content, err := store.Get(hash)
			if err != nil {
				return nil, fmt.Errorf("read base file %s/%s: %w", name, path, err)
			}

			tree[path] = content
		}

		skills[name] = tree
	}

	return skills, nil
}

func (e *Engine) activeAdapters() ([]adapter.Adapter, error) {
	active, err := lo.FilterErr(e.adapters, func(a adapter.Adapter, _ int) (bool, error) {
		if !e.config.Agents[a.ID()].Enabled {
			return false, nil
		}

		detected, err := a.Detect()
		if err != nil {
			return false, err
		}

		if !detected {
			e.log.Print(context.Background(), "agent enabled but not detected, skipping", "agent", a.ID())

			return false, nil
		}

		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("detect agents: %w", err)
	}

	return active, nil
}

func (e *Engine) orderedAgents(snapshots map[string]adapter.Snapshot) []string {
	ids := make([]string, 0, len(snapshots))

	for _, a := range e.adapters {
		if _, ok := snapshots[a.ID()]; ok {
			ids = append(ids, a.ID())
		}
	}

	return ids
}

func (e *Engine) adapterByID(id string) adapter.Adapter {
	for _, a := range e.adapters {
		if a.ID() == id {
			return a
		}
	}

	return nil
}

func (e *Engine) skillMode(agentID string) string {
	return e.config.Agents[agentID].SkillsMode()
}

type canonChanges struct {
	rules       bool
	servers     bool
	skills      map[string]struct{}
	permissions bool
}

func (e *Engine) persistCanon(state *canon, changed canonChanges) error {
	if changed.rules {
		if err := fsutil.WriteFileAtomic(filepath.Join(e.vault.Root(), "rules", "base.md"), state.rules, 0o600); err != nil {
			return fmt.Errorf("write vault rules: %w", err)
		}
	}

	if changed.servers || state.extracted {
		data, err := state.servers.MarshalCanonical()
		if err != nil {
			return err
		}

		if err := fsutil.WriteFileAtomic(filepath.Join(e.vault.Root(), "mcp", "servers.json"), data, 0o600); err != nil {
			return fmt.Errorf("write vault servers: %w", err)
		}

		state.extracted = false
	}

	for name := range changed.skills {
		if err := skill.SyncTree(filepath.Join(e.vault.Root(), "skills"), name, state.skills[name]); err != nil {
			return fmt.Errorf("write vault skill %s: %w", name, err)
		}
	}

	if changed.permissions {
		data, err := state.permissions.Marshal()
		if err != nil {
			return err
		}

		path := filepath.Join(e.vault.Root(), "permissions", "rules.json")

		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("create permissions directory: %w", err)
		}

		if err := fsutil.WriteFileAtomic(path, data, 0o600); err != nil {
			return fmt.Errorf("write vault permissions: %w", err)
		}
	}

	return nil
}

func readOptionalFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: vault paths are resolved by the tool
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	return data, nil
}

func hasMarkers(data []byte) bool {
	return bytes.Contains(data, []byte("<<<<<<<"))
}

func serverNames(servers mcp.Servers) []string {
	names := lo.Keys(servers)
	slices.Sort(names)

	return names
}
