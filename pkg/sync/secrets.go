package sync

import (
	"fmt"
	"slices"

	"github.com/odiumuniverse/agents-sync/pkg/secret"
)

func (e *Engine) Secrets() *secret.Store {
	return e.secrets
}

func (e *Engine) SecretsMode() string {
	return e.config.SecretsMode()
}

func (e *Engine) PruneSecrets() ([]string, error) {
	state, err := e.loadCanon()
	if err != nil {
		return nil, err
	}

	refs, err := secret.Refs(state.servers)
	if err != nil {
		return nil, err
	}

	removed := make([]string, 0)

	for _, name := range e.secrets.Names() {
		if slices.Contains(refs, name) {
			continue
		}

		if e.secrets.Delete(name) {
			removed = append(removed, name)
		}
	}

	if len(removed) == 0 {
		return removed, nil
	}

	if err := e.secrets.Save(); err != nil {
		return nil, err
	}

	return removed, nil
}

func (e *Engine) checkSecrets(state *canon) []Issue {
	refs, err := secret.Refs(state.servers)
	if err != nil {
		return []Issue{{
			Severity: SeverityError,
			Resource: ResourceMCP,
			Message:  "read secret references: " + err.Error(),
		}}
	}

	used := make(map[string]struct{}, len(refs))

	var issues []Issue

	for _, name := range refs {
		used[name] = struct{}{}

		if e.secrets.Has(name) {
			continue
		}

		issues = append(issues, Issue{
			Severity: SeverityError,
			Resource: ResourceMCP,
			Message: fmt.Sprintf(
				"secret %s has no value in %s; run agent-sync secrets set %s <value>",
				name, e.vault.SecretsPath(), name,
			),
		})
	}

	for _, name := range e.secrets.Names() {
		if _, ok := used[name]; ok {
			continue
		}

		issues = append(issues, Issue{
			Severity: SeverityInfo,
			Resource: ResourceMCP,
			Message:  fmt.Sprintf("secret %s is unused; run agent-sync secrets prune", name),
		})
	}

	return issues
}
