package engine

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/odiumuniverse/agents-sync/pkg/cas"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
	"github.com/odiumuniverse/agents-sync/pkg/secret"
	"github.com/odiumuniverse/agents-sync/pkg/state"
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

			refs, err := refsOf(c.Kind, data)
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

	for _, k := range []kind.ID{kind.MCP, kind.Memory} {
		items, _, err := e.loadVault(k)
		if err != nil {
			return nil, err
		}

		for _, data := range slices.Collect(maps.Values(items)) {
			refs, err := refsOf(k, data)
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

func refsOf(k kind.ID, data []byte) ([]string, error) {
	switch k {
	case kind.MCP:
		return secret.RefsJSON(data)
	case kind.Memory:
		return secret.RefsText(data), nil
	default:
		return nil, nil
	}
}
