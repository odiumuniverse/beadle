package engine

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// complementDelivery is the complement bookkeeping of one bundle-managed view.
type complementDelivery struct {
	// owned lists the names beadle delivered through the file before this
	// sync (BundleState.Complement).
	owned map[string]struct{}
	// delivered lists the names beadle delivers through the file now.
	delivered map[string]struct{}
	// touched lists the keys whose base the view rewrites from the file.
	touched map[string]struct{}
}

// carriesSecret reports whether a canon item references a vault secret. A
// native bundle never carries such a server: everything shipped in a plugin is
// readable by everyone who installs it. This one rule decides both what the
// bundle renders and what the host file must deliver instead.
func carriesSecret(data []byte) bool {
	return bytes.Contains(data, []byte(secretRefMarker))
}

// complementItems returns the canon MCP servers a native bundle leaves out.
func complementItems(items kind.Items) kind.Items {
	out := kind.Items{}

	for name, data := range items {
		if carriesSecret(data) {
			out[name] = data
		}
	}

	return out
}

// bundleManagesMCP reports whether the agent's verified bundle manages its MCP
// kind: the mode is off, yet the host file must still receive what the bundle
// cannot carry (and, off Claude, the plugin-sourced servers), so the surface
// gets a push-only view. Pull never creates one.
func (e *Engine) bundleManagesMCP(st *state.State, agentID string, k kind.ID, opts SyncOptions) bool {
	if k != kind.MCP || opts.Direction == config.ModePull {
		return false
	}

	host, ok := bundleHostFor(agentID)
	if !ok || !slices.Contains(host.Kinds(), kind.MCP) {
		return false
	}

	entry, known := st.Bundles[string(host)]

	return known && entry.Enabled && entry.Verified()
}

// complementOwned returns the names beadle delivered through the agent's file
// on earlier syncs.
func complementOwned(st *state.State, agentID string, k kind.ID) map[string]struct{} {
	owned := map[string]struct{}{}

	host, ok := bundleHostFor(agentID)
	if !ok {
		return owned
	}

	for _, name := range st.Bundles[string(host)].Complement[k] {
		owned[name] = struct{}{}
	}

	return owned
}

// deliverComplement adds the complement to a bundle-managed view: the canon
// servers the bundle cannot carry reach the host file, because the MCP mode is
// off and nothing else delivers them. A copy is written when it is missing or
// still beadle's (it matches the canon or the base); a copy the user edited is
// kept and reported, because a mode-off surface is never a source. A copy
// beadle delivered earlier leaves once the canon drops the server, and only
// while it is untouched; a server the bundle carries now keeps its copy until a
// verified enable withdraws it. Everything else in the file stays as it is.
func (e *Engine) deliverComplement(spec kind.Spec, v *view, vaultItems, desired kind.Items, report *KindReport) kind.Items {
	if !v.bundleManaged {
		return desired
	}

	v.complement.delivered = map[string]struct{}{}
	v.complement.touched = map[string]struct{}{}

	proj := project(complementItems(vaultItems), v.surface)

	for _, key := range slices.Sorted(maps.Keys(proj.items)) {
		data := proj.items[key]

		if v.frozen(spec, key) {
			continue
		}

		if note := e.unresolvedComplement(spec, key, data); note != "" {
			report.Warnings = append(report.Warnings, fmt.Sprintf("mcp %s: %s; not delivered to %s", key, note, v.agent.ID))

			continue
		}

		local := value(v.snap.Items, key)
		if local != nil && !same(local, data) && !same(local, value(v.base, key)) {
			report.Warnings = append(report.Warnings, fmt.Sprintf(
				"mcp %s: the %s copy differs from the canon and is kept; the bundle manages MCP there, so change the server in the vault",
				key, v.agent.ID))

			continue
		}

		desired[key] = data
		v.complement.delivered[key] = struct{}{}
		v.complement.touched[key] = struct{}{}
	}

	for _, name := range slices.Sorted(maps.Keys(v.complement.owned)) {
		if _, canon := vaultItems[name]; canon || v.frozen(spec, name) {
			continue
		}

		local := value(v.snap.Items, name)
		if local != nil && !same(local, value(v.base, name)) {
			continue
		}

		delete(desired, name)
		v.complement.touched[name] = struct{}{}
	}

	return desired
}

// unresolvedComplement returns why one complement server cannot be written, or
// "" when its secrets resolve. It is checked per server, so one missing secret
// holds back that server alone instead of the whole surface write.
func (e *Engine) unresolvedComplement(spec kind.Spec, key string, data []byte) string {
	_, missing, err := e.outbound(spec.ID, kind.Items{key: data})

	switch {
	case err != nil:
		return err.Error()
	case len(missing) > 0:
		return "missing secrets " + strings.Join(missing, ", ")
	default:
		return ""
	}
}

// unchangedComplementForm reports that every complement copy the view writes
// already has its rendered file form: a rotated secret changes only that form.
func (v *view) unchangedComplementForm(resolved kind.Items) bool {
	for key := range v.complement.delivered {
		if !same(resolved[key], v.raw[key]) {
			return false
		}
	}

	return true
}

// managedBase is the base a bundle-managed view records: the previous base,
// with only the keys this view wrote, removed or presented taken from the
// file. The remnants and the user's own servers keep what they had, so a later
// disable (a normal sync again) never mistakes them for beadle's and deletes
// them as dropped from the canon.
func (v *view) managedBase(actual kind.Items) kind.Items {
	out := maps.Clone(v.base)
	if out == nil {
		out = kind.Items{}
	}

	touched := maps.Clone(v.complement.touched)
	if touched == nil {
		touched = map[string]struct{}{}
	}

	for key := range v.presented {
		touched[key] = struct{}{}
	}

	for _, key := range unionKeys(v.snap.Items, actual) {
		if !same(value(v.snap.Items, key), value(actual, key)) {
			touched[key] = struct{}{}
		}
	}

	for key := range touched {
		if data, ok := actual[key]; ok {
			out[key] = data
		} else {
			delete(out, key)
		}
	}

	return out
}

// recordComplement stores the names the agent's bundle-managed views now
// deliver through the file, so a later sync may remove exactly those copies.
// A name counts only when the file holds it after the write.
func (e *Engine) recordComplement(st *state.State, spec kind.Spec, views []*view, actual kind.Items) {
	host, ok := bundleHostFor(views[0].agent.ID)
	if !ok {
		return
	}

	entry, known := st.Bundles[string(host)]
	if !known || !slices.ContainsFunc(views, func(v *view) bool { return v.bundleManaged }) {
		return
	}

	var names []string

	for _, v := range views {
		for name := range v.complement.delivered {
			if _, held := actual[name]; held && !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}

	slices.Sort(names)

	if len(names) == 0 {
		delete(entry.Complement, spec.ID)
	} else {
		if entry.Complement == nil {
			entry.Complement = map[kind.ID][]string{}
		}

		entry.Complement[spec.ID] = names
	}

	if len(entry.Complement) == 0 {
		entry.Complement = nil
	}

	st.Bundles[string(host)] = entry
}

// adoptComplement records the complement copies a verified enable keeps in the
// host file: a canon server the bundle cannot carry stays there, and when the
// copy is beadle's own and untouched (it matches the base) the complement owns
// it from then on, so it leaves the file once the canon drops the server.
func (e *Engine) adoptComplement(ctx context.Context, host bundle.Host, st *state.State, entry *state.BundleState) []string {
	surface := e.bundleSurface(host, kind.MCP)
	if surface == nil || !slices.Contains(host.Kinds(), kind.MCP) {
		return nil
	}

	canon, _, err := e.loadVault(kind.MCP)
	if err != nil {
		return []string{"bundles: " + err.Error()}
	}

	snap, err := surface.Read(ctx)
	if err != nil {
		return []string{fmt.Sprintf("bundles: cannot read the %s mcp surface: %v", host, err)}
	}

	local, _, err := e.inbound(kind.MCP, snap.Items)
	if err != nil {
		return []string{fmt.Sprintf("bundles: cannot read the %s mcp surface: %v", host, err)}
	}

	base, err := e.loadBase(st, kind.MCP, host.AgentID())
	if err != nil {
		return []string{"bundles: " + err.Error()}
	}

	local = normalize(local)

	var names []string

	for _, name := range slices.Sorted(maps.Keys(project(complementItems(canon), surface).items)) {
		if copied := value(local, name); copied != nil && same(copied, value(base, name)) {
			names = append(names, name)
		}
	}

	if len(names) == 0 {
		return nil
	}

	if entry.Complement == nil {
		entry.Complement = map[kind.ID][]string{}
	}

	entry.Complement[kind.MCP] = names

	return nil
}

// complementZeroDeliveryIssues finds the element-level zero delivery the
// kind-level check cannot see: with the host's MCP mode off, a canon server
// the bundle cannot carry reaches the host only through its file.
func (e *Engine) complementZeroDeliveryIssues(ctx context.Context, host bundle.Host, hostName string) []Issue {
	if !slices.Contains(host.Kinds(), kind.MCP) || e.agentMode(host.AgentID(), kind.MCP) != config.ModeOff {
		return nil
	}

	surface := e.bundleSurface(host, kind.MCP)
	if surface == nil {
		return nil
	}

	items, _, err := e.loadVault(kind.MCP)
	if err != nil {
		return nil
	}

	snap, err := surface.Read(ctx)
	if err != nil {
		return nil
	}

	var issues []Issue

	for _, name := range slices.Sorted(maps.Keys(project(complementItems(items), surface).items)) {
		if _, held := snap.Items[name]; held {
			continue
		}

		issues = append(issues, Issue{
			Severity: SeverityError, Kind: kind.MCP, Agent: host.AgentID(),
			Message: fmt.Sprintf("mcp %s is in the canon but neither the %s bundle nor the host config delivers it; run beadle sync", name, hostName),
		})
	}

	return issues
}
