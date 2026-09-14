package adapter

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/odiumuniverse/agents-sync/pkg/mcp"
	"github.com/odiumuniverse/agents-sync/pkg/permission"
)

type GeminiCLI struct {
	home string
}

func NewGeminiCLI(home string) *GeminiCLI {
	return &GeminiCLI{home: home}
}

func (g *GeminiCLI) ID() string { return geminiID }

func (g *GeminiCLI) DisplayName() string { return "Gemini CLI" }

func (g *GeminiCLI) SkillsPush() bool { return true }

func (g *GeminiCLI) RulesPush() bool { return true }

func (g *GeminiCLI) PermissionKinds() []string { return []string{permission.KindBash} }

func (g *GeminiCLI) Detect() (bool, error) {
	return anyExists(
		filepath.Join(g.home, ".gemini", "settings.json"),
		filepath.Join(g.home, ".gemini"),
	)
}

func (g *GeminiCLI) Paths() []string {
	return []string{g.rulesPath(), g.settingsPath(), g.skillsDir()}
}

func (g *GeminiCLI) SkillsDir() string { return g.skillsDir() }

func (g *GeminiCLI) WatchPaths() []string {
	return []string{g.rulesPath(), g.settingsPath(), g.skillsDir()}
}

func (g *GeminiCLI) Export(_ context.Context) (Snapshot, error) {
	snapshot, err := exportCommon(g.rulesPath(), g.skillsDir())
	if err != nil {
		return Snapshot{}, err
	}

	return exportWithExtras(snapshot, g.exportMCP, g.exportPermissions)
}

func (g *GeminiCLI) Apply(_ context.Context, update Update) error {
	if err := applyCommon(g.rulesPath(), g.skillsDir(), update); err != nil {
		return fmt.Errorf("apply gemini config: %w", err)
	}

	if update.MCP != nil {
		return g.applyMCP(update.MCP)
	}

	if update.Permissions != nil {
		return g.applyPermissions(update.Permissions, update.Overrides)
	}

	return nil
}

func (g *GeminiCLI) rulesPath() string {
	return filepath.Join(g.home, ".gemini", "GEMINI.md")
}

func (g *GeminiCLI) settingsPath() string {
	return filepath.Join(g.home, ".gemini", "settings.json")
}

func (g *GeminiCLI) skillsDir() string {
	return filepath.Join(g.home, ".gemini", "skills")
}

func (g *GeminiCLI) exportMCP() (mcp.Servers, bool, error) {
	return exportMCPDocument(g.settingsPath(), geminiFromEntry)
}

func (g *GeminiCLI) applyMCP(servers mcp.Servers) error {
	return applyMCPDocument(g.settingsPath(), servers, geminiToEntry)
}

func (g *GeminiCLI) exportPermissions() (permission.Rules, permission.Override, bool, error) {
	return exportPermissionBlock(g.settingsPath(), "tools", geminiExportPermissions)
}

func (g *GeminiCLI) applyPermissions(rules permission.Rules, override permission.Override) error {
	rendered := geminiRenderPermissions(rules, override)
	keys := []string{"allowed", "confirmationRequired", "exclude"}

	return applyPermissionBlock(g.settingsPath(), "tools", keys, rendered)
}
