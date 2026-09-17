package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/odiumuniverse/agents-sync/pkg/agent"
	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
	"github.com/odiumuniverse/agents-sync/pkg/permission"
	"github.com/odiumuniverse/agents-sync/pkg/state"
)

var conflictFileName = regexp.MustCompile(`^(rules|mcp|skills|permissions|memory|projects)-[a-z0-9-]+-[0-9a-f]{8}\.[A-Za-z0-9]+$`)

var extPattern = regexp.MustCompile(`^\.[A-Za-z0-9]{1,8}$`)

type Take string

const (
	TakeVault   Take = "vault"
	TakeAgent   Take = "agent"
	TakeFile    Take = "file"
	TakeContent Take = "content"
)

func ParseTake(name string) (Take, error) {
	switch take := Take(strings.ToLower(name)); take {
	case TakeVault, TakeAgent, TakeFile, TakeContent:
		return take, nil
	default:
		return "", fmt.Errorf("unknown resolution %q (expected vault, agent or file)", name)
	}
}

type Resolution struct {
	Take    Take
	Content []byte
}

func (e *Engine) Conflicts() ([]state.Conflict, error) {
	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		return nil, err
	}

	return st.OpenConflicts(), nil
}

func (e *Engine) ConflictValues(c state.Conflict) ([]byte, []byte, []byte, error) {
	base, err := e.blob(c.Base)
	if err != nil {
		return nil, nil, nil, err
	}

	vaultValue, err := e.blob(c.Vault)
	if err != nil {
		return nil, nil, nil, err
	}

	local, err := e.blob(c.Local)
	if err != nil {
		return nil, nil, nil, err
	}

	return base, vaultValue, local, nil
}

func (e *Engine) ConflictFile(c state.Conflict) (string, error) {
	name, _, err := e.conflictDocument(c)
	if err != nil {
		return "", err
	}

	return filepath.Join(e.vault.ConflictsDir(), name), nil
}

func (e *Engine) Resolve(ctx context.Context, ids []string, res Resolution) (*Report, error) {
	release, err := e.lock(ctx)
	if err != nil {
		return nil, err
	}

	defer release()

	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		return nil, err
	}

	for _, id := range ids {
		c, err := st.Conflict(id)
		if err != nil {
			return nil, err
		}

		if err := e.resolveConflict(st, c, res); err != nil {
			return nil, fmt.Errorf("resolve %s: %w", c.ID(), err)
		}
	}

	if err := st.Save(e.vault.StatePath()); err != nil {
		return nil, err
	}

	return e.sync(ctx, SyncOptions{})
}

func (e *Engine) resolveConflict(st *state.State, c state.Conflict, res Resolution) error {
	spec, ok := kind.Lookup(c.Kind)
	if !ok {
		return fmt.Errorf("unknown kind %q", c.Kind)
	}

	local, err := e.blob(c.Local)
	if err != nil {
		return err
	}

	switch res.Take {
	case TakeVault:
	case TakeAgent:
		if err := e.takeAgent(spec, c, local); err != nil {
			return err
		}
	case TakeFile, TakeContent:
		content := res.Content
		if res.Take == TakeFile {
			if content, err = e.readConflictFile(c); err != nil {
				return err
			}
		}

		if err := e.takeContent(c, content); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown resolution %q", res.Take)
	}

	if err := e.setBaseKey(st, c, local); err != nil {
		return err
	}

	st.RemoveConflict(c.ID())

	if file, err := e.ConflictFile(c); err == nil {
		_ = os.Remove(file)
	}

	return nil
}

func (e *Engine) takeAgent(spec kind.Spec, c state.Conflict, local []byte) error {
	vaultItems, _, err := e.loadVault(c.Kind)
	if err != nil {
		return err
	}

	proj := projection{items: kind.Items{}, vkeys: map[string][]string{}, hidden: map[string]bool{}}

	if surface := e.surfaceOf(c.Agent, c.Kind); surface != nil {
		proj = project(vaultItems, surface)
	}

	if c.VaultKey != "" {
		proj.vkeys[c.Key] = []string{c.VaultKey}
	}

	adopt(spec, proj, vaultItems, c.Agent, c.Key, local, &KindReport{})

	return e.saveVault(c.Kind, vaultItems)
}

func (e *Engine) takeContent(c state.Conflict, content []byte) error {
	normalized, err := normalizeContent(c.Kind, content)
	if err != nil {
		return err
	}

	vaultItems, _, err := e.loadVault(c.Kind)
	if err != nil {
		return err
	}

	vaultItems[c.TargetKey()] = normalized

	return e.saveVault(c.Kind, vaultItems)
}

func (e *Engine) readConflictFile(c state.Conflict) ([]byte, error) {
	file, err := e.ConflictFile(c)
	if err != nil {
		return nil, err
	}

	if filepath.Ext(file) == ".json" {
		return nil, fmt.Errorf("%s is a summary of a structured conflict: resolve it with --take vault or --take agent", file)
	}

	data, err := os.ReadFile(file) //nolint:gosec // G304: the file lives in the vault conflicts directory
	if err != nil {
		return nil, fmt.Errorf("read conflict file: %w", err)
	}

	return data, nil
}

func normalizeContent(k kind.ID, content []byte) ([]byte, error) {
	switch k {
	case kind.MCP:
		var object map[string]any

		if err := json.Unmarshal(content, &object); err != nil || object == nil {
			return nil, errors.New("an MCP server must be a JSON object")
		}

		return kind.CanonicalJSON(content)
	case kind.Permissions:
		effect := strings.TrimSpace(string(content))
		if !permission.ValidEffect(effect) {
			return nil, fmt.Errorf("a permission rule takes allow, ask or deny, not %q", effect)
		}

		return []byte(effect), nil
	default:
		if kind.HasMarkers(content) {
			return nil, errors.New("the content still contains conflict markers")
		}

		return content, nil
	}
}

func (e *Engine) setBaseKey(st *state.State, c state.Conflict, local []byte) error {
	current, _ := st.Base(c.Kind, c.Agent)

	base := maps.Clone(current)
	if base == nil {
		base = state.Base{}
	}

	if local == nil {
		delete(base, c.Key)
	} else {
		hash, err := e.store.Put(local)
		if err != nil {
			return err
		}

		base[c.Key] = hash
	}

	st.SetBase(c.Kind, c.Agent, base)

	return nil
}

func (e *Engine) surfaceOf(agentID string, k kind.ID) agent.Surface {
	a := agent.ByID(e.agents, agentID)
	if a == nil {
		return nil
	}

	return a.Surface(k)
}

func (e *Engine) writeConflictFiles(st *state.State) error {
	dir := e.vault.ConflictsDir()

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create conflicts directory: %w", err)
	}

	wanted := map[string][]byte{}

	for _, c := range st.OpenConflicts() {
		name, content, err := e.conflictDocument(c)
		if err != nil {
			return err
		}

		wanted[name] = content
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read conflicts directory: %w", err)
	}

	for _, entry := range entries {
		if _, keep := wanted[entry.Name()]; keep || !conflictFileName.MatchString(entry.Name()) {
			continue
		}

		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return fmt.Errorf("remove stale conflict file: %w", err)
		}
	}

	for _, name := range slices.Sorted(maps.Keys(wanted)) {
		file := filepath.Join(dir, name)
		if fsutil.Exists(file) {
			continue
		}

		if err := fsutil.WriteFileAtomic(file, wanted[name], 0o600); err != nil {
			return fmt.Errorf("write conflict file: %w", err)
		}
	}

	return nil
}

func (e *Engine) conflictDocument(c state.Conflict) (string, []byte, error) {
	base, vaultValue, local, err := e.ConflictValues(c)
	if err != nil {
		return "", nil, err
	}

	prefix := fmt.Sprintf("%s-%s-%s", c.Kind, c.Agent, c.ID())

	if (c.Kind == kind.Rules || c.Kind == kind.Skills || c.Kind == kind.Memory || c.Kind == kind.Projects) && kind.IsText(base) && kind.IsText(vaultValue) && kind.IsText(local) {
		ext := ".md"

		if c.Kind == kind.Skills {
			ext = ".txt"
			if candidate := path.Ext(c.Key); extPattern.MatchString(candidate) {
				ext = candidate
			}
		}

		return prefix + ext, kind.ConflictDocument(base, vaultValue, local, "agent:"+c.Agent), nil
	}

	summary := map[string]any{
		"kind":        c.Kind,
		"agent":       c.Agent,
		"key":         c.Key,
		"vault_key":   c.TargetKey(),
		"reason":      c.Reason,
		"base":        display(c.Kind, base),
		"vault":       display(c.Kind, vaultValue),
		"agent_value": display(c.Kind, local),
		"resolve":     "agent-sync resolve " + c.ID() + " --take vault|agent",
	}

	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return "", nil, fmt.Errorf("encode conflict summary: %w", err)
	}

	return prefix + ".json", append(data, '\n'), nil
}

func display(k kind.ID, data []byte) any {
	if data == nil {
		return nil
	}

	if k == kind.MCP {
		var decoded any

		if err := json.Unmarshal(data, &decoded); err == nil {
			return decoded
		}
	}

	if utf8.Valid(data) {
		return string(data)
	}

	return fmt.Sprintf("<%d bytes of binary data>", len(data))
}
