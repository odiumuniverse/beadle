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
	"github.com/odiumuniverse/agents-sync/pkg/secret"
	"github.com/odiumuniverse/agents-sync/pkg/skill"
)

type frozenResources struct {
	rules       bool
	permissions bool
}

func (e *Engine) merge(base, state *canon, snapshots map[string]adapter.Snapshot, report *Report) (bool, error) {
	frozen := frozenResources{
		rules:       e.resourceConflicted(ResourceRules),
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

	e.ensureEntry(ResourceMCP).Conflict = report.MCP.ConflictCount() > 0

	e.ensureEntry(ResourceSkills).Conflict = report.Skills.ConflictCount() > 0

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

func (e *Engine) mergeAgentMCP(base, state *canon, agentID string, snap adapter.Snapshot, report *Report, changed *canonChanges, _ frozenResources) error {
	if !snap.MCPPresent {
		return nil
	}

	return e.mergeMCP(base, state, agentID, snap, report, changed)
}

func (e *Engine) mergeAgentSkills(base, state *canon, agentID string, snap adapter.Snapshot, report *Report, changed *canonChanges, _ frozenResources) error {
	if snap.Skills == nil || e.skillMode(agentID) == config.SkillsOff {
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
	conflictedServers, err := e.conflictedMCPServerNames()
	if err != nil {
		return err
	}

	conflictedSkills, err := e.conflictedSkillNames()
	if err != nil {
		return err
	}

	var errs []error

	for _, agentID := range e.orderedAgents(snapshots) {
		if err := e.pushAgent(ctx, state, agentID, snapshots[agentID], report, conflictedServers, conflictedSkills); err != nil {
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

func (e *Engine) pushAgent(
	ctx context.Context, state *canon, agentID string, snap adapter.Snapshot, report *Report, conflictedServers, conflictedSkills map[string]struct{},
) error {
	update, err := e.agentUpdate(agentID, state, snap, report, conflictedServers, conflictedSkills)
	if err != nil {
		return err
	}

	actions, applyErr := e.applyUpdate(ctx, agentID, update)

	if len(report.MissingSecrets[agentID]) > 0 {
		actions.MCP = ActionSkipped
	}

	report.Actions[agentID] = actions

	if applyErr != nil {
		return fmt.Errorf("apply %s: %w", agentID, applyErr)
	}

	return nil
}

func (e *Engine) applyUpdate(ctx context.Context, agentID string, update adapter.Update) (AgentActions, error) {
	if emptyUpdate(update) {
		return AgentActions{Rules: ActionNoop, MCP: ActionNoop}, nil
	}

	err := e.adapterByID(agentID).Apply(ctx, update)

	switch {
	case err == nil:
		return setAction(AgentActions{}, update, ActionPushed), nil
	case errors.Is(err, adapter.ErrNotConfigured):
		e.log.Print(ctx, "agent config not found, skipping", "agent", agentID, "err", err)

		return setAction(AgentActions{}, update, ActionSkipped), nil
	default:
		return setAction(AgentActions{}, update, ActionError), err
	}
}

func (e *Engine) agentUpdate(
	agentID string, state *canon, snap adapter.Snapshot, report *Report, conflictedServers, conflictedSkills map[string]struct{},
) (adapter.Update, error) {
	update := adapter.Update{}

	if e.shouldPushRules(agentID, state, snap, report) {
		update.Rules = state.rules
	}

	payload := mcpPushPayload(state.servers, snap.MCP, conflictedServers)

	if shouldPushMCP(snap, payload) {
		servers, missing, err := secret.Resolve(payload, e.secrets, e.config.SecretsMode())
		if err != nil {
			return adapter.Update{}, fmt.Errorf("resolve secrets for %s: %w", agentID, err)
		}

		if len(missing) > 0 {
			report.addMissingSecrets(agentID, missing)
		} else {
			update.MCP = servers
		}
	}

	if skills := e.skillsToPush(agentID, state, snap, conflictedSkills); skills != nil {
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

	conflicted, err := e.conflictedSkillNames()
	if err != nil {
		return err
	}

	current, err := skill.ReadDir(e.sharedSkillsDir)
	if err != nil {
		return err
	}

	written := 0
	skipped := false

	for name, tree := range state.skills {
		if _, ok := conflicted[name]; ok {
			skipped = true

			continue
		}

		if treesEqual(current[name], tree) {
			continue
		}

		if err := skill.SyncTree(e.sharedSkillsDir, name, tree); err != nil {
			return fmt.Errorf("write shared skill %s: %w", name, err)
		}

		written++
	}

	switch {
	case written > 0:
		report.Actions[sharedSkillsAgent] = AgentActions{Skills: ActionPushed}
	case skipped:
		report.Actions[sharedSkillsAgent] = AgentActions{Skills: ActionSkipped}
	default:
		report.Actions[sharedSkillsAgent] = AgentActions{Skills: ActionNoop}
	}

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

func shouldPushMCP(snap adapter.Snapshot, payload mcp.Servers) bool {
	if !snap.MCPPresent {
		return false
	}

	return !serversEqual(snap.MCP, payload)
}

func mcpPushPayload(canonServers, agentServers mcp.Servers, conflictedServers map[string]struct{}) mcp.Servers {
	if len(conflictedServers) == 0 {
		return canonServers
	}

	payload := maps.Clone(canonServers)
	if payload == nil {
		payload = mcp.Servers{}
	}

	for name := range conflictedServers {
		if current, ok := agentServers[name]; ok {
			payload[name] = current
		} else {
			delete(payload, name)
		}
	}

	return payload
}

func (e *Engine) skillsToPush(agentID string, state *canon, snap adapter.Snapshot, conflictedNames map[string]struct{}) map[string]skill.Tree {
	a := e.adapterByID(agentID)

	if a == nil || !a.SkillsPush() || e.skillMode(agentID) != config.SkillsSync || len(state.skills) == 0 {
		return nil
	}

	diff := map[string]skill.Tree{}

	for name, tree := range state.skills {
		if _, ok := conflictedNames[name]; ok {
			continue
		}

		if !treesEqual(snap.Skills[name], tree) {
			diff[name] = tree
		}
	}

	if len(diff) == 0 {
		return nil
	}

	return diff
}

func (e *Engine) updateRegistry(base, state *canon, snapshots map[string]adapter.Snapshot, report *Report, merged bool) error {
	if err := e.updateRulesRegistry(state, snapshots, report, merged); err != nil {
		return err
	}

	if err := e.updateMCPRegistry(base, state, snapshots, report, merged); err != nil {
		return err
	}

	if err := e.updateSkillsRegistry(base, state, snapshots, report, merged); err != nil {
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

func (e *Engine) updateMCPRegistry(base, state *canon, snapshots map[string]adapter.Snapshot, report *Report, merged bool) error {
	serversData, err := state.servers.MarshalCanonical()
	if err != nil {
		return err
	}

	entry := e.ensureEntry(ResourceMCP)
	entry.Vault = cas.HashOf(serversData)

	conflicted, err := e.conflictedMCPServerNames()
	if err != nil {
		return err
	}

	if merged {
		baseData, err := pinConflictedServers(state.servers, base.servers, conflicted).MarshalCanonical()
		if err != nil {
			return err
		}

		if _, err := e.store.Put(baseData); err != nil {
			return err
		}

		entry.Base = cas.HashOf(baseData)
	}

	for _, agentID := range e.orderedAgents(snapshots) {
		snap := snapshots[agentID]
		actions := report.Actions[agentID]

		if !snap.MCPPresent && actions.MCP != ActionPushed {
			continue
		}

		servers := snap.MCP
		if actions.MCP == ActionPushed || actions.MCP == ActionNoop {
			servers = mcpPushPayload(state.servers, snap.MCP, conflicted)
		}

		data, err := servers.MarshalCanonical()
		if err != nil {
			return err
		}

		entry.Agents[agentID] = cas.HashOf(data)
	}

	return nil
}

func (e *Engine) updateSkillsRegistry(base, state *canon, snapshots map[string]adapter.Snapshot, report *Report, merged bool) error {
	data, err := skillsManifest(state.skills)
	if err != nil {
		return err
	}

	entry := e.ensureEntry(ResourceSkills)
	entry.Vault = cas.HashOf(data)

	conflicted, err := e.conflictedSkillNames()
	if err != nil {
		return err
	}

	if merged {
		if err := e.storeSkillTrees(state.skills); err != nil {
			return err
		}

		if _, err := e.store.Put(data); err != nil {
			return err
		}

		baseHash, err := e.storePinnedSkillsBase(base, state, conflicted)
		if err != nil {
			return err
		}

		entry.Base = baseHash
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
			agentSkills = skillsPushPayload(state.skills, snap.Skills, conflicted)
		}

		agentData, err := skillsManifest(agentSkills)
		if err != nil {
			return err
		}

		hashes[agentID] = cas.HashOf(agentData)
	}

	maps.Copy(entry.Agents, hashes)

	return nil
}

func (e *Engine) storeSkillTrees(skills map[string]skill.Tree) error {
	for _, tree := range skills {
		for _, content := range tree {
			if _, err := e.store.Put(content); err != nil {
				return err
			}
		}
	}

	return nil
}

func (e *Engine) storePinnedSkillsBase(base, state *canon, conflicted map[string]struct{}) (cas.Hash, error) {
	pinned := pinConflictedSkills(state.skills, base.skills, conflicted)

	if err := e.storeSkillTrees(pinned); err != nil {
		return "", err
	}

	pinnedData, err := skillsManifest(pinned)
	if err != nil {
		return "", err
	}

	if _, err := e.store.Put(pinnedData); err != nil {
		return "", err
	}

	return cas.HashOf(pinnedData), nil
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

func pinConflictedServers(canonServers, oldBase mcp.Servers, conflictedServers map[string]struct{}) mcp.Servers {
	if len(conflictedServers) == 0 {
		return canonServers
	}

	pinned := maps.Clone(canonServers)
	if pinned == nil {
		pinned = mcp.Servers{}
	}

	for name := range conflictedServers {
		if original, ok := oldBase[name]; ok {
			pinned[name] = original
		} else {
			delete(pinned, name)
		}
	}

	return pinned
}

func pinConflictedSkills(canonSkills, oldBase map[string]skill.Tree, conflictedNames map[string]struct{}) map[string]skill.Tree {
	if len(conflictedNames) == 0 {
		return canonSkills
	}

	pinned := maps.Clone(canonSkills)
	if pinned == nil {
		pinned = map[string]skill.Tree{}
	}

	for name := range conflictedNames {
		if original, ok := oldBase[name]; ok {
			pinned[name] = original
		} else {
			delete(pinned, name)
		}
	}

	return pinned
}

func skillsPushPayload(canonSkills, agentSkills map[string]skill.Tree, conflictedNames map[string]struct{}) map[string]skill.Tree {
	if len(conflictedNames) == 0 {
		return canonSkills
	}

	payload := maps.Clone(canonSkills)
	if payload == nil {
		payload = map[string]skill.Tree{}
	}

	for name := range conflictedNames {
		if current, ok := agentSkills[name]; ok {
			payload[name] = current
		} else {
			delete(payload, name)
		}
	}

	return payload
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
