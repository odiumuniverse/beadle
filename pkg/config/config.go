package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
	"github.com/odiumuniverse/agents-sync/pkg/permission"
	"github.com/odiumuniverse/agents-sync/pkg/secret"
)

const FileName = "config.json"

const CurrentVersion = 2

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
	Enabled bool             `json:"enabled"`
	Modes   map[kind.ID]Mode `json:"modes,omitempty"`
	Skills  string           `json:"skills,omitempty"`
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
	Version     int              `json:"version"`
	Agents      map[string]Agent `json:"agents,omitempty"`
	Kinds       map[kind.ID]Mode `json:"kinds,omitempty"`
	Permissions string           `json:"permissions,omitempty"`
	History     string           `json:"history,omitempty"`
	Secrets     string           `json:"secrets,omitempty"`
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
	return &Config{Version: CurrentVersion, Agents: map[string]Agent{}}
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

	if cfg.Agents == nil {
		cfg.Agents = map[string]Agent{}
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}

	return cfg, nil
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
