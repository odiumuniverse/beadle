package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/odiumuniverse/agents-sync/pkg/agent"
	"github.com/odiumuniverse/agents-sync/pkg/cas"
	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
	"github.com/odiumuniverse/agents-sync/pkg/state"
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
	holds           map[string]bool
	conflicts       []state.Conflict
	blobs           [][]byte
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

	var (
		pluginPlan   pluginMCPPlan
		pluginLedger pluginLedger
	)

	if spec.ID == kind.MCP {
		pluginLedger, pluginPlan = e.loadPluginMCPPlan(vaultItems, &report)
	}

	views := e.readViews(ctx, spec, agents, st, opts, &report)
	original := maps.Clone(vaultItems)
	owned := e.ownedPluginMCP(spec, views, pluginPlan, pluginLedger, vaultItems, &report)

	for _, v := range views {
		if v.mode.Pulls() {
			e.pull(spec, v, vaultItems, owned, &report)
		}
	}

	report.VaultChanged = !vaultItems.Equal(original)

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

	for _, v := range views {
		actual := e.push(ctx, spec, v, vaultItems, opts, &report)

		st.ReplaceConflicts(spec.ID, v.agent.ID, v.conflicts)

		if opts.DryRun {
			continue
		}

		if err := e.storeBase(st, spec, v, actual); err != nil {
			report.Err = err.Error()
		}
	}

	return report
}

func (e *Engine) readViews(
	ctx context.Context, spec kind.Spec, agents []*agent.Agent, st *state.State, opts SyncOptions, report *KindReport,
) []*view {
	var views []*view

	owners := map[string]string{}

	for _, a := range agents {
		surface := a.Surface(spec.ID)
		if surface == nil {
			continue
		}

		mode := restrict(e.config.ModeFor(a.ID, spec.ID, surface.Traits().DefaultMode), opts.Direction)
		if mode == config.ModeOff {
			continue
		}

		v, err := e.readView(ctx, spec, a, surface, mode, st)
		if err != nil {
			report.add(AgentResult{Agent: a.ID, Mode: mode, Action: ActionError, Note: err.Error()})

			continue
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

	return views
}

func (e *Engine) readView(
	ctx context.Context, spec kind.Spec, a *agent.Agent, surface agent.Surface, mode config.Mode, st *state.State,
) (*view, error) {
	snap, err := surface.Read(ctx)
	if err != nil {
		return nil, err
	}

	raw := maps.Clone(snap.Items)

	items, _, err := e.inbound(spec.ID, snap.Items)
	if err != nil {
		return nil, err
	}

	snap.Items = normalize(items)

	base, err := e.loadBase(st, spec.ID, a.ID)
	if err != nil {
		return nil, err
	}

	return &view{agent: a, surface: surface, mode: mode, snap: snap, raw: raw, base: base, holds: map[string]bool{}}, nil
}

func (e *Engine) pull(spec kind.Spec, v *view, vaultItems kind.Items, owned map[string]struct{}, report *KindReport) {
	proj := project(vaultItems, v.surface)

	var deleted []string

	for _, key := range unionKeys(v.base, proj.items, v.snap.Items) {
		if _, skip := owned[key]; skip {
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

	switch {
	case same(local, base), same(current, local):
		return false
	case same(current, base):
		switch {
		case local == nil:
			return !spec.Singleton
		case base == nil && proj.hides(key):
			e.addConflict(spec, v, proj, key, state.ReasonHidden, base, vaultItems[key], local)
		default:
			adopt(spec, proj, vaultItems, v.agent.ID, key, local, report)
		}
	default:
		if merged, ok := spec.Merge(base, current, local); ok {
			adopt(spec, proj, vaultItems, v.agent.ID, key, merged, report)
		} else {
			e.addConflict(spec, v, proj, key, reasonOf(base, current, local), base, current, local)
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
			e.addConflict(spec, v, proj, key, state.ReasonMassDelete, v.base[key], proj.items[key], nil)
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

func (e *Engine) addConflict(spec kind.Spec, v *view, proj projection, key, reason string, base, current, local []byte) {
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

	v.conflicts = append(v.conflicts, c)
	v.holds[spec.Group(key)] = true

	for _, blob := range [][]byte{base, current, local} {
		if blob != nil {
			v.blobs = append(v.blobs, blob)
		}
	}
}

func (e *Engine) push(ctx context.Context, spec kind.Spec, v *view, vaultItems kind.Items, opts SyncOptions, report *KindReport) kind.Items {
	result := AgentResult{Agent: v.agent.ID, Mode: v.mode, Action: ActionNoop}

	actual := e.pushView(ctx, spec, v, vaultItems, opts, &result)

	report.add(result)

	return actual
}

func (e *Engine) pushView(
	ctx context.Context, spec kind.Spec, v *view, vaultItems kind.Items, opts SyncOptions, result *AgentResult,
) kind.Items {
	if !v.mode.Pushes() {
		result.Action = ActionPullOnly

		return v.snap.Items
	}

	desired := e.desired(spec, v, vaultItems)

	// The ref-form comparison alone cannot see the secrets mode: both the
	// snapshot and the desired projection hold {secret:NAME} references, so a
	// literal↔env switch changes only the rendered file form. Compare what
	// the file is supposed to hold under the current mode with what it holds.
	resolved, _, err := e.outbound(spec.ID, desired)
	if err != nil {
		resolved = nil // write() reports the error with the server name
	}

	if desired.Equal(v.snap.Items) && (resolved == nil || resolved.Equal(v.raw)) {
		return v.snap.Items
	}

	result.Changes = diffItems(v.snap.Items, desired)

	if len(result.Changes) == 0 && resolved != nil {
		// The refs match but the file form does not: list the affected keys
		// with their ref-form payload so no secret value reaches the report.
		for _, key := range changedKeys(v.raw, resolved) {
			result.Changes = append(result.Changes, ItemChange{Key: key, Op: OpModified, Before: v.snap.Items[key], After: desired[key]})
		}
	}

	if !v.snap.Present && !v.surface.Traits().Creatable {
		result.Action, result.Note = ActionSkipped, "no config file to write into"

		return v.snap.Items
	}

	if opts.DryRun {
		result.Action = ActionWouldPush

		return desired
	}

	actual, action, note := e.write(ctx, spec, v, desired)
	result.Action, result.Note = action, note

	return actual
}

func (e *Engine) desired(spec kind.Spec, v *view, vaultItems kind.Items) kind.Items {
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
		if v.frozen(spec, key) {
			out[key] = data
		}
	}

	e.mergePresented(spec, v, vaultItems, out)

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
			return v.snap.Items, ActionSkipped, "no config file to write into"
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

func (e *Engine) storeBase(st *state.State, spec kind.Spec, v *view, actual kind.Items) error {
	base := state.Base{}

	put := func(key string, data []byte) error {
		hash, err := e.store.Put(data)
		if err != nil {
			return err
		}

		base[key] = hash

		return nil
	}

	for key, data := range actual {
		if v.holds[spec.Group(key)] {
			continue
		}

		if err := put(key, data); err != nil {
			return err
		}
	}

	for key, data := range v.base {
		if !v.holds[spec.Group(key)] {
			continue
		}

		if err := put(key, data); err != nil {
			return err
		}
	}

	for _, blob := range v.blobs {
		if _, err := e.store.Put(blob); err != nil {
			return err
		}
	}

	st.SetBase(spec.ID, v.agent.ID, base)

	return nil
}

func (e *Engine) loadBase(st *state.State, k kind.ID, agentID string) (kind.Items, error) {
	base, _ := st.Base(k, agentID)
	items := make(kind.Items, len(base))

	for key, hash := range base {
		data, err := e.store.Get(hash)
		if err != nil {
			return nil, fmt.Errorf("sync state of %s is damaged (%s): run agent-sync doctor: %w", agentID, key, err)
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
