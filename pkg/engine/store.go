package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/odiumuniverse/agents-sync/pkg/cas"
	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
	"github.com/odiumuniverse/agents-sync/pkg/memory"
	"github.com/odiumuniverse/agents-sync/pkg/permission"
	"github.com/odiumuniverse/agents-sync/pkg/skill"
	"github.com/odiumuniverse/agents-sync/pkg/state"
)

func (e *Engine) loadVault(k kind.ID) (kind.Items, bool, error) {
	switch k {
	case kind.Rules:
		data, present, err := readOptional(e.vault.RulesPath())
		if err != nil || !present {
			return kind.Items{}, false, err
		}

		return normalize(kind.Items{kind.RulesKey: data}), false, nil
	case kind.MCP:
		return e.loadServers()
	case kind.Skills:
		trees, err := skill.ReadDir(e.vault.SkillsDir())
		if err != nil {
			return nil, false, err
		}

		return normalize(skill.Flatten(trees)), false, nil
	case kind.Permissions:
		items, err := e.loadPermissions()

		return items, false, err
	case kind.Memory:
		items, _, err := loadNotesDir(e.vault.MemoryDir())
		if err != nil {
			return nil, false, err
		}

		guarded, extracted := e.guardNoteSecrets(items)

		return guarded, extracted > 0, nil
	case kind.Projects:
		return loadNotesDir(e.vault.ProjectsDir())
	default:
		return nil, false, fmt.Errorf("unknown kind %q", k)
	}
}

func (e *Engine) loadServers() (kind.Items, bool, error) {
	path := e.vault.ServersPath()

	data, present, err := readOptional(path)
	if err != nil {
		return nil, false, err
	}

	if !present || len(bytes.TrimSpace(data)) == 0 {
		return kind.Items{}, false, nil
	}

	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", path, err)
	}

	items := make(kind.Items, len(raw))

	for name, entry := range raw {
		var object map[string]any

		if err := json.Unmarshal(entry, &object); err != nil || object == nil {
			return nil, false, fmt.Errorf("server %q in %s is not a JSON object", name, path)
		}

		canonical, err := kind.CanonicalJSON(entry)
		if err != nil {
			return nil, false, fmt.Errorf("server %q in %s: %w", name, path, err)
		}

		items[name] = canonical
	}

	return e.inbound(kind.MCP, items)
}

func (e *Engine) loadPermissions() (kind.Items, error) {
	path := e.vault.PermissionsPath()

	data, present, err := readOptional(path)
	if err != nil || !present {
		return kind.Items{}, err
	}

	rules, err := permission.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	items := make(kind.Items, len(rules))

	for key, effect := range rules {
		if _, _, _, ok := permission.Split(key); !ok {
			return nil, fmt.Errorf("%s: invalid rule %q (expected bash:<pattern>, tool:<name> or mcp:<server>:<tool>)", path, key)
		}

		if !permission.ValidEffect(effect) {
			return nil, fmt.Errorf("%s: rule %q has effect %q (expected allow, ask or deny)", path, key, effect)
		}

		items[key] = []byte(effect)
	}

	return items, nil
}

func (e *Engine) saveVault(k kind.ID, items kind.Items) error {
	switch k {
	case kind.Rules:
		content, ok := items[kind.RulesKey]
		if !ok {
			return nil
		}

		return writeVaultFile(e.vault.RulesPath(), content)
	case kind.MCP:
		doc := make(map[string]json.RawMessage, len(items))

		for name, data := range items {
			doc[name] = data
		}

		return writeVaultJSON(e.vault.ServersPath(), doc)
	case kind.Skills:
		return e.saveSkills(items)
	case kind.Memory:
		guarded, _ := e.guardNoteSecrets(items)

		return saveNotesDir(e.vault.MemoryDir(), "memory", guarded)
	case kind.Projects:
		return saveNotesDir(e.vault.ProjectsDir(), "project", items)
	case kind.Permissions:
		rules := make(map[string]string, len(items))

		for key, data := range items {
			rules[key] = string(data)
		}

		return writeVaultJSON(e.vault.PermissionsPath(), rules)
	default:
		return fmt.Errorf("unknown kind %q", k)
	}
}

func (e *Engine) saveSkills(items kind.Items) error {
	dir := e.vault.SkillsDir()

	current, err := skill.ReadDir(dir)
	if err != nil {
		return err
	}

	want := skill.Group(items)

	for _, name := range slices.Sorted(maps.Keys(current)) {
		if _, keep := want[name]; keep {
			continue
		}

		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("remove vault skill %s: %w", name, err)
		}
	}

	for _, name := range slices.Sorted(maps.Keys(want)) {
		if maps.EqualFunc(current[name], want[name], bytes.Equal) {
			continue
		}

		if err := skill.SyncTree(dir, name, want[name]); err != nil {
			return fmt.Errorf("write vault skill %s: %w", name, err)
		}
	}

	return nil
}

func loadNotesDir(dir string) (kind.Items, bool, error) {
	trees, err := memory.ReadDir(dir)
	if err != nil {
		return nil, false, err
	}

	return normalize(memory.Flatten(trees)), false, nil
}

func saveNotesDir(dir, label string, items kind.Items) error {
	current, err := memory.ReadDir(dir)
	if err != nil {
		return err
	}

	want := memory.Group(items)

	if err := removeStaleSlugs(dir, label, current, want); err != nil {
		return err
	}

	for _, slug := range slices.Sorted(maps.Keys(want)) {
		if maps.EqualFunc(current[slug], want[slug], bytes.Equal) {
			continue
		}

		if err := syncNotesSlug(dir, label, slug, current[slug], want[slug]); err != nil {
			return err
		}
	}

	return nil
}

func removeStaleSlugs(dir, label string, current, want map[string]memory.Tree) error {
	for _, slug := range slices.Sorted(maps.Keys(current)) {
		if _, keep := want[slug]; keep {
			continue
		}

		path := filepath.Join(dir, slug)

		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() {
			continue
		}

		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove vault %s %s: %w", label, slug, err)
		}
	}

	return nil
}

func syncNotesSlug(dir, label, slug string, current, want memory.Tree) error {
	for _, note := range slices.Sorted(maps.Keys(current)) {
		if _, keep := want[note]; keep {
			continue
		}

		if err := os.Remove(filepath.Join(dir, slug, note)); err != nil {
			return fmt.Errorf("remove vault %s note %s/%s: %w", label, slug, note, err)
		}
	}

	if err := memory.SyncTree(dir, slug, want); err != nil {
		return fmt.Errorf("write vault %s %s: %w", label, slug, err)
	}

	return nil
}

func (e *Engine) snapshot(st *state.State, k kind.ID, items kind.Items) error {
	if len(items) == 0 && len(st.History(k)) == 0 {
		return nil
	}

	manifest := make(map[string]cas.Hash, len(items))

	for key, data := range items {
		hash, err := e.store.Put(data)
		if err != nil {
			return err
		}

		manifest[key] = hash
	}

	data, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode snapshot: %w", err)
	}

	hash, err := e.store.Put(data)
	if err != nil {
		return err
	}

	st.AddSnapshot(k, state.Snapshot{At: e.now().UTC(), Manifest: hash})

	return nil
}

func (e *Engine) loadSnapshot(snap state.Snapshot) (kind.Items, error) {
	data, err := e.store.Get(snap.Manifest)
	if err != nil {
		return nil, err
	}

	manifest := map[string]cas.Hash{}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse snapshot %s: %w", snap.Manifest, err)
	}

	items := make(kind.Items, len(manifest))

	for key, hash := range manifest {
		blob, err := e.store.Get(hash)
		if err != nil {
			return nil, err
		}

		items[key] = blob
	}

	return normalize(items), nil
}

func (e *Engine) blob(hash cas.Hash) ([]byte, error) {
	if hash == "" {
		return nil, nil
	}

	data, err := e.store.Get(hash)
	if err != nil {
		return nil, err
	}

	if data == nil {
		data = []byte{}
	}

	return data, nil
}

func readOptional(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: vault paths are resolved by the vault package
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}

	return data, true, nil
}

func writeVaultFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}

	if err := fsutil.WriteFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}

func writeVaultJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}

	return writeVaultFile(path, append(data, '\n'))
}
