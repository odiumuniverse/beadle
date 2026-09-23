package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/permission"
	"github.com/odiumuniverse/beadle/pkg/secret"
)

const FileName = "config.json"

// CurrentVersion is the schema version this build reads and writes. Version 3
// migrated the default-on experience: permissions are synchronized and the
// shared skills surface is enabled unless the user opted out explicitly.
const CurrentVersion = 3

// SharedAgentID is the agent that owns the shared skills surface
// (~/.agents/skills); the migration enables it because it is the delivery
// channel for hosts that read the shared directory natively.
const SharedAgentID = "shared"

type Mode string

const (
	ModeSync Mode = "sync"
	ModePull Mode = "pull"
	ModePush Mode = "push"
	ModeOff  Mode = "off"
)

const (
	SkillsSync     = "sync"
	SkillsPullOnly = "pull-only"
	SkillsOff      = "off"
)

func ParseMode(name string) (Mode, error) {
	switch mode := Mode(strings.ToLower(strings.TrimSpace(name))); mode {
	case ModeSync, ModePull, ModePush, ModeOff:
		return mode, nil
	default:
		return "", fmt.Errorf("unknown mode %q (expected sync, pull, push or off)", name)
	}
}

func (m Mode) Pulls() bool {
	return m == ModeSync || m == ModePull
}

func (m Mode) Pushes() bool {
	return m == ModeSync || m == ModePush
}

type Agent struct {
	Enabled    bool              `json:"enabled"`
	Modes      map[kind.ID]Mode  `json:"modes,omitempty"`
	Skills     string            `json:"skills,omitempty"`
	PluginPins map[string]string `json:"plugin_pins,omitempty"`
}

func (a Agent) Mode(k kind.ID, fallback Mode) Mode {
	if mode, ok := a.Modes[k]; ok {
		return mode
	}

	if k == kind.Skills {
		switch a.Skills {
		case SkillsPullOnly:
			return ModePull
		case SkillsOff:
			return ModeOff
		case SkillsSync:
			return ModeSync
		}
	}

	return fallback
}

const (
	HistoryOff = "off"
	HistoryGit = "git"
)

type Config struct {
	Version       int              `json:"version"`
	Agents        map[string]Agent `json:"agents,omitempty"`
	Kinds         map[kind.ID]Mode `json:"kinds,omitempty"`
	Permissions   string           `json:"permissions"`
	History       string           `json:"history"`
	Secrets       string           `json:"secrets"`
	ApprovedHooks []string         `json:"approved_hooks,omitempty"`
	// migration carries the one-time flips Load applied, so the caller can
	// report them; it is never serialized.
	migration []string
	// migrated records that Load applied the flips in memory only. Save
	// clears it, so a mutating command can persist the migrated config even
	// after the notes have already been rendered.
	migrated bool
}

// MigrationNotes lists the one-time default flips Load applied. It is empty
// for a config that was already current.
func (c *Config) MigrationNotes() []string {
	return slices.Clone(c.migration)
}

// Migrated reports that the config was migrated in memory and not yet
// persisted; the next Save writes the migrated defaults to the vault.
func (c *Config) Migrated() bool {
	return c.migrated
}

// TakeMigrationNotes returns the pending migration notes and clears them, so
// every flip is reported exactly once.
func (c *Config) TakeMigrationNotes() []string {
	notes := c.migration
	c.migration = nil

	return notes
}

func (c *Config) KindEnabled(k kind.ID) bool {
	if mode, ok := c.Kinds[k]; ok {
		return mode != ModeOff
	}

	if k == kind.Permissions {
		return c.Permissions == permission.ModeSync
	}

	return true
}

func (c *Config) SetKind(k kind.ID, mode Mode) {
	if c.Kinds == nil {
		c.Kinds = map[kind.ID]Mode{}
	}

	c.Kinds[k] = mode
}

func (c *Config) ModeFor(agentID string, k kind.ID, fallback Mode) Mode {
	return c.Agents[agentID].Mode(k, fallback)
}

func (c *Config) SetMode(agentID string, k kind.ID, mode Mode) {
	if c.Agents == nil {
		c.Agents = map[string]Agent{}
	}

	agent := c.Agents[agentID]
	if agent.Modes == nil {
		agent.Modes = map[kind.ID]Mode{}
	}

	agent.Modes[k] = mode
	c.Agents[agentID] = agent
}

func (c *Config) SecretsMode() string {
	if c.Secrets == secret.ModeEnv {
		return secret.ModeEnv
	}

	return secret.ModeLiteral
}

func (c *Config) HistoryMode() string {
	if c.History == HistoryGit {
		return HistoryGit
	}

	return HistoryOff
}

func Default() *Config {
	return &Config{
		Version:     CurrentVersion,
		Agents:      map[string]Agent{},
		Permissions: permission.ModeSync,
		History:     HistoryGit,
		Secrets:     secret.ModeLiteral,
	}
}

// Normalize fills empty scalars with their defaults so legacy or partial
// files behave like a fresh config.
func (c *Config) Normalize() {
	defaults := Default()

	if c.Version == 0 {
		c.Version = defaults.Version
	}

	if c.Permissions == "" {
		c.Permissions = defaults.Permissions
	}

	if c.History == "" {
		c.History = defaults.History
	}

	if c.Secrets == "" {
		c.Secrets = defaults.Secrets
	}

	if c.Agents == nil {
		c.Agents = map[string]Agent{}
	}
}

// migrate applies the one-time default flips introduced by config v3 and
// reports every flip. It returns changed=true whenever the stored schema
// version is older, even when no flip happened. The caller decides when the
// migrated config is persisted: a read-only command must not write the vault.
func (c *Config) migrate(storedVersion int) ([]string, bool) {
	if storedVersion >= CurrentVersion {
		return nil, false
	}

	var notes []string

	if _, ok := c.Kinds[kind.Permissions]; !ok {
		c.SetKind(kind.Permissions, ModeSync)

		c.Permissions = permission.ModeSync

		notes = append(notes, "permissions are synchronized by default now; run `beadle kinds disable permissions` to opt out")
	}

	if _, ok := c.Agents[SharedAgentID]; !ok {
		c.Enable(SharedAgentID)

		notes = append(notes, "the shared skills surface (~/.agents/skills) is enabled by default now; run `beadle agents disable shared` to opt out")
	}

	c.Version = CurrentVersion

	return notes, true
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: reading the vault config path is the intended function
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}

	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := Default()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// The stored version is read from the raw document: cfg starts from
	// Default(), so a legacy file without a version field would otherwise
	// silently inherit the current one.
	storedVersion, err := storedConfigVersion(data)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	cfg.Normalize()

	if storedVersion > CurrentVersion {
		return nil, fmt.Errorf("config %s has version %d, but this beadle supports up to version %d", path, storedVersion, CurrentVersion)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}

	// The migration stays in memory: read-only commands (status, doctor,
	// dry runs) must not write the vault. The first command that saves the
	// config persists it and reports every flip.
	notes, changed := cfg.migrate(storedVersion)
	cfg.migration = notes
	cfg.migrated = changed

	return cfg, nil
}

// storedConfigVersion reads the version recorded in the file. A missing field
// counts as version 0, so the legacy config is migrated instead of silently
// inheriting the current default.
func storedConfigVersion(data []byte) (int, error) {
	var stored struct {
		Version *int `json:"version"`
	}

	if err := json.Unmarshal(data, &stored); err != nil {
		return 0, err
	}

	if stored.Version == nil {
		return 0, nil
	}

	return *stored.Version, nil
}

func (c *Config) validate() error {
	for id, agent := range c.Agents {
		for k, mode := range agent.Modes {
			if _, err := kind.Parse(string(k)); err != nil {
				return fmt.Errorf("agent %s: %w", id, err)
			}

			if _, err := ParseMode(string(mode)); err != nil {
				return fmt.Errorf("agent %s, %s: %w", id, k, err)
			}
		}

		for key, version := range agent.PluginPins {
			if err := ValidatePluginPin(key, version); err != nil {
				return fmt.Errorf("agent %s: %w", id, err)
			}
		}
	}

	for k, mode := range c.Kinds {
		if _, err := kind.Parse(string(k)); err != nil {
			return err
		}

		if _, err := ParseMode(string(mode)); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
	}

	return nil
}

func (c *Config) Save(path string) error {
	c.Version = CurrentVersion
	c.migrated = false

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	if err := fsutil.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}

func (c *Config) Enable(agentID string) {
	if c.Agents == nil {
		c.Agents = map[string]Agent{}
	}

	agent := c.Agents[agentID]
	agent.Enabled = true
	c.Agents[agentID] = agent
}

func (c *Config) Disable(agentID string) {
	agent := c.Agents[agentID]
	agent.Enabled = false

	if c.Agents == nil {
		c.Agents = map[string]Agent{}
	}

	c.Agents[agentID] = agent
}

var pinVersionPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidatePluginPin checks a plugin pin key ("<marketplace>/<name>") and a
// version that must be a single path element.
func ValidatePluginPin(key, version string) error {
	marketplace, name, ok := strings.Cut(key, "/")
	if !ok || strings.Contains(name, "/") || !filepath.IsLocal(marketplace) || !filepath.IsLocal(name) {
		return fmt.Errorf("invalid plugin key %q (expected <marketplace>/<name>)", key)
	}

	if !filepath.IsLocal(version) || !pinVersionPattern.MatchString(version) {
		return fmt.Errorf("invalid version %q (expected one element of [A-Za-z0-9._-])", version)
	}

	return nil
}

func (c *Config) PluginPin(agentID, key string) (string, bool) {
	version, ok := c.Agents[agentID].PluginPins[key]

	return version, ok
}

func (c *Config) SetPluginPin(agentID, key, version string) error {
	if err := ValidatePluginPin(key, version); err != nil {
		return err
	}

	if c.Agents == nil {
		c.Agents = map[string]Agent{}
	}

	agent := c.Agents[agentID]
	if agent.PluginPins == nil {
		agent.PluginPins = map[string]string{}
	}

	agent.PluginPins[key] = version
	c.Agents[agentID] = agent

	return nil
}

func (c *Config) UnsetPluginPin(agentID, key string) {
	agent, ok := c.Agents[agentID]
	if !ok {
		return
	}

	delete(agent.PluginPins, key)

	if len(agent.PluginPins) == 0 {
		agent.PluginPins = nil
	}

	c.Agents[agentID] = agent
}

func (c *Config) HookApproved(name string) bool {
	return slices.Contains(c.ApprovedHooks, name)
}

func (c *Config) ApproveHook(name string) {
	if c.HookApproved(name) {
		return
	}

	c.ApprovedHooks = append(c.ApprovedHooks, name)
	slices.Sort(c.ApprovedHooks)
}

func (c *Config) RevokeHook(name string) {
	c.ApprovedHooks = slices.DeleteFunc(c.ApprovedHooks, func(entry string) bool { return entry == name })

	if len(c.ApprovedHooks) == 0 {
		c.ApprovedHooks = nil
	}
}
