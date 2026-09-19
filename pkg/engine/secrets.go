package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/kind"
	proj "github.com/odiumuniverse/beadle/pkg/project"
	"github.com/odiumuniverse/beadle/pkg/secret"
	"github.com/odiumuniverse/beadle/pkg/state"
)

func (e *Engine) Secrets() *secret.Store {
	return e.secrets
}

func (e *Engine) SecretsMode() string {
	return e.config.SecretsMode()
}

func (e *Engine) inbound(k kind.ID, items kind.Items) (kind.Items, bool, error) {
	switch k {
	case kind.MCP:
		return e.inboundMCP(items)
	case kind.Memory:
		out, extracted := e.guardNoteSecrets(items)

		return out, extracted > 0, nil
	case kind.Projects:
		return e.inboundProjects(items)
	default:
		return items, false, nil
	}
}

func (e *Engine) inboundMCP(items kind.Items) (kind.Items, bool, error) {
	if len(items) == 0 {
		return items, false, nil
	}

	out := make(kind.Items, len(items))
	changed := false

	for name, data := range items {
		extracted, replaced, err := secret.ExtractJSON(data, e.secrets)
		if err != nil {
			return nil, false, fmt.Errorf("server %s: %w", name, err)
		}

		if replaced {
			if extracted, err = kind.CanonicalJSON(extracted); err != nil {
				return nil, false, fmt.Errorf("server %s: %w", name, err)
			}

			changed = true
		}

		out[name] = extracted
	}

	return out, changed, nil
}

func (e *Engine) outbound(k kind.ID, items kind.Items) (kind.Items, []string, error) {
	switch k {
	case kind.MCP:
		return e.outboundMCP(items)
	case kind.Memory:
		return e.outboundNotes(items)
	case kind.Projects:
		return e.outboundProjects(items)
	default:
		return items, nil, nil
	}
}

func (e *Engine) outboundMCP(items kind.Items) (kind.Items, []string, error) {
	if len(items) == 0 {
		return items, nil, nil
	}

	out := make(kind.Items, len(items))
	missing := map[string]struct{}{}

	for name, data := range items {
		resolved, absent, err := secret.ResolveJSON(data, e.secrets, e.config.SecretsMode())
		if err != nil {
			return nil, nil, fmt.Errorf("server %s: %w", name, err)
		}

		for _, n := range absent {
			missing[n] = struct{}{}
		}

		out[name] = resolved
	}

	return out, slices.Sorted(maps.Keys(missing)), nil
}

func (e *Engine) outboundNotes(items kind.Items) (kind.Items, []string, error) {
	if len(items) == 0 {
		return items, nil, nil
	}

	out := make(kind.Items, len(items))
	missing := map[string]struct{}{}

	for key, data := range items {
		resolved, absent := secret.ResolveText(data, e.secrets)

		for _, name := range absent {
			missing[name] = struct{}{}
		}

		out[key] = resolved
	}

	return out, slices.Sorted(maps.Keys(missing)), nil
}

func (e *Engine) guardNoteSecrets(items kind.Items) (kind.Items, int) {
	if len(items) == 0 {
		return items, 0
	}

	out := make(kind.Items, len(items))
	extracted := 0

	for key, data := range items {
		replaced, names, changed, err := secret.ExtractText(data, e.secrets)
		if err != nil {
			e.log.Error(context.Background(), "note secret scan failed", "note", key, "err", err)

			replaced = data
		}

		if changed {
			extracted += len(names)
			e.log.Print(context.Background(), "note secrets moved to the vault store", "note", key, "count", len(names))
		}

		out[key] = replaced
	}

	return out, extracted
}

func (e *Engine) PruneSecrets(ctx context.Context) ([]string, error) {
	release, err := e.lock(ctx)
	if err != nil {
		return nil, err
	}

	defer release()

	refs, err := e.secretRefs()
	if err != nil {
		return nil, err
	}

	var removed []string

	for _, name := range e.secrets.Names() {
		if slices.Contains(refs, name) {
			continue
		}

		if e.secrets.Delete(name) {
			removed = append(removed, name)
		}
	}

	if err := e.secrets.Save(); err != nil {
		return nil, err
	}

	return removed, nil
}

func (e *Engine) secretRefs() ([]string, error) {
	names, err := e.vaultRefs()
	if err != nil {
		return nil, err
	}

	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		return nil, err
	}

	for _, c := range st.OpenConflicts() {
		for _, hash := range []cas.Hash{c.Base, c.Vault, c.Local} {
			data, err := e.blob(hash)
			if err != nil {
				return nil, err
			}

			if data == nil {
				continue
			}

			refs, err := refsOf(c.Kind, c.TargetKey(), data)
			if err != nil {
				return nil, err
			}

			for _, name := range refs {
				names[name] = struct{}{}
			}
		}
	}

	return slices.Sorted(maps.Keys(names)), nil
}

func (e *Engine) vaultRefs() (map[string]struct{}, error) {
	names := map[string]struct{}{}

	for _, k := range []kind.ID{kind.MCP, kind.Memory, kind.Projects} {
		items, _, err := e.loadVault(k)
		if err != nil {
			return nil, err
		}

		for key, data := range items {
			refs, err := refsOf(k, key, data)
			if err != nil {
				return nil, err
			}

			for _, name := range refs {
				names[name] = struct{}{}
			}
		}
	}

	return names, nil
}

func refsOf(k kind.ID, key string, data []byte) ([]string, error) {
	switch k {
	case kind.MCP:
		return secret.RefsJSON(data)
	case kind.Memory:
		return secret.RefsText(data), nil
	case kind.Projects:
		if !strings.HasSuffix(projectRelOf(key), projectJSONSuffix) {
			return nil, nil
		}

		return secret.RefsJSON(data)
	default:
		return nil, nil
	}
}

const projectJSONSuffix = ".json"

var (
	projectEnvValue = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)
	projectEnvRef   = regexp.MustCompile(`^\{env:([A-Za-z_][A-Za-z0-9_]*)\}$`)
)

func projectRelOf(key string) string {
	_, rel, _ := strings.Cut(key, "/")

	return rel
}

func (e *Engine) inboundProjects(items kind.Items) (kind.Items, bool, error) {
	out := make(kind.Items, len(items))
	changed := false

	for key, data := range items {
		if !strings.HasSuffix(projectRelOf(key), projectJSONSuffix) {
			out[key] = data

			continue
		}

		prepared, prefixed := rewriteProjectStrings(data, func(value, _ string) string {
			if name, ok := parseProjectEnvValue(value); ok {
				return "{env:" + name + "}"
			}

			return value
		})

		extracted, replaced, err := secret.ExtractJSON(prepared, e.secrets)
		if err != nil {
			return nil, false, fmt.Errorf("project %s: %w", key, err)
		}

		converted, envChanged := rewriteProjectStrings(extracted, func(value, _ string) string {
			if match := projectEnvRef.FindStringSubmatch(value); match != nil {
				return "${" + match[1] + "}"
			}

			return value
		})

		changed = changed || prefixed || replaced || envChanged
		out[key] = converted
	}

	return out, changed, nil
}

func parseProjectEnvValue(value string) (string, bool) {
	match := projectEnvValue.FindStringSubmatch(value)
	if match == nil {
		return "", false
	}

	return match[1], true
}

func (e *Engine) outboundProjects(items kind.Items) (kind.Items, []string, error) {
	out := make(kind.Items, len(items))
	missing := map[string]struct{}{}

	policy, err := e.projectPolicy()
	if err != nil {
		return nil, nil, err
	}

	for key, data := range items {
		rel := projectRelOf(key)

		if !strings.HasSuffix(rel, projectJSONSuffix) {
			out[key] = data

			continue
		}

		publishable, _ := e.projectPublishable(rel)
		materialize := policy.AllowsSecrets(rel) || publishable

		resolved, absent, err := e.resolveProjectJSON(data, materialize, policy)
		if err != nil {
			return nil, nil, fmt.Errorf("project %s: %w", key, err)
		}

		for _, name := range absent {
			missing[name] = struct{}{}
		}

		out[key] = resolved
	}

	return out, slices.Sorted(maps.Keys(missing)), nil
}

func (e *Engine) resolveProjectJSON(data []byte, materialize bool, policy proj.Policy) ([]byte, []string, error) {
	missing := map[string]struct{}{}

	resolve := func(value, server string) string {
		name, ok := secret.ParseRef(value)
		if !ok {
			return value
		}

		if !materialize || (server != "" && !policy.ServerAllowed(server)) {
			return "${" + name + "}"
		}

		stored, ok := e.secrets.Get(name)
		if !ok {
			missing[name] = struct{}{}

			return value
		}

		return stored
	}

	out, changed := rewriteProjectStrings(data, resolve)
	if !changed {
		return data, nil, nil
	}

	return out, slices.Sorted(maps.Keys(missing)), nil
}

func rewriteProjectStrings(data []byte, resolve func(value, server string) string) ([]byte, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	var tree any

	if err := decoder.Decode(&tree); err != nil {
		return data, false
	}

	changed := false
	out := rewriteProjectTree(tree, "", func(value, server string) string {
		updated := resolve(value, server)
		if updated != value {
			changed = true
		}

		return updated
	})

	if !changed {
		return data, false
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		return data, false
	}

	return encoded, true
}

func rewriteProjectTree(node any, server string, resolve func(value, server string) string) any {
	switch typed := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))

		for key, child := range typed {
			if server == "" && key == "mcpServers" {
				if servers, ok := child.(map[string]any); ok {
					out[key] = rewriteServers(servers, resolve)

					continue
				}
			}

			out[key] = rewriteProjectTree(child, server, resolve)
		}

		return out
	case []any:
		out := make([]any, len(typed))

		for i, item := range typed {
			out[i] = rewriteProjectTree(item, server, resolve)
		}

		return out
	case string:
		return resolve(typed, server)
	default:
		return node
	}
}

func rewriteServers(servers map[string]any, resolve func(value, server string) string) map[string]any {
	out := make(map[string]any, len(servers))

	for name, entry := range servers {
		out[name] = rewriteProjectTree(entry, name, resolve)
	}

	return out
}
