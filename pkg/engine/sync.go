package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
	"github.com/odiumuniverse/beadle/pkg/rulings"
	"github.com/odiumuniverse/beadle/pkg/skill"
	"github.com/odiumuniverse/beadle/pkg/state"
)

type view struct {
	agent           *agent.Agent
	surface         agent.Surface
	mode            config.Mode
	snap            agent.Snapshot
	raw             kind.Items
	base            kind.Items
	presented       kind.Items
	presentedFailed bool
	// bundleManaged marks a view created for a host whose MCP mode is off
	// because its verified bundle manages the kind: the view leaves the canon
	// remnants alone, presents plugin-sourced items when presentsPlugins, and
	// delivers the complement (the canon servers the bundle cannot carry).
	bundleManaged   bool
	presentsPlugins bool
	// complement tracks the complement delivery of a bundle-managed view.
	complement  complementDelivery
	pluginOwned map[string]struct{}
	// visibility is the resolved skill copy map of the view's agent; it is
	// set for writing skill views only (В-14).
	visibility *visibility
	moved      int
	holds      map[string]bool
	conflicts  []state.Conflict
	blobs      [][]byte
}

func (v *view) frozen(spec kind.Spec, key string) bool {
	group := spec.Group(key)

	return v.holds[group] || v.snap.ReadOnly[group] != ""
}

func (e *Engine) syncKind(ctx context.Context, spec kind.Spec, agents []*agent.Agent, st *state.State, opts SyncOptions) KindReport {
	report := KindReport{Kind: spec.ID}

	vaultItems, dirty, err := e.loadVault(spec.ID)
	if err != nil {
		report.Err = err.Error()

		return report
	}

	e.noteMCPCanonIssues(spec, opts, vaultItems, &report)

	e.notePermissionCanonIssues(spec, opts, vaultItems, &report)

	if !e.prepareProjects(spec, &report) {
		return report
	}

	var (
		pluginPlan   pluginMCPPlan
		pluginLedger pluginLedger
	)

	if spec.ID == kind.MCP {
		pluginLedger, pluginPlan = e.loadPluginMCPPlan(vaultItems, &report)
	}

	views := e.readViews(ctx, spec, agents, st, opts, pluginPlan, &report)
	original := maps.Clone(vaultItems)

	e.logNoteSecretMoves(ctx, spec, views, opts)

	e.fanInInbox(ctx, spec, agents, vaultItems, &report, opts)

	owned := e.ownedPluginMCP(spec, views, pluginPlan, pluginLedger, vaultItems, &report)

	for _, v := range views {
		if v.mode.Pulls() {
			e.pull(spec, v, vaultItems, owned, &report)
		}
	}

	retired := e.cleanupInvalidSkills(spec, views, st, opts, &report)

	e.attachSkillVisibility(spec, views, st, opts, vaultItems, &report)

	report.VaultChanged = retired || !vaultItems.Equal(original)

	if !opts.DryRun {
		if report.VaultChanged || dirty {
			if err := e.saveVault(spec.ID, vaultItems); err != nil {
				report.Err = err.Error()

				return report
			}
		}

		if err := e.snapshot(st, spec.ID, vaultItems); err != nil {
			report.Err = err.Error()
		}
	}

	e.maybePersistPluginMCP(spec, pluginPlan, pluginLedger, opts, &report)

	e.pushGroups(ctx, spec, views, vaultItems, opts, st, &report)

	if spec.ID == kind.Projects {
		e.warnProjectGit(&report, vaultItems)
	}

	return report
}

// cleanupInvalidSkills retires beadle-owned canon entries without a root
// SKILL.md and prunes them from the writable file surfaces. A dry run and the
// pull direction leave the canon alone; foreign directories are never touched.
func (e *Engine) cleanupInvalidSkills(spec kind.Spec, views []*view, st *state.State, opts SyncOptions, report *KindReport) bool {
	if spec.ID != kind.Skills || opts.DryRun || opts.Direction == config.ModePull {
		return false
	}

	for _, v := range views {
		if v.mode.Pushes() {
			pruneInvalidSkills(v.surface.Path(), v.base, report)
		}
	}

	return e.retireInvalidSkills(st, report)
}

// attachSkillVisibility resolves the canon skills against the read area of
// every writing view once per sync, so pushView can release the writable
// copies another copy already delivers. Views without an independent read
// directory are skipped: a foreign copy can only live there (В-14). The
// warnings are reported for real runs only; doctor diagnoses dry runs.
func (e *Engine) attachSkillVisibility(spec kind.Spec, views []*view, st *state.State, opts SyncOptions, vaultItems kind.Items, report *KindReport) {
	if spec.ID != kind.Skills {
		return
	}

	canon := skill.Group(vaultItems)

	for _, v := range views {
		if !v.mode.Pushes() || len(e.foreignReadDirs(v.agent, v.surface)) == 0 {
			continue
		}

		vis := e.resolveSkillVisibility(v.agent, v.surface, st, canon)
		v.visibility = &vis

		if !opts.DryRun {
			report.Warnings = append(report.Warnings, vis.Warnings...)
		}
	}
}

// complementSkills drops the canon skills a foreign copy already delivers
// when the local beadle copy is unmodified: the foreign copy is the single
// winner for the host. Only base-owned copies leave, and only through the
// ordinary surface write, so read-only entries keep their protection.
func (e *Engine) complementSkills(spec kind.Spec, v *view, desired kind.Items, report *KindReport) kind.Items {
	if spec.ID != kind.Skills || v.visibility == nil {
		return desired
	}

	covered := v.visibility.foreignCoverage()
	if len(covered) == 0 {
		return desired
	}

	out := desired

	for _, name := range slices.Sorted(maps.Keys(covered)) {
		if v.holds[name] || v.snap.ReadOnly[name] != "" {
			continue
		}

		files := groupFiles(v.snap.Items, name)
		if len(files) > 0 {
			if !groupMatchesBase(v.snap.Items, v.base, name, files) {
				continue
			}

			for _, key := range files {
				delete(out, key)
			}

			report.Warnings = append(report.Warnings, fmt.Sprintf("skills: %s is also delivered by %s; the beadle copy is not written", name, covered[name].path))

			continue
		}

		// No local copy: keep the covered name from being written while the
		// foreign copy delivers it.
		for key := range out {
			if strings.HasPrefix(key, name+"/") {
				delete(out, key)
			}
		}
	}

	return out
}

// groupFiles lists the snapshot keys of one skill group, sorted.
func groupFiles(items kind.Items, name string) []string {
	prefix := name + "/"

	var files []string

	for key := range items {
		if strings.HasPrefix(key, prefix) {
			files = append(files, key)
		}
	}

	slices.Sort(files)

	return files
}

// groupMatchesBase reports that the local copy is beadle's own and untouched:
// every local file matches the base and the base knows no extra file.
func groupMatchesBase(snapshot, base kind.Items, name string, files []string) bool {
	recorded := 0

	for key := range base {
		if groupName(kind.Skills, key) == name {
			recorded++
		}
	}

	if recorded != len(files) {
		return false
	}

	for _, key := range files {
		if !same(value(snapshot, key), value(base, key)) {
			return false
		}
	}

	return true
}

func (e *Engine) prepareProjects(spec kind.Spec, report *KindReport) bool {
	if spec.ID != kind.Projects {
		return true
	}

	if _, err := e.projectPolicy(); err != nil {
		report.Err = err.Error()

		return false
	}

	return true
}

func (e *Engine) pushGroups(
	ctx context.Context, spec kind.Spec, views []*view, vaultItems kind.Items, opts SyncOptions, st *state.State, report *KindReport,
) {
	for _, group := range groupViews(views) {
		result := AgentResult{Agent: group[0].agent.ID, Mode: group[0].mode, Action: ActionNoop}
		actual := kind.Items{}

		var conflicts []state.Conflict

		// written is what the files hold after the push; actual is what the
		// base records, which a bundle-managed view scopes to its own keys.
		written := kind.Items{}

		for _, v := range group {
			items, single := e.pushView(ctx, spec, v, vaultItems, opts, report)
			maps.Copy(written, items)

			if v.bundleManaged {
				items = v.managedBase(items)
			}

			maps.Copy(actual, items)

			mergeResults(&result, single)

			conflicts = append(conflicts, v.conflicts...)
		}

		report.add(result)
		st.ReplaceConflicts(spec.ID, result.Agent, conflicts)

		if opts.DryRun {
			continue
		}

		if err := e.storeBase(st, spec, group, actual); err != nil {
			report.Err = err.Error()
		}

		e.recordComplement(st, spec, group, written)
	}
}

func (e *Engine) warnProjectGit(report *KindReport, items kind.Items) {
	policy, err := e.projectPolicy()
	if err != nil {
		report.Warnings = append(report.Warnings, "project: "+err.Error())

		return
	}

	id := e.projectIdentity().ID

	for _, rel := range e.projectRels() {
		if !policy.Enabled(rel) {
			continue
		}

		publishable, err := e.projectPublishable(rel)
		if err != nil {
			report.Warnings = append(report.Warnings, "project "+rel+": "+err.Error())
		}

		if publishable || policy.AllowsSecrets(rel) || !strings.HasSuffix(rel, ".json") {
			continue
		}

		if bytes.Contains(items[id+"/"+rel], []byte("{secret:")) {
			report.Warnings = append(report.Warnings, fmt.Sprintf(
				"project %s is git-tracked and holds secret references; values are rendered as ${NAME}", rel))
		}
	}
}

func groupViews(views []*view) [][]*view {
	groups := map[string][]*view{}

	var order []string

	for _, v := range views {
		if _, seen := groups[v.agent.ID]; !seen {
			order = append(order, v.agent.ID)
		}

		groups[v.agent.ID] = append(groups[v.agent.ID], v)
	}

	out := make([][]*view, 0, len(order))

	for _, id := range order {
		out = append(out, groups[id])
	}

	return out
}

func mergeResults(into *AgentResult, single AgentResult) {
	into.Changes = append(into.Changes, single.Changes...)

	if resultRank(single.Action) > resultRank(into.Action) {
		into.Action, into.Note = single.Action, single.Note
	}

	if into.ReloadHint == "" {
		into.ReloadHint = single.ReloadHint
	}
}

func resultRank(action Action) int {
	switch action {
	case ActionError:
		return 5
	case ActionPushed:
		return 4
	case ActionWouldPush:
		return 3
	case ActionSkipped:
		return 2
	case ActionAlias:
		return 1
	default:
		return 0
	}
}

func (e *Engine) readViews(
	ctx context.Context, spec kind.Spec, agents []*agent.Agent, st *state.State, opts SyncOptions, pluginPlan pluginMCPPlan, report *KindReport,
) []*view {
	var views []*view

	owners := map[string]string{}
	// chainSeen remembers the content of every enabled chain file per agent,
	// including the files the owners dedup later drops as aliases: the canon
	// must hold one element per unique content, whichever surface delivers it.
	chainSeen := map[string][][]byte{}

	for _, a := range agents {
		for _, surface := range a.SurfacesOf(spec.ID) {
			if !e.surfaceEnabled(surface) {
				continue
			}

			mode := restrict(e.config.ModeFor(a.ID, spec.ID, surface.Traits().DefaultMode), opts.Direction)
			managed := false

			if mode == config.ModeOff {
				if !e.bundleManagesMCP(st, a.ID, spec.ID, opts) {
					continue
				}

				mode = config.ModePush
				managed = true
			}

			v, err := e.readView(ctx, spec, a, surface, mode, st)
			if err != nil {
				report.add(AgentResult{Agent: a.ID, Mode: mode, Action: ActionError, Note: err.Error()})

				continue
			}

			if managed {
				v.bundleManaged = true
				v.presentsPlugins = e.bundlePresentsPluginMCP(st, a.ID, pluginPlan, opts)
				v.complement.owned = complementOwned(st, a.ID, spec.ID)
			}

			if _, chain := surface.(agent.ProjectRootRel); chain {
				shadowChainView(chainSeen, a.ID, v)
			}

			for _, reason := range slices.Sorted(maps.Values(v.snap.Unreadable)) {
				report.Warnings = append(report.Warnings, fmt.Sprintf(
					"%s/%s: %s; the item is left untouched", a.ID, spec.ID, reason))
			}

			for _, warning := range v.snap.Warnings {
				report.Warnings = append(report.Warnings, fmt.Sprintf("%s/%s: %s", a.ID, spec.ID, warning))
			}

			if v.snap.Present {
				realPath := agent.RealPath(surface.Path())
				if owner, taken := owners[realPath]; taken {
					report.add(AgentResult{Agent: a.ID, Mode: mode, Action: ActionAlias, Note: "same file as " + owner})

					continue
				}

				owners[realPath] = a.ID
			}

			views = append(views, v)
		}
	}

	return views
}

// shadowChainView drops the items of a chain file whose content an earlier
// enabled chain file of the same agent already carries: the first file keeps
// the canon element and identical siblings render nothing. The content is
// remembered even when the owners dedup later drops the view as an alias, so a
// file delivered through another agent's rel still collapses its twins.
// Disabled surfaces never reach this point — readViews skips them — so a
// disabled predecessor cannot hide an enabled sibling.
func shadowChainView(seen map[string][][]byte, agentID string, v *view) {
	data := singleContent(v.snap.Items)
	if data == nil {
		return
	}

	agentSeen := seen[agentID]

	if slices.ContainsFunc(agentSeen, func(prev []byte) bool { return bytes.Equal(prev, data) }) {
		v.snap.Items = kind.Items{}
		v.raw = kind.Items{}

		return
	}

	seen[agentID] = append(agentSeen, data)
}

// singleContent returns the only value of an item set, or nil when it holds
// none or several.
func singleContent(items kind.Items) []byte {
	if len(items) != 1 {
		return nil
	}

	for _, data := range items {
		return data
	}

	return nil
}

func (e *Engine) surfaceEnabled(surface agent.Surface) bool {
	file, ok := surface.(agent.ProjectFile)
	if !ok {
		return true
	}

	policy, err := e.projectPolicy()
	if err != nil {
		return false
	}

	return policy.Enabled(file.ProjectRel())
}

func (e *Engine) readView(
	ctx context.Context, spec kind.Spec, a *agent.Agent, surface agent.Surface, mode config.Mode, st *state.State,
) (*view, error) {
	snap, err := surface.Read(ctx)
	if err != nil {
		return nil, err
	}

	raw := maps.Clone(snap.Items)

	items, moved, err := e.inboundCount(spec.ID, snap.Items)
	if err != nil {
		return nil, err
	}

	snap.Items = normalize(items)

	base, err := e.loadBase(st, spec.ID, a.ID)
	if err != nil {
		return nil, err
	}

	if len(a.SurfacesOf(spec.ID)) > 1 {
		base = ownedBy(surface, base)
	}

	return &view{agent: a, surface: surface, mode: mode, snap: snap, raw: raw, base: base, moved: moved, holds: map[string]bool{}}, nil
}

func ownedBy(surface agent.Surface, items kind.Items) kind.Items {
	projector, ok := surface.(agent.Projector)
	if !ok {
		return items
	}

	out := kind.Items{}

	for key, data := range items {
		if _, _, visible := projector.Project(key, data); visible {
			out[key] = data
		}
	}

	return out
}

func (e *Engine) pull(spec kind.Spec, v *view, vaultItems kind.Items, owned map[string]struct{}, report *KindReport) {
	proj := project(vaultItems, v.surface)

	var deleted []string

	for _, key := range unionKeys(v.base, proj.items, v.snap.Items) {
		if _, skip := owned[key]; skip {
			continue
		}

		if _, skip := v.snap.Unreadable[spec.Group(key)]; skip {
			continue
		}

		if e.pullKey(spec, v, proj, vaultItems, key, report) {
			deleted = append(deleted, key)
		}
	}

	e.pullDeletions(spec, v, proj, vaultItems, deleted, report)
}

func (e *Engine) pullKey(spec kind.Spec, v *view, proj projection, vaultItems kind.Items, key string, report *KindReport) bool {
	base, current, local := value(v.base, key), value(proj.items, key), value(v.snap.Items, key)

	if e.keepImplicitDelete(spec, v, key, base, current, local, report) {
		return false
	}

	current = rememberedCurrent(spec, proj, vaultItems, key, base, local, current)

	switch {
	case same(local, base), same(current, local):
		return false
	case same(current, base):
		switch {
		case local == nil:
			return !spec.Singleton
		case base == nil && proj.hides(key):
			e.addConflict(spec, v, proj, vaultItems, key, state.ReasonHidden, base, vaultItems[key], local, report)
		default:
			adopt(spec, proj, vaultItems, v.agent.ID, key, local, report)
		}
	default:
		if merged, ok := spec.Merge(base, current, local); ok {
			adopt(spec, proj, vaultItems, v.agent.ID, key, merged, report)
		} else {
			e.addConflict(spec, v, proj, vaultItems, key, reasonOf(base, current, local), base, current, local, report)
		}
	}

	return false
}

func (e *Engine) pullDeletions(spec kind.Spec, v *view, proj projection, vaultItems kind.Items, deleted []string, report *KindReport) {
	if len(deleted) == 0 {
		return
	}

	if massDeletion(spec, v.base, deleted) {
		for _, key := range deleted {
			e.addConflict(spec, v, proj, vaultItems, key, state.ReasonMassDelete, v.base[key], proj.items[key], nil, report)
		}

		return
	}

	for _, key := range deleted {
		adopt(spec, proj, vaultItems, v.agent.ID, key, nil, report)
	}
}

func adopt(spec kind.Spec, proj projection, vaultItems kind.Items, agentID, key string, updated []byte, report *KindReport) {
	for _, target := range proj.targets(key) {
		before := value(vaultItems, target)
		after := updated

		if updated != nil && before != nil && spec.Lift != nil {
			if projected := value(proj.items, key); projected != nil {
				after = spec.Lift(before, projected, updated)
			}
		}

		if after == nil {
			delete(vaultItems, target)
		} else {
			vaultItems[target] = after
		}

		if !same(before, after) {
			report.Pulled = append(report.Pulled, Change{Agent: agentID, Key: target, Op: opOf(before, after)})
		}
	}
}

func (e *Engine) addConflict(spec kind.Spec, v *view, proj projection, vaultItems kind.Items, key, reason string, base, current, local []byte, report *KindReport) {
	c := state.Conflict{
		Kind:   spec.ID,
		Agent:  v.agent.ID,
		Key:    key,
		Reason: reason,
		Base:   hashOf(base),
		Vault:  hashOf(current),
		Local:  hashOf(local),
		Since:  e.now().UTC(),
	}

	if targets := proj.targets(key); len(targets) == 1 && targets[0] != key {
		c.VaultKey = targets[0]
	}

	if e.applyRuling(spec, v, proj, vaultItems, c, base, current, local, report) {
		return
	}

	e.observeRuling(c, base, current, local, report)

	v.conflicts = append(v.conflicts, c)
	v.holds[spec.Group(key)] = true

	for _, blob := range [][]byte{base, current, local} {
		if blob != nil {
			v.blobs = append(v.blobs, blob)
		}
	}
}

func (e *Engine) applyRuling(
	spec kind.Spec, v *view, proj projection, vaultItems kind.Items, c state.Conflict, base, current, local []byte, report *KindReport,
) bool {
	if e.rulings == nil {
		return false
	}

	sig, err := e.signatureFor(c, base, current, local)
	if err != nil {
		return false
	}

	match := e.rulings.Match(sig)
	if !match.Found || !match.Exact || match.Ruling.State != rulings.StateTrusted || sig.BlastRadius() {
		return false
	}

	switch match.Ruling.Ruling {
	case rulings.RulingTakeAgent:
		adopt(spec, proj, vaultItems, v.agent.ID, c.Key, local, report)
	case rulings.RulingTakeVault:
	default:
		return false
	}

	e.rulings.Applied(sig, e.now().UTC())

	e.rulingsDirty = true

	report.RulingsApplied = append(report.RulingsApplied, RulingEvent{Signature: sig, Ruling: match.Ruling.Ruling, Kind: c.Kind, Agent: c.Agent, Key: c.Key})

	return true
}

func (e *Engine) observeRuling(c state.Conflict, base, current, local []byte, report *KindReport) {
	if e.rulings == nil {
		return
	}

	sig, err := e.signatureFor(c, base, current, local)
	if err != nil {
		return
	}

	if existing, ok := e.rulings.Get(sig); ok {
		if existing.State == rulings.StateSuggested {
			report.RulingSuggestions = append(report.RulingSuggestions, RulingEvent{Signature: sig, Ruling: existing.Ruling, Kind: c.Kind, Agent: c.Agent, Key: c.Key})
		}

		return
	}

	e.rulings.Observe(sig, "", e.now().UTC())
	e.rulingsDirty = true
}

func (e *Engine) pushView(
	ctx context.Context, spec kind.Spec, v *view, vaultItems kind.Items, opts SyncOptions, report *KindReport,
) (kind.Items, AgentResult) {
	single := AgentResult{Agent: v.agent.ID, Mode: v.mode, Action: ActionNoop}

	if !v.mode.Pushes() {
		single.Action = ActionPullOnly

		return v.snap.Items, single
	}

	desired := e.desired(spec, v, vaultItems)

	desired = dropInvalidMCP(spec, desired)
	desired = e.suppressKept(spec, v, desired, report)
	desired = e.complementSkills(spec, v, desired, report)
	desired = e.deliverComplement(spec, v, vaultItems, desired, report)

	// The ref-form comparison alone cannot see the secrets mode: both the
	// snapshot and the desired projection hold {secret:NAME} references, so a
	// literal↔env switch changes only the rendered file form. Compare what
	// the file is supposed to hold under the current mode with what it holds.
	resolved, _, err := e.outbound(spec.ID, desired)
	if err != nil {
		resolved = nil // write() reports the error with the server name
	}

	// A snapshot that needs normalizing (host-schema strays) must reach the
	// surface even when the canon already matches: otherwise the file keeps
	// its stray keys and the host ignores the rest of the frontmatter.
	if desired.Equal(v.snap.Items) && v.unchangedForm(resolved) && !v.snap.NeedsRewrite {
		return v.snap.Items, single
	}

	// A surface that cannot be created (a missing config file the host never
	// initializes) takes no writes: report the skip instead of listing pending
	// changes a sync will drop.
	if !v.snap.Present && !v.surface.Traits().Creatable {
		single.Action = ActionSkipped
		single.Note = "no config file to write into; create " + v.surface.Path() + " first"

		return v.snap.Items, single
	}

	single.Changes = diffItems(v.snap.Items, desired)

	if len(single.Changes) == 0 && resolved != nil {
		// The refs match but the file form does not: list the affected keys
		// with their ref-form payload so no secret value reaches the report.
		for _, key := range changedKeys(v.raw, resolved) {
			single.Changes = append(single.Changes, ItemChange{Key: key, Op: OpModified, Before: v.snap.Items[key], After: desired[key]})
		}
	}

	if opts.DryRun {
		single.Action = ActionWouldPush

		return desired, single
	}

	actual, action, note := e.write(ctx, spec, v, desired)
	single.Action, single.Note = action, note

	if single.Action == ActionPushed {
		single.ReloadHint = v.surface.Traits().ReloadHint
	}

	return actual, single
}

func (e *Engine) noteMCPCanonIssues(spec kind.Spec, opts SyncOptions, items kind.Items, report *KindReport) {
	if spec.ID != kind.MCP || opts.Direction == config.ModePull {
		return
	}

	if issues := mcpCanonIssues(items); len(issues) > 0 {
		report.Err = e.vault.ServersPath() + ": " + strings.Join(issues, "; ")
	}
}

func mcpCanonIssues(items kind.Items) []string {
	var issues []string

	for _, name := range slices.Sorted(maps.Keys(items)) {
		if _, err := mcp.Decode(items[name]); err != nil {
			issues = append(issues, fmt.Sprintf("server %s: %v", name, err))
		}
	}

	return issues
}

func dropInvalidMCP(spec kind.Spec, items kind.Items) kind.Items {
	if spec.ID != kind.MCP {
		return items
	}

	out := make(kind.Items, len(items))

	for name, data := range items {
		if _, err := mcp.Decode(data); err == nil {
			out[name] = data
		}
	}

	return out
}

func rememberedCurrent(spec kind.Spec, proj projection, vaultItems kind.Items, key string, base, local, current []byte) []byte {
	if spec.ID != kind.Memory || local != nil || base == nil || !proj.hides(key) {
		return current
	}

	return value(vaultItems, key)
}

func (e *Engine) keepImplicitDelete(spec kind.Spec, v *view, key string, base, current, local []byte, report *KindReport) bool {
	if !spec.NoImplicitDelete || local != nil || base == nil || current == nil {
		return false
	}

	report.Kept = append(report.Kept, Change{Agent: v.agent.ID, Key: key, Op: OpKept})

	return true
}

func (e *Engine) suppressKept(spec kind.Spec, v *view, desired kind.Items, report *KindReport) kind.Items {
	if !spec.NoImplicitDelete {
		return desired
	}

	out := maps.Clone(desired)
	if out == nil {
		out = kind.Items{}
	}

	for key := range v.base {
		if _, present := v.snap.Items[key]; present {
			continue
		}

		if _, wanted := out[key]; !wanted {
			continue
		}

		delete(out, key)
	}

	return out
}

// unchangedForm reports that the rendered file form matches what the agent
// holds: a bundle-managed view never rewrites the canon remnants it inherited,
// only its complement copies.
func (v *view) unchangedForm(resolved kind.Items) bool {
	if resolved == nil {
		return true
	}

	if v.bundleManaged {
		return v.unchangedComplementForm(resolved)
	}

	return resolved.Equal(v.raw)
}

func (e *Engine) desired(spec kind.Spec, v *view, vaultItems kind.Items) kind.Items {
	if v.bundleManaged {
		return bundleManagedDesired(spec, v)
	}

	proj := project(vaultItems, v.surface)

	if spec.Singleton && len(proj.items) == 0 {
		return maps.Clone(v.snap.Items)
	}

	out := kind.Items{}

	for key, data := range proj.items {
		if !v.frozen(spec, key) {
			out[key] = data
		}
	}

	for key, data := range v.snap.Items {
		// A host file whose canon item the surface cannot express is kept:
		// hiding an item must never read as "delete the host file".
		if v.frozen(spec, key) || proj.hiddenTarget(key) {
			out[key] = data
		}
	}

	e.mergePresented(spec, v, vaultItems, out)

	return out
}

// bundleManagedDesired keeps the canon remnants as they are (they stay
// untouched until a verified withdrawal), adds the plugin-sourced items and
// drops the servers of plugins that are gone; deliverComplement then adds what
// the bundle cannot carry.
func bundleManagedDesired(spec kind.Spec, v *view) kind.Items {
	out := maps.Clone(v.snap.Items)
	if out == nil {
		out = kind.Items{}
	}

	for key, data := range v.presented {
		if _, taken := out[key]; taken || v.frozen(spec, key) {
			continue
		}

		out[key] = data
	}

	for key := range out {
		if _, presented := v.presented[key]; presented {
			continue
		}

		if _, pluginOwned := v.pluginOwned[key]; pluginOwned {
			delete(out, key)
		}
	}

	return out
}

func (e *Engine) mergePresented(spec kind.Spec, v *view, vaultItems, out kind.Items) {
	for key, data := range v.presented {
		if _, taken := out[key]; taken || v.frozen(spec, key) {
			continue
		}

		out[key] = data
	}

	if !v.presentedFailed {
		return
	}

	for key, data := range v.snap.Items {
		if _, canon := vaultItems[key]; canon || v.frozen(spec, key) {
			continue
		}

		out[key] = data
	}
}

func (e *Engine) write(ctx context.Context, spec kind.Spec, v *view, desired kind.Items) (kind.Items, Action, string) {
	out, missing, err := e.outbound(spec.ID, desired)
	if err != nil {
		return v.snap.Items, ActionError, err.Error()
	}

	if len(missing) > 0 {
		return v.snap.Items, ActionSkipped, "missing secrets: " + strings.Join(missing, ", ")
	}

	if err := v.surface.Write(ctx, out); err != nil {
		if errors.Is(err, agent.ErrNotConfigured) {
			return v.snap.Items, ActionSkipped, "no config file to write into; create " + v.surface.Path() + " first"
		}

		return v.snap.Items, ActionError, err.Error()
	}

	after, err := v.surface.Read(ctx)
	if err != nil {
		return desired, ActionPushed, "written, but reading it back failed: " + err.Error()
	}

	actual, _, err := e.inbound(spec.ID, after.Items)
	if err != nil {
		return desired, ActionPushed, "written, but reading it back failed: " + err.Error()
	}

	actual = normalize(actual)

	if !actual.Equal(desired) {
		return actual, ActionPushed, "the agent did not keep: " + strings.Join(changedKeys(actual, desired), ", ")
	}

	return actual, ActionPushed, ""
}

func (e *Engine) storeBase(st *state.State, spec kind.Spec, views []*view, actual kind.Items) error {
	base := state.Base{}

	put := func(key string, data []byte) error {
		hash, err := e.store.Put(data)
		if err != nil {
			return err
		}

		base[key] = hash

		return nil
	}

	held := func(key string) bool {
		for _, v := range views {
			if v.holds[spec.Group(key)] {
				return true
			}
		}

		return false
	}

	if err := writeActualBase(actual, held, put); err != nil {
		return err
	}

	if err := writeHeldBase(views, held, base, put, func(blob []byte) error {
		_, err := e.store.Put(blob)

		return err
	}); err != nil {
		return err
	}

	if err := e.retainKeptBase(spec, views, actual, base, put); err != nil {
		return err
	}

	st.SetBase(spec.ID, views[0].agent.ID, base)

	return nil
}

func writeActualBase(actual kind.Items, held func(string) bool, put func(string, []byte) error) error {
	for key, data := range actual {
		if held(key) {
			continue
		}

		if err := put(key, data); err != nil {
			return err
		}
	}

	return nil
}

func writeHeldBase(
	views []*view, held func(string) bool, base state.Base, put func(string, []byte) error, putBlob func([]byte) error,
) error {
	for _, v := range views {
		for key, data := range v.base {
			if !held(key) {
				continue
			}

			if _, done := base[key]; done {
				continue
			}

			if err := put(key, data); err != nil {
				return err
			}
		}

		for _, blob := range v.blobs {
			if err := putBlob(blob); err != nil {
				return err
			}
		}
	}

	return nil
}

func (e *Engine) retainKeptBase(
	spec kind.Spec, views []*view, actual kind.Items, base state.Base, put func(string, []byte) error,
) error {
	if !spec.NoImplicitDelete {
		return nil
	}

	for _, v := range views {
		for key, data := range v.base {
			if _, done := base[key]; done {
				continue
			}

			if _, written := actual[key]; written {
				continue
			}

			if err := put(key, data); err != nil {
				return err
			}
		}
	}

	return nil
}

func (e *Engine) loadBase(st *state.State, k kind.ID, agentID string) (kind.Items, error) {
	base, _ := st.Base(k, agentID)
	items := make(kind.Items, len(base))

	for key, hash := range base {
		data, err := e.store.Get(hash)
		if err != nil {
			return nil, fmt.Errorf("sync state of %s is damaged (%s): run beadle doctor: %w", agentID, key, err)
		}

		items[key] = data
	}

	return normalize(items), nil
}

type projection struct {
	items  kind.Items
	vkeys  map[string][]string
	hidden map[string]bool
}

func project(vaultItems kind.Items, surface agent.Surface) projection {
	proj := projection{items: kind.Items{}, vkeys: map[string][]string{}, hidden: map[string]bool{}}
	projector, lossy := surface.(agent.Projector)

	for _, vkey := range vaultItems.Keys() {
		pkey, pvalue, ok := vkey, vaultItems[vkey], true
		if lossy {
			pkey, pvalue, ok = projector.Project(vkey, pvalue)
		}

		if !ok {
			proj.hidden[vkey] = true

			continue
		}

		if _, taken := proj.items[pkey]; !taken {
			proj.items[pkey] = pvalue
		}

		proj.vkeys[pkey] = append(proj.vkeys[pkey], vkey)
	}

	return proj
}

func (p projection) targets(key string) []string {
	if vkeys := p.vkeys[key]; len(vkeys) > 0 {
		return vkeys
	}

	return []string{key}
}

func (p projection) hides(key string) bool {
	return p.hidden[key]
}

// hiddenTarget reports whether a host key is a vault item the surface cannot
// express: the host file is kept instead of being deleted. The check compares
// keys directly, which is exact for kinds whose item key is the identity
// (commands, subagents). A kind that remaps keys (permissions) has no forward
// mapping for a hidden item, so its host file is not recognised here.
func (p projection) hiddenTarget(key string) bool {
	return p.hidden[key]
}

func restrict(mode, direction config.Mode) config.Mode {
	switch direction {
	case config.ModePull:
		if mode.Pulls() {
			return config.ModePull
		}

		return config.ModeOff
	case config.ModePush:
		if mode.Pushes() {
			return config.ModePush
		}

		return config.ModeOff
	default:
		return mode
	}
}

func massDeletion(spec kind.Spec, base kind.Items, deleted []string) bool {
	groups := map[string]struct{}{}

	for key := range base {
		groups[spec.Group(key)] = struct{}{}
	}

	gone := map[string]struct{}{}

	for _, key := range deleted {
		gone[spec.Group(key)] = struct{}{}
	}

	d, n := len(gone), len(groups)

	return (d >= 2 && d == n) || (d > 3 && d*2 > n)
}

func value(items kind.Items, key string) []byte {
	data, ok := items[key]
	if !ok {
		return nil
	}

	if data == nil {
		return []byte{}
	}

	return data
}

func same(a, b []byte) bool {
	if (a == nil) != (b == nil) {
		return false
	}

	return bytes.Equal(a, b)
}

func normalize(items kind.Items) kind.Items {
	if items == nil {
		return kind.Items{}
	}

	for key, data := range items {
		if data == nil {
			items[key] = []byte{}
		}
	}

	return items
}

func hashOf(data []byte) cas.Hash {
	if data == nil {
		return ""
	}

	return cas.HashOf(data)
}

func reasonOf(base, current, local []byte) string {
	switch {
	case base == nil:
		return state.ReasonAdded
	case current == nil || local == nil:
		return state.ReasonDeleted
	default:
		return state.ReasonModified
	}
}

func opOf(before, after []byte) string {
	switch {
	case before == nil:
		return OpAdded
	case after == nil:
		return OpDeleted
	default:
		return OpModified
	}
}

func unionKeys(sets ...kind.Items) []string {
	keys := map[string]struct{}{}

	for _, set := range sets {
		for key := range set {
			keys[key] = struct{}{}
		}
	}

	return slices.Sorted(maps.Keys(keys))
}

func diffItems(before, after kind.Items) []ItemChange {
	var changes []ItemChange

	for _, key := range unionKeys(before, after) {
		old, updated := value(before, key), value(after, key)
		if same(old, updated) {
			continue
		}

		changes = append(changes, ItemChange{Key: key, Op: opOf(old, updated), Before: old, After: updated})
	}

	return changes
}

func changedKeys(a, b kind.Items) []string {
	var keys []string

	for _, key := range unionKeys(a, b) {
		if !same(value(a, key), value(b, key)) {
			keys = append(keys, key)
		}
	}

	return keys
}
