package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"

	"github.com/odiumuniverse/agents-sync/pkg/adapter"
	"github.com/odiumuniverse/agents-sync/pkg/cas"
	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/mcp"
	"github.com/odiumuniverse/agents-sync/pkg/merge"
	"github.com/odiumuniverse/agents-sync/pkg/registry"
	"github.com/odiumuniverse/agents-sync/pkg/skill"
)

type frozenResources struct {
	rules       bool
	mcp         bool
	skills      bool
	permissions bool
}

func (e *Engine) merge(state *canon, snapshots map[string]adapter.Snapshot, report *Report) (bool, error) {
	base, err := e.loadBase()
	if err != nil {
		return false, err
	}

	frozen := frozenResources{
		rules:       e.resourceConflicted(ResourceRules),
		mcp:         e.resourceConflicted(ResourceMCP),
		skills:      e.resourceConflicted(ResourceSkills),
		permissions: e.resourceConflicted(ResourcePermissions),
	}

	changed := canonChanges{skills: map[string]struct{}{}}

	for _, agentID := range e.orderedAgents(snapshots) {
		if err := e.mergeAgent(base, state, agentID, snapshots[agentID], report, &changed, frozen); err != nil {
			return false, err
		}
	}

	if err := e.persistCanon(state, changed); err != nil {
		return false, err
	}

	report.Rules.Changed = changed.rules
	report.MCP.Changed = changed.servers
	report.Skills.Changed = len(changed.skills) > 0
	report.Permissions.Changed = changed.permissions

	if !frozen.rules {
		e.ensureEntry(ResourceRules).Conflict = report.Rules.ConflictCount() > 0 || hasMarkers(state.rules)
	}

	if !frozen.mcp {
		e.ensureEntry(ResourceMCP).Conflict = report.MCP.ConflictCount() > 0
	}

	if !frozen.skills {
		e.ensureEntry(ResourceSkills).Conflict = report.Skills.ConflictCount() > 0
	}

	if !frozen.permissions {
		e.ensureEntry(ResourcePermissions).Conflict = report.Permissions.ConflictCount() > 0
	}

	return true, nil
}

func (e *Engine) mergeAgent(base, state *canon, agentID string, snap adapter.Snapshot, report *Report, changed *canonChanges, frozen frozenResources) error {
	if err := e.mergeAgentRules(base, state, agentID, snap, report, changed, frozen); err != nil {
		return err
	}

	if err := e.mergeAgentMCP(base, state, agentID, snap, report, changed, frozen); err != nil {
		return err
	}

	if err := e.mergeAgentSkills(base, state, agentID, snap, report, changed, frozen); err != nil {
		return err
	}

	return e.mergeAgentPermissions(base, state, agentID, snap, report, changed, frozen)
}

func (e *Engine) mergeAgentRules(base, state *canon, agentID string, snap adapter.Snapshot, report *Report, changed *canonChanges, frozen frozenResources) error {
	if frozen.rules || !e.participatesRules(agentID, snap) {
		return nil
	}

	return e.mergeRules(base, state, agentID, snap, report, changed)
}

func (e *Engine) mergeAgentMCP(base, state *canon, agentID string, snap adapter.Snapshot, report *Report, changed *canonChanges, frozen frozenResources) error {
	if frozen.mcp || !snap.MCPPresent {
		return nil
	}

	return e.mergeMCP(base, state, agentID, snap, report, changed)
}

func (e *Engine) mergeAgentSkills(base, state *canon, agentID string, snap adapter.Snapshot, report *Report, changed *canonChanges, frozen frozenResources) error {
	if frozen.skills || snap.Skills == nil || e.skillMode(agentID) == config.SkillsOff {
		return nil
	}

	return e.mergeSkills(base, state, agentID, snap, report, changed)
}

func (e *Engine) mergeAgentPermissions(base, state *canon, agentID string, snap adapter.Snapshot, report *Report, changed *canonChanges, frozen frozenResources) error {
	if frozen.permissions || !e.permissionsEnabled() || !snap.PermissionsPresent {
		return nil
	}

	return e.mergePermissions(base, state, agentID, snap, report, changed)
}

func (e *Engine) mergeRules(base, state *canon, agentID string, snap adapter.Snapshot, report *Report, changed *canonChanges) error {
	result := merge.Text(base.rules, state.rules, snap.Rules, merge.TextOptions{AgentLabel: "agent:" + agentID})

	if !bytes.Equal(result.Merged, state.rules) {
		changed.rules = true
	}

	state.rules = result.Merged

	if result.Conflicts == 0 {
		return e.removeConflictArtifacts(ResourceRules, agentID)
	}

	report.Rules.addConflict(agentID, result.Conflicts)

	return e.writeRulesConflict(agentID, result.Conflicts)
}

func (e *Engine) mergeMCP(base, state *canon, agentID string, snap adapter.Snapshot, report *Report, changed *canonChanges) error {
	mergedAny, conflicts := merge.JSON(toAny(base.servers), toAny(state.servers), toAny(snap.MCP))
	mergedServers := fromAny(mergedAny)

	if !serversEqual(mergedServers, state.servers) {
		changed.servers = true
	}

	state.servers = mergedServers

	if len(conflicts) == 0 {
		return e.removeConflictArtifacts(ResourceMCP, agentID)
	}

	report.MCP.addConflict(agentID, len(conflicts))

	return e.writeMCPConflict(agentID, conflicts)
}

func (e *Engine) mergeSkills(base, state *canon, agentID string, snap adapter.Snapshot, report *Report, changed *canonChanges) error {
	conflicts := make([]skill.Conflict, 0)

	for name, agentTree := range snap.Skills {
		result := skill.Tree3(name, base.skills[name], state.skills[name], agentTree)

		if !treesEqual(result.Tree, state.skills[name]) {
			changed.skills[name] = struct{}{}
		}

		state.skills[name] = result.Tree

		conflicts = append(conflicts, result.Conflicts...)
	}

	if len(conflicts) == 0 {
		return e.removeConflictArtifacts(ResourceSkills, agentID)
	}

	report.Skills.addConflict(agentID, len(conflicts))

	return e.writeSkillsConflict(agentID, snap.Skills, conflicts)
}

func (e *Engine) push(ctx context.Context, state *canon, snapshots map[string]adapter.Snapshot, report *Report) error {
	var errs []error

	for _, agentID := range e.orderedAgents(snapshots) {
		if err := e.pushAgent(ctx, state, agentID, snapshots[agentID], report); err != nil {
			errs = append(errs, err)
		}

		if e.prune {
			if err := e.pruneAgentSkills(agentID, state); err != nil {
				errs = append(errs, err)
			}
		}
	}

	return errors.Join(errs...)
}

func (e *Engine) pushAgent(ctx context.Context, state *canon, agentID string, snap adapter.Snapshot, report *Report) error {
	update, err := e.agentUpdate(agentID, state, snap, report)
	if err != nil {
		return err
	}

	if emptyUpdate(update) {
		report.Actions[agentID] = AgentActions{Rules: ActionNoop, MCP: ActionNoop}

		return nil
	}

	applyErr := e.adapterByID(agentID).Apply(ctx, update)

	switch {
	case applyErr == nil:
		report.Actions[agentID] = setAction(AgentActions{}, update, ActionPushed)
	case errors.Is(applyErr, adapter.ErrNotConfigured):
		e.log.Print(ctx, "agent config not found, skipping", "agent", agentID, "err", applyErr)

		report.Actions[agentID] = setAction(AgentActions{}, update, ActionSkipped)
	default:
		report.Actions[agentID] = setAction(AgentActions{}, update, ActionError)

		return fmt.Errorf("apply %s: %w", agentID, applyErr)
	}

	return nil
}

func (e *Engine) agentUpdate(agentID string, state *canon, snap adapter.Snapshot, report *Report) (adapter.Update, error) {
	update := adapter.Update{}

	if e.shouldPushRules(agentID, state, snap, report) {
		update.Rules = state.rules
	}

	if e.shouldPushMCP(agentID, state, snap, report) {
		update.MCP = state.servers
	}

	if skills := e.skillsToPush(agentID, state, snap, report); skills != nil {
		update.Skills = skills
	}

	rules, overrides, pushPermissions, err := e.permissionsToPush(agentID, state, snap, report)
	if err != nil {
		return adapter.Update{}, fmt.Errorf("prepare permissions for %s: %w", agentID, err)
	}

	if pushPermissions {
		update.Permissions = rules
		update.Overrides = overrides
	}

	return update, nil
}

func emptyUpdate(update adapter.Update) bool {
	return update.Rules == nil && update.MCP == nil && update.Skills == nil && update.Permissions == nil
}

func (e *Engine) syncSharedSkills(state *canon, report *Report) error {
	if e.sharedSkillsDir == "" {
		return nil
	}

	if e.resourceConflicted(ResourceSkills) {
		report.Actions[sharedSkillsAgent] = AgentActions{Skills: ActionSkipped}

		return nil
	}

	current, err := skill.ReadDir(e.sharedSkillsDir)
	if err != nil {
		return err
	}

	written := 0

	for name, tree := range state.skills {
		if treesEqual(current[name], tree) {
			continue
		}

		if err := skill.SyncTree(e.sharedSkillsDir, name, tree); err != nil {
			return fmt.Errorf("write shared skill %s: %w", name, err)
		}

		written++
	}

	if written == 0 {
		report.Actions[sharedSkillsAgent] = AgentActions{Skills: ActionNoop}

		return nil
	}

	report.Actions[sharedSkillsAgent] = AgentActions{Skills: ActionPushed}

	return nil
}

func (e *Engine) shouldPushRules(agentID string, state *canon, snap adapter.Snapshot, report *Report) bool {
	a := e.adapterByID(agentID)

	if a == nil || !a.RulesPush() {
		return false
	}

	if state.rules == nil || report.Rules.conflictedAgent(agentID) || e.resourceConflicted(ResourceRules) {
		return false
	}

	return !bytes.Equal(snap.Rules, state.rules)
}

func (e *Engine) shouldPushMCP(agentID string, state *canon, snap adapter.Snapshot, report *Report) bool {
	if !snap.MCPPresent || report.MCP.conflictedAgent(agentID) || e.resourceConflicted(ResourceMCP) {
		return false
	}

	return !serversEqual(snap.MCP, state.servers)
}

func (e *Engine) skillsToPush(agentID string, state *canon, snap adapter.Snapshot, report *Report) map[string]skill.Tree {
	a := e.adapterByID(agentID)

	if a == nil || !a.SkillsPush() || e.skillMode(agentID) != config.SkillsSync || len(state.skills) == 0 {
		return nil
	}

	if report.Skills.conflictedAgent(agentID) || e.resourceConflicted(ResourceSkills) {
		return nil
	}

	diff := map[string]skill.Tree{}

	for name, tree := range state.skills {
		if !treesEqual(snap.Skills[name], tree) {
			diff[name] = tree
		}
	}

	if len(diff) == 0 {
		return nil
	}

	return diff
}

func (e *Engine) updateRegistry(state *canon, snapshots map[string]adapter.Snapshot, report *Report, merged bool) error {
	if err := e.updateRulesRegistry(state, snapshots, report, merged); err != nil {
		return err
	}

	if err := e.updateMCPRegistry(state, snapshots, report, merged); err != nil {
		return err
	}

	if err := e.updateSkillsRegistry(state, snapshots, report, merged); err != nil {
		return err
	}

	return e.updatePermissionsRegistry(state, snapshots, report, merged)
}

func (e *Engine) updateRulesRegistry(state *canon, snapshots map[string]adapter.Snapshot, report *Report, merged bool) error {
	rulesHash := cas.HashOf(state.rules)

	entry := e.ensureEntry(ResourceRules)
	entry.Vault = rulesHash

	if merged {
		if _, err := e.store.Put(state.rules); err != nil {
			return err
		}

		entry.Base = rulesHash
	}

	for _, agentID := range e.orderedAgents(snapshots) {
		snap := snapshots[agentID]
		actions := report.Actions[agentID]

		if snap.Rules != nil || actions.Rules == ActionPushed {
			entry.Agents[agentID] = cas.HashOf(contentAfterPush(snap.Rules, state.rules, actions.Rules))
		}
	}

	return nil
}

func (e *Engine) updateMCPRegistry(state *canon, snapshots map[string]adapter.Snapshot, report *Report, merged bool) error {
	serversData, err := state.servers.MarshalCanonical()
	if err != nil {
		return err
	}

	serversHash := cas.HashOf(serversData)

	entry := e.ensureEntry(ResourceMCP)
	entry.Vault = serversHash

	if merged {
		if _, err := e.store.Put(serversData); err != nil {
			return err
		}

		entry.Base = serversHash
	}

	for _, agentID := range e.orderedAgents(snapshots) {
		snap := snapshots[agentID]
		actions := report.Actions[agentID]

		if !snap.MCPPresent && actions.MCP != ActionPushed {
			continue
		}

		servers := snap.MCP
		if actions.MCP == ActionPushed || actions.MCP == ActionNoop {
			servers = state.servers
		}

		data, err := servers.MarshalCanonical()
		if err != nil {
			return err
		}

		entry.Agents[agentID] = cas.HashOf(data)
	}

	return nil
}

func (e *Engine) updateSkillsRegistry(state *canon, snapshots map[string]adapter.Snapshot, report *Report, merged bool) error {
	data, err := skillsManifest(state.skills)
	if err != nil {
		return err
	}

	if merged {
		for _, tree := range state.skills {
			for _, content := range tree {
				if _, err := e.store.Put(content); err != nil {
					return err
				}
			}
		}
	}

	hashes := map[string]cas.Hash{}

	for _, agentID := range e.orderedAgents(snapshots) {
		snap := snapshots[agentID]
		actions := report.Actions[agentID]

		if snap.Skills == nil && actions.Skills != ActionPushed {
			continue
		}

		agentSkills := snap.Skills
		if actions.Skills == ActionPushed || actions.Skills == ActionNoop {
			agentSkills = state.skills
		}

		agentData, err := skillsManifest(agentSkills)
		if err != nil {
			return err
		}

		hashes[agentID] = cas.HashOf(agentData)
	}

	return e.updateResourceRegistry(ResourceSkills, data, hashes, merged)
}

func (e *Engine) updateResourceRegistry(resource string, data []byte, agentHashes map[string]cas.Hash, merged bool) error {
	hash := cas.HashOf(data)

	entry := e.ensureEntry(resource)
	entry.Vault = hash

	if merged {
		if _, err := e.store.Put(data); err != nil {
			return err
		}

		entry.Base = hash
	}

	maps.Copy(entry.Agents, agentHashes)

	return nil
}

func skillsManifest(skills map[string]skill.Tree) ([]byte, error) {
	manifests := make(map[string]skill.Manifest, len(skills))

	for name, tree := range skills {
		manifests[name] = skill.ManifestOf(tree)
	}

	data, err := json.Marshal(manifests)
	if err != nil {
		return nil, fmt.Errorf("encode skills manifest: %w", err)
	}

	return append(data, '\n'), nil
}

func contentAfterPush(snapshot, canonContent []byte, action string) []byte {
	if action == ActionPushed || action == ActionNoop {
		return canonContent
	}

	return snapshot
}

func setAction(actions AgentActions, update adapter.Update, action string) AgentActions {
	if update.Rules != nil {
		actions.Rules = action
	}

	if update.MCP != nil {
		actions.MCP = action
	}

	if update.Skills != nil {
		actions.Skills = action
	}

	if update.Permissions != nil {
		actions.Permissions = action
	}

	return actions
}

func (e *Engine) participatesRules(agentID string, snap adapter.Snapshot) bool {
	if snap.Rules != nil {
		return true
	}

	entry := e.state.Resources[ResourceRules]

	return entry != nil && entry.Agents[agentID] != ""
}

func (e *Engine) ensureEntry(resource string) *registry.Entry {
	if e.state.Resources == nil {
		e.state.Resources = map[string]*registry.Entry{}
	}

	entry := e.state.Resources[resource]
	if entry == nil {
		entry = &registry.Entry{Agents: map[string]cas.Hash{}}
		e.state.Resources[resource] = entry
	}

	if entry.Agents == nil {
		entry.Agents = map[string]cas.Hash{}
	}

	return entry
}

func (e *Engine) resourceConflicted(resource string) bool {
	entry := e.state.Resources[resource]

	return entry != nil && entry.Conflict
}

func toAny(servers mcp.Servers) map[string]any {
	if len(servers) == 0 {
		return map[string]any{}
	}

	data, err := json.Marshal(servers)
	if err != nil {
		return map[string]any{}
	}

	out := map[string]any{}
	if err := json.Unmarshal(data, &out); err != nil {
		return map[string]any{}
	}

	return out
}

func fromAny(value any) mcp.Servers {
	obj, ok := value.(map[string]any)
	if !ok {
		return mcp.Servers{}
	}

	data, err := json.Marshal(obj)
	if err != nil {
		return mcp.Servers{}
	}

	servers := mcp.Servers{}
	if err := json.Unmarshal(data, &servers); err != nil {
		return mcp.Servers{}
	}

	return servers
}

func serversEqual(a, b mcp.Servers) bool {
	return reflect.DeepEqual(a, b)
}

func treesEqual(a, b skill.Tree) bool {
	return maps.EqualFunc(a, b, bytes.Equal)
}

func skillMapsEqual(a, b map[string]skill.Tree) bool {
	if len(a) != len(b) {
		return false
	}

	for name, tree := range a {
		if !treesEqual(tree, b[name]) {
			return false
		}
	}

	return true
}
