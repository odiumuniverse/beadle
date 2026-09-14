package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
	"github.com/odiumuniverse/agents-sync/pkg/permission"
)

const FileName = "config.json"

const CurrentVersion = 1

const (
	SkillsSync     = "sync"
	SkillsPullOnly = "pull-only"
	SkillsOff      = "off"
)

type Agent struct {
	Enabled bool   `json:"enabled"`
	Skills  string `json:"skills,omitempty"`
}

func (a Agent) SkillsMode() string {
	switch a.Skills {
	case SkillsPullOnly, SkillsOff:
		return a.Skills
	default:
		return SkillsSync
	}
}

const (
	HistoryOff = "off"
	HistoryGit = "git"
)

type Config struct {
	Version     int              `json:"version"`
	Agents      map[string]Agent `json:"agents,omitempty"`
	Permissions string           `json:"permissions,omitempty"`
	History     string           `json:"history,omitempty"`
}

func (c *Config) HistoryMode() string {
	if c.History == HistoryGit {
		return HistoryGit
	}

	return HistoryOff
}

func (c *Config) PermissionsMode() string {
	if c.Permissions == permission.ModeSync {
		return permission.ModeSync
	}

	return permission.ModeOff
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

	return cfg, nil
}

func (c *Config) Save(path string) error {
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

	c.Agents[agentID] = Agent{Enabled: true}
}
