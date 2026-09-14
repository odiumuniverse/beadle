package sync

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/odiumuniverse/agents-sync/pkg/adapter"
	"github.com/odiumuniverse/agents-sync/pkg/cas"
	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
	"github.com/odiumuniverse/agents-sync/pkg/merge"
	"github.com/odiumuniverse/agents-sync/pkg/permission"
)

func (e *Engine) permissionsEnabled() bool {
	return e.config.PermissionsMode() == permission.ModeSync
}

func (e *Engine) permissionsPath() string {
	return filepath.Join(e.vault.Root(), "permissions", "rules.json")
}

func (e *Engine) overridePath(agentID string) string {
	return filepath.Join(e.vault.Root(), "permissions", "override", agentID+".json")
}

func (e *Engine) loadPermissions() (permission.Rules, error) {
	data, err := readOptionalFile(e.permissionsPath())
	if err != nil {
		return nil, err
	}

	return permission.Parse(data)
}

func (e *Engine) mergePermissions(base, state *canon, agentID string, snap adapter.Snapshot, report *Report, changed *canonChanges) error {
	a := e.adapterByID(agentID)
	if a == nil {
		return nil
	}

	kinds := a.PermissionKinds()

	baseSubset := filterRulesByKind(base.permissions, kinds)
	vaultSubset := filterRulesByKind(state.permissions, kinds)

	mergedAny, conflicts := merge.JSON(rulesToAny(baseSubset), rulesToAny(vaultSubset), rulesToAny(snap.Permissions))
	mergedSubset := rulesFromAny(mergedAny)

	newState := replaceRulesByKind(state.permissions, kinds, mergedSubset)

	if !rulesEqual(newState, state.permissions) {
		changed.permissions = true
	}

	state.permissions = newState

	if err := e.writeOverride(agentID, snap.PermissionsOverride); err != nil {
		return err
	}

	if len(conflicts) == 0 {
		return e.removeConflictArtifacts(ResourcePermissions, agentID)
	}

	report.Permissions.addConflict(agentID, len(conflicts))

	return e.writeJSONConflict(ResourcePermissions, agentID, conflicts)
}

func filterRulesByKind(rules permission.Rules, kinds []string) permission.Rules {
	out := make(permission.Rules, len(rules))

	for key, effect := range rules {
		kind, _, _, ok := permission.Split(key)
		if !ok || !slices.Contains(kinds, kind) {
			continue
		}

		out[key] = effect
	}

	return out
}

func replaceRulesByKind(current permission.Rules, kinds []string, replacement permission.Rules) permission.Rules {
	out := make(permission.Rules, len(current)+len(replacement))

	for key, effect := range current {
		kind, _, _, ok := permission.Split(key)
		if ok && slices.Contains(kinds, kind) {
			continue
		}

		out[key] = effect
	}

	maps.Copy(out, replacement)

	return out
}

func (e *Engine) permissionsToPush(agentID string, state *canon, snap adapter.Snapshot, report *Report) (permission.Rules, permission.Override, bool, error) {
	if !e.permissionsEnabled() || !snap.PermissionsPresent {
		return nil, permission.Override{}, false, nil
	}

	if report.Permissions.conflictedAgent(agentID) || e.resourceConflicted(ResourcePermissions) {
		return nil, permission.Override{}, false, nil
	}

	a := e.adapterByID(agentID)
	if a == nil {
		return nil, permission.Override{}, false, nil
	}

	if rulesEqual(snap.Permissions, filterRulesByKind(state.permissions, a.PermissionKinds())) {
		return nil, permission.Override{}, false, nil
	}

	override, err := e.readOverride(agentID)
	if err != nil {
		return nil, permission.Override{}, false, err
	}

	return state.permissions, override, true, nil
}

func (e *Engine) updatePermissionsRegistry(state *canon, snapshots map[string]adapter.Snapshot, report *Report, merged bool) error {
	data, err := state.permissions.Marshal()
	if err != nil {
		return err
	}

	hashes := map[string]cas.Hash{}

	for _, agentID := range e.orderedAgents(snapshots) {
		snap := snapshots[agentID]
		actions := report.Actions[agentID]

		if !snap.PermissionsPresent && actions.Permissions != ActionPushed {
			continue
		}

		rules := snap.Permissions
		if actions.Permissions == ActionPushed || actions.Permissions == ActionNoop {
			rules = filterRulesByKind(state.permissions, e.adapterByID(agentID).PermissionKinds())
		}

		agentData, err := rules.Marshal()
		if err != nil {
			return err
		}

		hashes[agentID] = cas.HashOf(agentData)
	}

	return e.updateResourceRegistry(ResourcePermissions, data, hashes, merged)
}

func (e *Engine) writeOverride(agentID string, override permission.Override) error {
	path := e.overridePath(agentID)

	if override.Empty() {
		err := os.Remove(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove permission override: %w", err)
		}

		return nil
	}

	data, err := override.Marshal()
	if err != nil {
		return err
	}

	if err := fsutil.WriteFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("write permission override: %w", err)
	}

	return nil
}

func (e *Engine) readOverride(agentID string) (permission.Override, error) {
	data, err := readOptionalFile(e.overridePath(agentID))
	if err != nil {
		return permission.Override{}, err
	}

	return permission.ParseOverride(data)
}

func (e *Engine) resolvePermissions(opts ResolveOptions) error {
	if !opts.KeepAgent {
		return e.clearConflictArtifacts(ResourcePermissions)
	}

	agent, err := e.resolveAgent(ResourcePermissions, opts.Agent)
	if err != nil {
		return err
	}

	conflicts, err := readJSONConflictFile(e.conflictPath(ResourcePermissions, agent))
	if err != nil {
		return err
	}

	rules, err := e.loadPermissions()
	if err != nil {
		return err
	}

	for _, conflict := range conflicts.Conflicts {
		applyFlatConflict(rules, conflict)
	}

	data, err := rules.Marshal()
	if err != nil {
		return err
	}

	if err := fsutil.WriteFileAtomic(e.permissionsPath(), data, 0o600); err != nil {
		return fmt.Errorf("write vault permissions: %w", err)
	}

	return e.removeConflictArtifacts(ResourcePermissions, agent)
}

func applyFlatConflict(rules permission.Rules, conflict merge.JSONConflict) {
	if conflict.Agent == nil {
		delete(rules, conflict.Path)

		return
	}

	if effect, ok := conflict.Agent.(string); ok {
		rules[conflict.Path] = effect
	}
}

func rulesToAny(rules permission.Rules) map[string]any {
	out := make(map[string]any, len(rules))

	for key, effect := range rules {
		out[key] = effect
	}

	return out
}

func rulesFromAny(value any) permission.Rules {
	obj, ok := value.(map[string]any)
	if !ok {
		return permission.Rules{}
	}

	rules := make(permission.Rules, len(obj))

	for key, effect := range obj {
		if str, ok := effect.(string); ok {
			rules[key] = str
		}
	}

	return rules
}

func rulesEqual(a, b permission.Rules) bool {
	if len(a) != len(b) {
		return false
	}

	for key, effect := range a {
		if b[key] != effect {
			return false
		}
	}

	return true
}

func permissionDiff(agent, vault permission.Rules) []RuleChange {
	changes := make([]RuleChange, 0)

	seen := map[string]struct{}{}

	for key := range agent {
		seen[key] = struct{}{}
	}

	for key := range vault {
		seen[key] = struct{}{}
	}

	for _, key := range sortedKeys(seen) {
		agentEffect, agentOK := agent[key]
		vaultEffect, vaultOK := vault[key]

		switch {
		case agentOK && !vaultOK:
			changes = append(changes, RuleChange{Key: key, Status: StatusAdded})
		case !agentOK && vaultOK:
			changes = append(changes, RuleChange{Key: key, Status: StatusRemoved})
		case agentEffect != vaultEffect:
			changes = append(changes, RuleChange{Key: key, Status: StatusChanged})
		}
	}

	return changes
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))

	for key := range set {
		keys = append(keys, key)
	}

	slices.Sort(keys)

	return keys
}
