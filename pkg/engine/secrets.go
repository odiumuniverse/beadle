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
	if k != kind.MCP || len(items) == 0 {
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
	if k != kind.MCP || len(items) == 0 {
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
	items, _, err := e.loadVault(kind.MCP)
	if err != nil {
		return nil, err
	}

	documents := slices.Collect(maps.Values(items))

	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		return nil, err
	}

	for _, c := range st.OpenConflicts() {
		if c.Kind != kind.MCP {
			continue
		}

		for _, hash := range []cas.Hash{c.Base, c.Vault, c.Local} {
			data, err := e.blob(hash)
			if err != nil {
				return nil, err
			}

			if data != nil {
				documents = append(documents, data)
			}
		}
	}

	names := map[string]struct{}{}

	for _, data := range documents {
		refs, err := secret.RefsJSON(data)
		if err != nil {
			return nil, err
		}

		for _, name := range refs {
			names[name] = struct{}{}
		}
	}

	return slices.Sorted(maps.Keys(names)), nil
}
