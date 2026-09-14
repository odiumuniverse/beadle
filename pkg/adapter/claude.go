package adapter

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/odiumuniverse/agents-sync/pkg/mcp"
	"github.com/odiumuniverse/agents-sync/pkg/permission"
)

type ClaudeCode struct {
	home string
}

func NewClaudeCode(home string) *ClaudeCode {
	return &ClaudeCode{home: home}
}

func (c *ClaudeCode) ID() string { return claudeID }

func (c *ClaudeCode) DisplayName() string { return "Claude Code" }

func (c *ClaudeCode) SkillsPush() bool { return true }

func (c *ClaudeCode) RulesPush() bool { return true }

func (c *ClaudeCode) Paths() []string {
	return []string{c.rulesPath(), c.settingsPath(), c.configPath(), c.skillsDir()}
}

func (c *ClaudeCode) SkillsDir() string { return c.skillsDir() }

func (c *ClaudeCode) WatchPaths() []string {
	return []string{c.rulesPath(), c.settingsPath(), c.configPath(), c.skillsDir()}
}

func (c *ClaudeCode) Detect() (bool, error) {
	return anyExists(
		filepath.Join(c.home, ".claude.json"),
		filepath.Join(c.home, ".claude"),
	)
}

func (c *ClaudeCode) Export(_ context.Context) (Snapshot, error) {
	snapshot, err := exportCommon(c.rulesPath(), c.skillsDir())
	if err != nil {
		return Snapshot{}, err
	}

	return exportWithExtras(snapshot, c.exportMCP, c.exportPermissions)
}

func (c *ClaudeCode) Apply(_ context.Context, update Update) error {
	if err := applyCommon(c.rulesPath(), c.skillsDir(), update); err != nil {
		return fmt.Errorf("apply claude config: %w", err)
	}

	if update.MCP != nil {
		return c.applyMCP(update.MCP)
	}

	if update.Permissions != nil {
		return c.applyPermissions(update.Permissions, update.Overrides)
	}

	return nil
}

func (c *ClaudeCode) settingsPath() string {
	return filepath.Join(c.home, ".claude", "settings.json")
}

func (c *ClaudeCode) exportPermissions() (permission.Rules, permission.Override, bool, error) {
	return exportPermissionBlock(c.settingsPath(), "permissions", claudeExportPermissions)
}

func (c *ClaudeCode) applyPermissions(rules permission.Rules, override permission.Override) error {
	rendered := claudeRenderPermissions(rules, override)
	keys := []string{permission.EffectAllow, permission.EffectAsk, permission.EffectDeny}

	return applyPermissionBlock(c.settingsPath(), "permissions", keys, rendered)
}

func (c *ClaudeCode) skillsDir() string {
	return filepath.Join(c.home, ".claude", "skills")
}

func (c *ClaudeCode) rulesPath() string {
	return filepath.Join(c.home, ".claude", "CLAUDE.md")
}

func (c *ClaudeCode) configPath() string {
	return filepath.Join(c.home, ".claude.json")
}

func (c *ClaudeCode) exportMCP() (mcp.Servers, bool, error) {
	return exportMCPDocument(c.configPath(), claudeFromEntry)
}

func (c *ClaudeCode) applyMCP(servers mcp.Servers) error {
	return applyMCPDocument(c.configPath(), servers, claudeToEntry)
}
