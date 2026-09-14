package adapter

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/odiumuniverse/agents-sync/pkg/mcp"
	"github.com/odiumuniverse/agents-sync/pkg/permission"
)

type Cursor struct {
	home string
}

func NewCursor(home string) *Cursor {
	return &Cursor{home: home}
}

func (c *Cursor) ID() string { return cursorID }

func (c *Cursor) DisplayName() string { return "Cursor" }

func (c *Cursor) SkillsPush() bool { return false }

func (c *Cursor) RulesPush() bool { return false }

func (c *Cursor) Detect() (bool, error) {
	return anyExists(
		filepath.Join(c.home, ".cursor", "mcp.json"),
		filepath.Join(c.home, ".cursor"),
	)
}

func (c *Cursor) Paths() []string {
	return []string{c.mcpPath(), c.skillsDir()}
}

func (c *Cursor) SkillsDir() string { return c.skillsDir() }

func (c *Cursor) WatchPaths() []string {
	return []string{c.mcpPath(), c.skillsDir()}
}

func (c *Cursor) Export(_ context.Context) (Snapshot, error) {
	snapshot, err := exportCommon("", c.skillsDir())
	if err != nil {
		return Snapshot{}, err
	}

	return exportWithExtras(snapshot, c.exportMCP, c.exportPermissions)
}

func (c *Cursor) Apply(_ context.Context, update Update) error {
	if update.Skills != nil {
		if err := applySkills(c.skillsDir(), update.Skills); err != nil {
			return fmt.Errorf("write cursor skills: %w", err)
		}
	}

	if update.MCP != nil {
		return c.applyMCP(update.MCP)
	}

	if update.Permissions != nil {
		return c.applyPermissions(update.Permissions, update.Overrides)
	}

	return nil
}

func (c *Cursor) mcpPath() string {
	return filepath.Join(c.home, ".cursor", "mcp.json")
}

func (c *Cursor) skillsDir() string {
	return filepath.Join(c.home, ".cursor", "skills")
}

func (c *Cursor) exportMCP() (mcp.Servers, bool, error) {
	return exportMCPDocument(c.mcpPath(), cursorFromEntry)
}

func (c *Cursor) applyMCP(servers mcp.Servers) error {
	return applyMCPDocument(c.mcpPath(), servers, cursorToEntry)
}

func (c *Cursor) cliConfigPath() string {
	return filepath.Join(c.home, ".cursor", "cli-config.json")
}

func (c *Cursor) exportPermissions() (permission.Rules, permission.Override, bool, error) {
	return exportPermissionBlock(c.cliConfigPath(), "permissions", cursorExportPermissions)
}

func (c *Cursor) applyPermissions(rules permission.Rules, override permission.Override) error {
	rendered := cursorRenderPermissions(rules, override)
	keys := []string{permission.EffectAllow, permission.EffectDeny}

	return applyPermissionBlock(c.cliConfigPath(), "permissions", keys, rendered)
}
