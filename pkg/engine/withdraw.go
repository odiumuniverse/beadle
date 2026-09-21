package engine

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
)

const (
	withdrawKeepModified    = "modified"
	withdrawKeepUnmanaged   = "unmanaged"
	withdrawKeepUndelivered = "undelivered"
	withdrawKeepNoShared    = "kept for other agents"
)

// WithdrawDecision is one canon element the withdrawal pass considered for a
// host file surface.
type WithdrawDecision struct {
	Kind    kind.ID
	Name    string
	Files   []string
	Digest  cas.Hash
	KeepFor string
}

// Withdraws reports whether the element leaves the file surface.
func (d WithdrawDecision) Withdraws() bool { return d.KeepFor == "" }

// planWithdrawal decides which canon elements beadle owns (state base) and
// the bundle delivers may leave the host file surface. It is pure: every
// filesystem read happens in the caller.
func planWithdrawal(
	host bundle.Host, k kind.ID, canon, delivered map[string]struct{},
	snapshot kind.Items, base map[string]cas.Hash, shared map[string]bool,
) []WithdrawDecision {
	groups := snapshotGroups(snapshot, k)

	decisions := make([]WithdrawDecision, 0, len(groups))

	for _, name := range slices.Sorted(maps.Keys(groups)) {
		if _, ok := canon[name]; !ok {
			continue
		}

		files := groups[name]
		decision := WithdrawDecision{Kind: k, Name: name, Files: files}

		switch {
		case !baseOwns(base, k, name):
			decision.KeepFor = withdrawKeepUnmanaged
		case !snapshotMatchesBase(snapshot, base, k, name, files):
			decision.KeepFor = withdrawKeepModified
		case !containsName(delivered, name):
			decision.KeepFor = withdrawKeepUndelivered
		case k == kind.Skills && host == bundle.Claude && !shared[name]:
			decision.KeepFor = withdrawKeepNoShared
		default:
			decision.Digest = groupDigest(snapshot, files)
		}

		decisions = append(decisions, decision)
	}

	return decisions
}

func snapshotGroups(snapshot kind.Items, k kind.ID) map[string][]string {
	groups := map[string][]string{}

	for key := range snapshot {
		name := groupName(k, key)
		groups[name] = append(groups[name], key)
	}

	for name := range groups {
		slices.Sort(groups[name])
	}

	return groups
}

func groupName(k kind.ID, key string) string {
	if k == kind.Skills {
		name, _, _ := strings.Cut(key, "/")

		return name
	}

	return key
}

func baseOwns(base map[string]cas.Hash, k kind.ID, name string) bool {
	for key := range base {
		if groupName(k, key) == name {
			return true
		}
	}

	return false
}

// snapshotMatchesBase requires the whole element to match: every current file
// has the recorded hash, and the base knows no extra file of the element
// (a partial tree is never shredded).
func snapshotMatchesBase(snapshot kind.Items, base map[string]cas.Hash, k kind.ID, name string, files []string) bool {
	recorded := 0

	for key := range base {
		if groupName(k, key) == name {
			recorded++
		}
	}

	if recorded != len(files) {
		return false
	}

	for _, key := range files {
		hash, ok := base[key]
		if !ok || hash != cas.HashOf(snapshot[key]) {
			return false
		}
	}

	return true
}

func groupDigest(snapshot kind.Items, files []string) cas.Hash {
	var builder strings.Builder

	for _, key := range files {
		builder.WriteString(key)
		builder.WriteByte(0)
		builder.WriteString(string(cas.HashOf(snapshot[key])))
		builder.WriteByte('\n')
	}

	return cas.HashOf([]byte(builder.String()))
}

func containsName(names map[string]struct{}, name string) bool {
	_, ok := names[name]

	return ok
}

// withdrawPlan is one kind's prepared withdrawal: the file surface and the
// item set with the canon elements removed, plus the audit records.
type withdrawPlan struct {
	kind    kind.ID
	surface agent.Surface
	desired kind.Items
	records []state.WithdrawnItem
}

// planBundleWithdrawal reads the host surfaces and decides what leaves them.
// It performs no writes, so the caller can checkpoint the records before the
// deletions run.
func (e *Engine) planBundleWithdrawal(
	ctx context.Context, host bundle.Host, st *state.State, req bundle.Request, cov coverage,
) ([]withdrawPlan, []string, []string) {
	var (
		plans []withdrawPlan
		kept  []string
		warns []string
	)

	for _, k := range host.Kinds() {
		surface := e.bundleSurface(host, k)
		if surface == nil {
			continue
		}

		canonItems, _, err := e.loadVault(k)
		if err != nil {
			warns = append(warns, "bundles: "+err.Error())

			continue
		}

		snap, err := surface.Read(ctx)
		if err != nil {
			warns = append(warns, fmt.Sprintf("bundles: cannot read the %s %s surface: %v", host, k, err))

			continue
		}

		base, err := e.withdrawalBase(st, k, host.AgentID())
		if err != nil {
			warns = append(warns, fmt.Sprintf("bundles: %s %s ownership is unreadable: %v", host, k, err))

			continue
		}

		canon := itemGroups(canonItems, k)
		delivered := withdrawalDelivered(k, req, cov)

		var shared map[string]bool
		if k == kind.Skills {
			shared = e.sharedSkillCopies(ctx, canon)
		}

		// Read-only entries (symlinked skills) are not beadle's to remove:
		// the surface write skips them, so they never enter the plan.
		items, keptReadOnly := writableItems(snap, k)
		kept = append(kept, keptReadOnly...)

		decisions := planWithdrawal(host, k, canon, delivered, items, base, shared)

		desired := maps.Clone(items)
		if desired == nil {
			desired = kind.Items{}
		}

		var records []state.WithdrawnItem

		for _, decision := range decisions {
			if !decision.Withdraws() {
				kept = append(kept, fmt.Sprintf("%s %s (%s)", k, decision.Name, decision.KeepFor))

				if decision.KeepFor != withdrawKeepUnmanaged {
					warns = append(warns, fmt.Sprintf("bundles: kept %s %s in %s files (%s)", k, decision.Name, host, decision.KeepFor))
				}

				continue
			}

			for _, file := range decision.Files {
				delete(desired, file)
			}

			records = append(records, state.WithdrawnItem{
				Kind: k, Name: decision.Name, Digest: decision.Digest, At: e.now().UTC(),
			})
		}

		if len(records) == 0 {
			continue
		}

		plans = append(plans, withdrawPlan{kind: k, surface: surface, desired: desired, records: records})
	}

	return plans, kept, warns
}

// applyBundleWithdrawal removes the planned elements. It reports whether
// every plan succeeded; a partial failure keeps the records so a later
// disable can still materialize the elements back.
func (e *Engine) applyBundleWithdrawal(ctx context.Context, plans []withdrawPlan) ([]state.WithdrawnItem, []string, bool) {
	var withdrawn []state.WithdrawnItem

	var warns []string

	complete := true

	for _, plan := range plans {
		if err := plan.surface.Write(ctx, plan.desired); err != nil {
			warns = append(warns, fmt.Sprintf("bundles: withdrawing %s failed: %v", plan.kind, err))

			complete = false

			continue
		}

		withdrawn = append(withdrawn, plan.records...)
	}

	return withdrawn, warns, complete
}

func plannedWithdrawn(plans []withdrawPlan) []state.WithdrawnItem {
	var records []state.WithdrawnItem

	for _, plan := range plans {
		records = append(records, plan.records...)
	}

	return records
}

// writableItems drops the snapshot groups the surface marked read-only (for
// example a symlinked skill): they cannot be withdrawn and never should be.
func writableItems(snap agent.Snapshot, k kind.ID) (kind.Items, []string) {
	if len(snap.ReadOnly) == 0 {
		return snap.Items, nil
	}

	var kept []string

	out := make(kind.Items, len(snap.Items))

	for key, data := range snap.Items {
		if _, readOnly := snap.ReadOnly[groupName(k, key)]; readOnly {
			continue
		}

		out[key] = data
	}

	for _, name := range slices.Sorted(maps.Keys(snap.ReadOnly)) {
		kept = append(kept, fmt.Sprintf("%s %s (read-only)", k, name))
	}

	return out, kept
}

// withdrawalBase returns the item set beadle owns for the agent, resolved to
// the same file domain the host surface lives in: the base stores secret
// references, the host file stores literals or `${VAR}` forms.
func (e *Engine) withdrawalBase(st *state.State, k kind.ID, agentID string) (map[string]cas.Hash, error) {
	base, err := e.loadBase(st, k, agentID)
	if err != nil {
		return nil, err
	}

	resolved, _, err := e.outbound(k, base)
	if err != nil {
		return nil, err
	}

	hashes := make(map[string]cas.Hash, len(resolved))

	for key, data := range resolved {
		hashes[key] = cas.HashOf(data)
	}

	return hashes, nil
}

func itemGroups(items kind.Items, k kind.ID) map[string]struct{} {
	names := map[string]struct{}{}

	for key := range items {
		names[groupName(k, key)] = struct{}{}
	}

	return names
}

func requestGroups(k kind.ID, req bundle.Request) map[string]struct{} {
	names := map[string]struct{}{}

	if k == kind.Skills {
		for name := range req.Skills {
			names[name] = struct{}{}
		}

		return names
	}

	for name := range req.Servers {
		names[name] = struct{}{}
	}

	return names
}

func (e *Engine) sharedSkillCopies(ctx context.Context, names map[string]struct{}) map[string]bool {
	out := map[string]bool{}

	shared := agent.ByID(e.agents, agent.SharedID)
	if shared == nil {
		return out
	}

	surface := shared.Surface(kind.Skills)
	if surface == nil {
		return out
	}

	if e.config.ModeFor(agent.SharedID, kind.Skills, surface.Traits().DefaultMode) == config.ModeOff {
		return out
	}

	snap, err := surface.Read(ctx)
	if err != nil {
		return out
	}

	for name := range names {
		out[name] = surfaceHasName(snap.Items, kind.Skills, name)
	}

	return out
}

func (e *Engine) rematerializeBundle(ctx context.Context, host bundle.Host, entry state.BundleState) ([]string, []string, error) {
	if len(entry.Withdrawn) == 0 {
		return nil, nil, nil
	}

	// The restore path keeps the unfiltered canon: a covered name may need to
	// come back when its foreign copy is gone.
	req, warns, err := e.bundleRequest(host)
	if err != nil {
		return nil, warns, err
	}

	byKind := map[kind.ID]map[string]state.WithdrawnItem{}

	for _, item := range entry.Withdrawn {
		if byKind[item.Kind] == nil {
			byKind[item.Kind] = map[string]state.WithdrawnItem{}
		}

		byKind[item.Kind][item.Name] = item
	}

	var restored []string

	for _, k := range host.Kinds() {
		pending, ok := byKind[k]
		if !ok {
			continue
		}

		canonItems, _, err := e.loadVault(k)
		if err != nil {
			return restored, warns, err
		}

		names, kindWarns, err := e.rematerializeKind(ctx, host, k, req, itemGroups(canonItems, k), pending)
		warns = append(warns, kindWarns...)
		restored = append(restored, names...)

		if err != nil {
			return restored, warns, err
		}
	}

	return restored, warns, nil
}

func (e *Engine) rematerializeKind(
	ctx context.Context, host bundle.Host, k kind.ID, req bundle.Request,
	canon map[string]struct{}, pending map[string]state.WithdrawnItem,
) ([]string, []string, error) {
	surface := e.bundleSurface(host, k)
	if surface == nil {
		return nil, nil, nil
	}

	snap, err := surface.Read(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot read the %s %s surface: %w", host, k, err)
	}

	desired := maps.Clone(snap.Items)
	if desired == nil {
		desired = kind.Items{}
	}

	var (
		expected []string
		warns    []string
	)

	for _, name := range slices.Sorted(maps.Keys(pending)) {
		tree, ok := canonElement(req, k, name)
		if !ok {
			if _, still := canon[name]; still {
				// The canon holds it, but it cannot be rendered (unresolved
				// secrets): flipping the modes now would let the next sync
				// read the missing file as a deletion.
				return nil, warns, fmt.Errorf("%s %s cannot be restored from the canon (unresolved secrets); the bundle stays enabled", k, name)
			}

			warns = append(warns, fmt.Sprintf("bundles: %s %s is no longer in the canon; not restored", k, name))

			continue
		}

		for rel, data := range tree {
			desired[withdrawKey(k, name, rel)] = data
		}

		expected = append(expected, name)
	}

	if len(expected) == 0 {
		return nil, warns, nil
	}

	if err := surface.Write(ctx, desired); err != nil {
		return nil, warns, fmt.Errorf("restoring the %s %s elements: %w", host, k, err)
	}

	after, err := surface.Read(ctx)
	if err != nil {
		return nil, warns, err
	}

	restored := make([]string, 0, len(expected))

	for _, name := range expected {
		if !surfaceHasName(after.Items, k, name) {
			return nil, warns, fmt.Errorf("the host did not keep the restored %s %s", k, name)
		}

		restored = append(restored, string(k)+" "+name)
	}

	return restored, warns, nil
}

func canonElement(req bundle.Request, k kind.ID, name string) (map[string][]byte, bool) {
	if k == kind.Skills {
		tree, ok := req.Skills[name]

		return tree, ok
	}

	server, ok := req.Servers[name]
	if !ok {
		return nil, false
	}

	return map[string][]byte{"": server}, true
}

func withdrawKey(k kind.ID, name, rel string) string {
	if k == kind.Skills {
		return name + "/" + rel
	}

	return name
}

func mergeWithdrawn(existing, added []state.WithdrawnItem) []state.WithdrawnItem {
	if len(added) == 0 {
		return existing
	}

	merged := map[string]state.WithdrawnItem{}

	for _, item := range slices.Concat(existing, added) {
		merged[string(item.Kind)+"/"+item.Name] = item
	}

	keys := slices.Sorted(maps.Keys(merged))

	out := make([]state.WithdrawnItem, 0, len(keys))

	for _, key := range keys {
		out = append(out, merged[key])
	}

	return out
}

func withdrawNames(items []state.WithdrawnItem) []string {
	out := make([]string, 0, len(items))

	for _, item := range items {
		out = append(out, fmt.Sprintf("%s %s", item.Kind, item.Name))
	}

	return out
}
