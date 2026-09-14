package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
	"github.com/odiumuniverse/agents-sync/pkg/mcp"
	"github.com/odiumuniverse/agents-sync/pkg/merge"
	"github.com/odiumuniverse/agents-sync/pkg/skill"
)

type ResolveOptions struct {
	KeepAgent bool
	Agent     string
}

type jsonConflictFile struct {
	Resource  string               `json:"resource"`
	Agent     string               `json:"agent"`
	Conflicts []merge.JSONConflict `json:"conflicts"`
}

type rulesConflictFile struct {
	Resource  string `json:"resource"`
	Agent     string `json:"agent"`
	Conflicts int    `json:"conflicts"`
}

type skillsConflictFile struct {
	Resource  string           `json:"resource"`
	Agent     string           `json:"agent"`
	Conflicts []skill.Conflict `json:"conflicts"`
}

func (e *Engine) Resolve(ctx context.Context, resource string, opts ResolveOptions) error {
	entry := e.state.Resources[resource]
	if entry == nil || !entry.Conflict {
		return fmt.Errorf("resource %s has no unresolved conflict", resource)
	}

	switch resource {
	case ResourceRules:
		if err := e.resolveRules(); err != nil {
			return err
		}
	case ResourceMCP:
		if err := e.resolveMCP(opts); err != nil {
			return err
		}
	case ResourceSkills:
		if err := e.resolveSkills(opts); err != nil {
			return err
		}
	case ResourcePermissions:
		if err := e.resolvePermissions(opts); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown resource %q", resource)
	}

	entry.Conflict = false

	if err := e.state.Save(e.vault.RegistryPath()); err != nil {
		return err
	}

	_, err := e.Run(ctx, ModePush)

	return err
}

func (e *Engine) resolveRules() error {
	rules, err := readOptionalFile(filepath.Join(e.vault.Root(), "rules", "base.md"))
	if err != nil {
		return err
	}

	if hasMarkers(rules) {
		return errors.New("rules still contain conflict markers: edit the vault file and retry")
	}

	for _, a := range e.adapters {
		if err := e.removeConflictArtifacts(ResourceRules, a.ID()); err != nil {
			return err
		}
	}

	return nil
}

func (e *Engine) resolveMCP(opts ResolveOptions) error {
	if !opts.KeepAgent {
		return e.clearConflictArtifacts(ResourceMCP)
	}

	agent, err := e.resolveAgent(ResourceMCP, opts.Agent)
	if err != nil {
		return err
	}

	conflicts, err := readJSONConflictFile(e.conflictPath(ResourceMCP, agent))
	if err != nil {
		return err
	}

	path := filepath.Join(e.vault.Root(), "mcp", "servers.json")

	serversData, err := readOptionalFile(path)
	if err != nil {
		return err
	}

	servers, err := mcp.ParseCanonical(serversData)
	if err != nil {
		return err
	}

	anyServers := toAny(servers)

	for _, conflict := range conflicts.Conflicts {
		setPath(anyServers, strings.Split(conflict.Path, "."), conflict.Agent)
	}

	out, err := fromAny(anyServers).MarshalCanonical()
	if err != nil {
		return err
	}

	if err := fsutil.WriteFileAtomic(path, out, 0o600); err != nil {
		return fmt.Errorf("write vault servers: %w", err)
	}

	return e.removeConflictArtifacts(ResourceMCP, agent)
}

func (e *Engine) resolveSkills(opts ResolveOptions) error {
	if !opts.KeepAgent {
		return e.clearConflictArtifacts(ResourceSkills)
	}

	agent, err := e.resolveAgent(ResourceSkills, opts.Agent)
	if err != nil {
		return err
	}

	conflicts, err := readSkillsConflictFile(e.conflictPath(ResourceSkills, agent))
	if err != nil {
		return err
	}

	for _, conflict := range conflicts.Conflicts {
		if err := e.applySkillConflict(agent, conflict); err != nil {
			return err
		}
	}

	return e.removeConflictArtifacts(ResourceSkills, agent)
}

func (e *Engine) applySkillConflict(agent string, conflict skill.Conflict) error {
	vaultSkills := filepath.Join(e.vault.Root(), "skills")

	path, err := skill.FilePath(vaultSkills, conflict.Skill, conflict.Path)
	if err != nil {
		return err
	}

	if conflict.Kind == skill.ConflictDeleted {
		err := os.Remove(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove vault skill file: %w", err)
		}

		return nil
	}

	variantPath, err := skill.FilePath(e.skillsConflictDir(agent), conflict.Skill, conflict.Path)
	if err != nil {
		return err
	}

	content, err := os.ReadFile(variantPath) //nolint:gosec // G304: conflict paths are resolved by the tool
	if err != nil {
		return fmt.Errorf("read skill variant: %w", err)
	}

	if err := skill.WriteTree(vaultSkills, conflict.Skill, skill.Tree{conflict.Path: content}); err != nil {
		return err
	}

	return nil
}

func (e *Engine) resolveAgent(resource, agent string) (string, error) {
	if agent != "" {
		return agent, nil
	}

	agents, err := e.conflictedAgents(resource)
	if err != nil {
		return "", err
	}

	if len(agents) != 1 {
		return "", fmt.Errorf("--agent is required: %d agents have conflicts", len(agents))
	}

	return agents[0], nil
}

func readJSONConflictFile(path string) (jsonConflictFile, error) {
	conflicts := jsonConflictFile{}

	if err := readConflictJSON(path, &conflicts); err != nil {
		return jsonConflictFile{}, err
	}

	return conflicts, nil
}

func readSkillsConflictFile(path string) (skillsConflictFile, error) {
	conflicts := skillsConflictFile{}

	if err := readConflictJSON(path, &conflicts); err != nil {
		return skillsConflictFile{}, err
	}

	return conflicts, nil
}

func readConflictJSON(path string, dst any) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: conflict paths are built from vault paths and agent IDs
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no conflict file at %s", path)
	}

	if err != nil {
		return fmt.Errorf("read conflict file: %w", err)
	}

	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("parse conflict file: %w", err)
	}

	return nil
}

func (e *Engine) clearConflictArtifacts(resource string) error {
	for _, a := range e.adapters {
		if err := e.removeConflictArtifacts(resource, a.ID()); err != nil {
			return err
		}
	}

	return nil
}

func (e *Engine) conflictedMCPServerNames() (map[string]struct{}, error) {
	agents, err := e.conflictedAgents(ResourceMCP)
	if err != nil {
		return nil, err
	}

	names := map[string]struct{}{}

	for _, agent := range agents {
		conflicts, err := readJSONConflictFile(e.conflictPath(ResourceMCP, agent))
		if err != nil {
			return nil, err
		}

		for _, conflict := range conflicts.Conflicts {
			name, _, _ := strings.Cut(conflict.Path, ".")
			if name != "" {
				names[name] = struct{}{}
			}
		}
	}

	return names, nil
}

func (e *Engine) conflictedAgents(resource string) ([]string, error) {
	entries, err := os.ReadDir(e.vault.ConflictsDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}

		return nil, fmt.Errorf("read conflicts directory: %w", err)
	}

	var agents []string

	prefix := resource + "-"

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
			continue
		}

		agents = append(agents, strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".json"))
	}

	return agents, nil
}

func (e *Engine) conflictPath(resource, agent string) string {
	return filepath.Join(e.vault.ConflictsDir(), resource+"-"+agent+".json")
}

func (e *Engine) skillsConflictDir(agent string) string {
	return filepath.Join(e.vault.ConflictsDir(), ResourceSkills+"-"+agent)
}

func (e *Engine) writeRulesConflict(agent string, count int) error {
	return e.writeConflictFile(ResourceRules, agent, rulesConflictFile{
		Resource:  ResourceRules,
		Agent:     agent,
		Conflicts: count,
	})
}

func (e *Engine) writeMCPConflict(agent string, conflicts []merge.JSONConflict) error {
	return e.writeJSONConflict(ResourceMCP, agent, conflicts)
}

func (e *Engine) writeJSONConflict(resource, agent string, conflicts []merge.JSONConflict) error {
	return e.writeConflictFile(resource, agent, jsonConflictFile{
		Resource:  resource,
		Agent:     agent,
		Conflicts: conflicts,
	})
}

func (e *Engine) writeSkillsConflict(agent string, agentSkills map[string]skill.Tree, conflicts []skill.Conflict) error {
	if err := e.writeConflictFile(ResourceSkills, agent, skillsConflictFile{
		Resource:  ResourceSkills,
		Agent:     agent,
		Conflicts: conflicts,
	}); err != nil {
		return err
	}

	dir := e.skillsConflictDir(agent)

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clean conflict variants: %w", err)
	}

	for _, conflict := range conflicts {
		content, ok := agentSkills[conflict.Skill][conflict.Path]
		if !ok {
			continue
		}

		path, err := skill.FilePath(dir, conflict.Skill, conflict.Path)
		if err != nil {
			return err
		}

		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return fmt.Errorf("create conflict variant directory: %w", err)
		}

		if err := fsutil.WriteFileAtomic(path, content, 0o600); err != nil {
			return fmt.Errorf("write conflict variant: %w", err)
		}
	}

	return nil
}

func (e *Engine) writeConflictFile(resource, agent string, content any) error {
	data, err := json.MarshalIndent(content, "", "  ")
	if err != nil {
		return fmt.Errorf("encode conflict file: %w", err)
	}

	if err := fsutil.WriteFileAtomic(e.conflictPath(resource, agent), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write conflict file: %w", err)
	}

	return nil
}

func (e *Engine) removeConflictArtifacts(resource, agent string) error {
	err := os.Remove(e.conflictPath(resource, agent))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove conflict file: %w", err)
	}

	if resource != ResourceSkills {
		return nil
	}

	if err := os.RemoveAll(e.skillsConflictDir(agent)); err != nil {
		return fmt.Errorf("remove conflict variants: %w", err)
	}

	return nil
}

func setPath(root map[string]any, parts []string, value any) {
	if len(parts) == 0 {
		return
	}

	if len(parts) == 1 {
		if value == nil {
			delete(root, parts[0])
		} else {
			root[parts[0]] = value
		}

		return
	}

	child, ok := root[parts[0]].(map[string]any)
	if !ok {
		return
	}

	setPath(child, parts[1:], value)
}
