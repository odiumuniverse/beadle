package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
	"github.com/odiumuniverse/agents-sync/pkg/mcp"
	"github.com/odiumuniverse/agents-sync/pkg/permission"
	"github.com/odiumuniverse/agents-sync/pkg/skill"
)

type Adapter interface {
	ID() string
	DisplayName() string
	Detect() (bool, error)
	Export(ctx context.Context) (Snapshot, error)
	Apply(ctx context.Context, update Update) error
	SkillsPush() bool
	RulesPush() bool
	Paths() []string
	SkillsDir() string
	WatchPaths() []string
}

type Snapshot struct {
	Rules               []byte
	MCP                 mcp.Servers
	MCPPresent          bool
	Skills              map[string]skill.Tree
	Permissions         permission.Rules
	PermissionsOverride permission.Override
	PermissionsPresent  bool
}

type Update struct {
	Rules       []byte
	MCP         mcp.Servers
	Skills      map[string]skill.Tree
	Permissions permission.Rules
	Overrides   permission.Override
}

func All(home string) []Adapter {
	return []Adapter{
		NewClaudeCode(home),
		NewOpenCode(home),
		NewGeminiCLI(home),
		NewCursor(home),
	}
}

func anyExists(paths ...string) (bool, error) {
	for _, path := range paths {
		_, err := os.Stat(path)
		if err == nil {
			return true, nil
		}

		if !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("stat %s: %w", path, err)
		}
	}

	return false, nil
}

func readFileIfExists(path string) ([]byte, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: agent config paths are resolved by the adapter
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	return data, nil
}

func writeFilePreserveMode(path string, data []byte, defaultPerm fs.FileMode) error {
	perm := defaultPerm

	info, err := os.Stat(path)
	switch {
	case err == nil:
		perm = info.Mode().Perm()
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("stat %s: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	return fsutil.WriteFileAtomic(path, data, perm)
}

func exportWithExtras(
	snapshot Snapshot,
	exportMCP func() (mcp.Servers, bool, error),
	exportPerms func() (permission.Rules, permission.Override, bool, error),
) (Snapshot, error) {
	servers, mcpPresent, err := exportMCP()
	if err != nil {
		return Snapshot{}, err
	}

	snapshot.MCP = servers
	snapshot.MCPPresent = mcpPresent

	rules, override, permsPresent, err := exportPerms()
	if err != nil {
		return Snapshot{}, err
	}

	snapshot.Permissions = rules
	snapshot.PermissionsOverride = override
	snapshot.PermissionsPresent = permsPresent

	return snapshot, nil
}

func applyCommon(rulesPath, skillsDir string, update Update) error {
	if update.Rules != nil && rulesPath != "" {
		if err := writeFilePreserveMode(rulesPath, update.Rules, 0o644); err != nil {
			return err
		}
	}

	if update.Skills != nil {
		if err := applySkills(skillsDir, update.Skills); err != nil {
			return err
		}
	}

	return nil
}

func exportCommon(rulesPath, skillsDir string) (Snapshot, error) {
	snapshot := Snapshot{}

	if rulesPath != "" {
		rules, err := readFileIfExists(rulesPath)
		if err != nil {
			return Snapshot{}, err
		}

		snapshot.Rules = rules
	}

	skills, err := readSkills(skillsDir)
	if err != nil {
		return Snapshot{}, err
	}

	snapshot.Skills = skills

	return snapshot, nil
}

func applySkills(dir string, skills map[string]skill.Tree) error {
	for name, tree := range skills {
		if err := skill.WriteTree(dir, name, tree); err != nil {
			return err
		}
	}

	return nil
}

func readSkills(dir string) (map[string]skill.Tree, error) {
	if !fsutil.Exists(dir) {
		return nil, nil
	}

	return skill.ReadDir(dir)
}

func toStringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}

	out := make([]string, 0, len(items))

	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}

	return out
}

func toStringMap(value any) map[string]string {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil
	}

	out := make(map[string]string, len(obj))

	for key, item := range obj {
		if s, ok := item.(string); ok {
			out[key] = s
		}
	}

	return out
}

func marshalExtension(agentID string, extra map[string]any) map[string]json.RawMessage {
	if len(extra) == 0 {
		return nil
	}

	data, err := json.Marshal(extra)
	if err != nil {
		return nil
	}

	return map[string]json.RawMessage{agentID: data}
}

func mergeExtension(entry map[string]any, extensions map[string]json.RawMessage, agentID string) {
	raw, ok := extensions[agentID]
	if !ok {
		return
	}

	extra := map[string]any{}
	if err := json.Unmarshal(raw, &extra); err != nil {
		return
	}

	for key, value := range extra {
		if _, exists := entry[key]; !exists {
			entry[key] = value
		}
	}
}
