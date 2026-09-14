package adapter

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
	"github.com/odiumuniverse/agents-sync/pkg/mcp"
	"github.com/odiumuniverse/agents-sync/pkg/permission"
)

type OpenCode struct {
	home string
}

func NewOpenCode(home string) *OpenCode {
	return &OpenCode{home: home}
}

func (o *OpenCode) ID() string { return openCodeID }

func (o *OpenCode) DisplayName() string { return "OpenCode" }

func (o *OpenCode) SkillsPush() bool { return false }

func (o *OpenCode) RulesPush() bool { return true }

func (o *OpenCode) Paths() []string {
	return []string{o.rulesPath(), o.configDir(), o.skillsDir()}
}

func (o *OpenCode) SkillsDir() string { return o.skillsDir() }

func (o *OpenCode) WatchPaths() []string {
	paths := []string{o.rulesPath(), o.skillsDir()}

	if configPath := o.existingConfigFile(); configPath != "" {
		paths = append(paths, configPath)
	}

	return paths
}

func (o *OpenCode) existingConfigFile() string {
	for _, name := range []string{"opencode.jsonc", "opencode.json"} {
		path := filepath.Join(o.configDir(), name)
		if fsutil.Exists(path) {
			return path
		}
	}

	return ""
}

func (o *OpenCode) Detect() (bool, error) {
	dir := o.configDir()

	return anyExists(
		dir,
		filepath.Join(dir, "opencode.json"),
		filepath.Join(dir, "opencode.jsonc"),
	)
}

func (o *OpenCode) Export(_ context.Context) (Snapshot, error) {
	snapshot, err := exportCommon(o.rulesPath(), o.skillsDir())
	if err != nil {
		return Snapshot{}, err
	}

	return exportWithExtras(snapshot, o.exportMCP, o.exportPermissions)
}

func (o *OpenCode) Apply(_ context.Context, update Update) error {
	if err := applyCommon(o.rulesPath(), o.skillsDir(), update); err != nil {
		return fmt.Errorf("apply opencode config: %w", err)
	}

	if update.MCP != nil {
		return o.applyMCP(update.MCP)
	}

	if update.Permissions != nil {
		return o.applyPermissions(update.Permissions, update.Overrides)
	}

	return nil
}

func (o *OpenCode) exportPermissions() (permission.Rules, permission.Override, bool, error) {
	_, data, err := o.configFile()
	if err != nil {
		return nil, permission.Override{}, false, err
	}

	if data == nil {
		return nil, permission.Override{}, false, nil
	}

	var raw map[string]any

	if err := findJSON(data, "/permission", &raw); err != nil {
		return nil, permission.Override{}, false, fmt.Errorf("read opencode permission section: %w", err)
	}

	rules, override := openCodeExportPermissions(raw)

	return rules, override, true, nil
}

func (o *OpenCode) applyPermissions(rules permission.Rules, override permission.Override) error {
	path, data, err := o.configFile()
	if err != nil {
		return err
	}

	if data == nil {
		return fmt.Errorf("opencode config: %w", ErrNotConfigured)
	}

	out, err := patchJSON(data, "/permission", openCodeRenderPermissions(rules, override))
	if err != nil {
		return fmt.Errorf("patch opencode config: %w", err)
	}

	if err := fsutil.WriteFileAtomic(path, out, 0o600); err != nil {
		return fmt.Errorf("write opencode config: %w", err)
	}

	return nil
}

func (o *OpenCode) skillsDir() string {
	return filepath.Join(o.configDir(), "skills")
}

func (o *OpenCode) rulesPath() string {
	return filepath.Join(o.configDir(), "AGENTS.md")
}

func (o *OpenCode) configDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode")
	}

	return filepath.Join(o.home, ".config", "opencode")
}

func (o *OpenCode) configFile() (string, []byte, error) {
	for _, name := range []string{"opencode.jsonc", "opencode.json"} {
		path := filepath.Join(o.configDir(), name)

		data, err := os.ReadFile(path) //nolint:gosec // G304: agent config path is resolved by the adapter
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			return "", nil, fmt.Errorf("read %s: %w", path, err)
		}

		return path, data, nil
	}

	return "", nil, nil
}

func (o *OpenCode) exportMCP() (mcp.Servers, bool, error) {
	_, data, err := o.configFile()
	if err != nil {
		return nil, false, err
	}

	if data == nil {
		return mcp.Servers{}, false, nil
	}

	var raw map[string]map[string]any

	if err := findJSON(data, "/mcp", &raw); err != nil {
		return nil, false, fmt.Errorf("read opencode mcp section: %w", err)
	}

	servers := make(mcp.Servers, len(raw))
	for name, entry := range raw {
		servers[name] = openCodeFromEntry(entry)
	}

	return servers, true, nil
}

func (o *OpenCode) applyMCP(servers mcp.Servers) error {
	path, data, err := o.configFile()
	if err != nil {
		return err
	}

	if data == nil {
		return fmt.Errorf("opencode config: %w", ErrNotConfigured)
	}

	rendered := make(map[string]any, len(servers))
	for name, server := range servers {
		rendered[name] = openCodeToEntry(server)
	}

	out, err := patchJSON(data, "/mcp", rendered)
	if err != nil {
		return fmt.Errorf("patch opencode config: %w", err)
	}

	if err := fsutil.WriteFileAtomic(path, out, 0o600); err != nil {
		return fmt.Errorf("write opencode config: %w", err)
	}

	return nil
}
