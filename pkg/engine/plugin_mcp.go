package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/plugin"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// bundlePresentsPluginMCP reports whether plugin-sourced servers must still
// reach this agent even though its MCP mode is off: a verified bundle owns
// the canon on that surface, and the host reads plugin servers only through
// beadle. Claude Code is excluded: it loads plugin .mcp.json files natively,
// so presenting them into ~/.claude.json duplicates the server and makes the
// host skip one copy with a warning.
func (e *Engine) bundlePresentsPluginMCP(st *state.State, agentID string, plan pluginMCPPlan, opts SyncOptions) bool {
	if opts.Direction == config.ModePull || len(plan.Items[agentID]) == 0 {
		return false
	}

	for hostName, entry := range st.Bundles {
		if !entry.Verified() {
			continue
		}

		host, err := bundle.ParseHost(hostName)
		if err != nil || host == bundle.Claude || host.AgentID() != agentID || !slices.Contains(host.Kinds(), kind.MCP) {
			continue
		}

		return true
	}

	return false
}

const (
	mcpPluginPrefix = "plugin mcp: "
)

type pluginMCPPlan struct {
	Items     map[string]kind.Items
	Owner     map[string]string
	Servers   map[string][]string
	Warnings  []string
	Failed    map[string]bool
	AllFailed bool
}

func (p pluginMCPPlan) failed(agentID string) bool {
	return p.AllFailed || p.Failed[agentID]
}

func (p pluginMCPPlan) anyFailed() bool {
	return p.AllFailed || len(p.Failed) > 0
}

func (e *Engine) ownedPluginMCP(spec kind.Spec, views []*view, plan pluginMCPPlan, ledger pluginLedger, vaultItems kind.Items, report *KindReport) map[string]struct{} {
	owned := map[string]struct{}{}

	if spec.ID != kind.MCP {
		return owned
	}

	e.presentPluginMCP(views, plan, report)
	e.collectOwnedServers(plan, ledger, vaultItems, owned)
	e.holdMCPFailSafe(spec, views, plan, vaultItems, owned, report)
	markPluginOwnership(views, plan, owned)

	return owned
}

// markPluginOwnership lets mode-off presentation-only views drop servers
// whose plugin is gone.
func markPluginOwnership(views []*view, plan pluginMCPPlan, owned map[string]struct{}) {
	for _, v := range views {
		if !v.pluginOnly || plan.failed(v.agent.ID) {
			continue
		}

		v.pluginOwned = owned
	}
}

func (e *Engine) holdMCPFailSafe(spec kind.Spec, views []*view, plan pluginMCPPlan, vaultItems kind.Items, owned map[string]struct{}, report *KindReport) {
	held := 0

	for _, v := range views {
		if !plan.failed(v.agent.ID) {
			continue
		}

		held += holdViewMCP(spec, v, vaultItems, owned)
	}

	if held > 0 {
		report.Warnings = append(report.Warnings, mcpPluginPrefix+"the plugin state is unavailable; keeping unmanaged MCP servers untouched")
	}
}

func holdViewMCP(spec kind.Spec, v *view, vaultItems kind.Items, owned map[string]struct{}) int {
	held := 0

	for key := range v.snap.Items {
		if _, canon := vaultItems[key]; canon || v.frozen(spec, key) {
			continue
		}

		owned[key] = struct{}{}
		held++
	}

	return held
}

func (e *Engine) maybePersistPluginMCP(spec kind.Spec, plan pluginMCPPlan, ledger pluginLedger, opts SyncOptions, report *KindReport) {
	if spec.ID != kind.MCP || opts.DryRun || opts.Direction == config.ModePull || plan.anyFailed() {
		return
	}

	if err := e.persistPluginOwnership(ledger, plan); err != nil {
		report.Warnings = append(report.Warnings, mcpPluginPrefix+err.Error())
	}
}

func (e *Engine) loadPluginMCPPlan(vaultItems kind.Items, report *KindReport) (pluginLedger, pluginMCPPlan) {
	ledger, _, err := loadPluginLedger(e.vault.PluginsLedgerPath())
	if err != nil {
		report.Warnings = append(report.Warnings, mcpPluginPrefix+err.Error())

		return emptyPluginLedger(), pluginMCPPlan{AllFailed: true}
	}

	plan, warns := e.buildPluginMCPPlan(ledger, canonKeys(vaultItems))

	report.Warnings = append(report.Warnings, warns...)

	return ledger, plan
}

func (e *Engine) buildPluginMCPPlan(ledger pluginLedger, canon map[string]struct{}) (pluginMCPPlan, []string) {
	plan := pluginMCPPlan{
		Items:   map[string]kind.Items{},
		Owner:   map[string]string{},
		Servers: map[string][]string{},
		Failed:  map[string]bool{},
	}

	if e.home == "" {
		return plan, nil
	}

	var warns []string

	dedup := e.pluginDedup(ledger)

	for _, a := range e.agents {
		agentWarns := e.buildPluginMCPAgent(&plan, a.ID, ledger, dedup.Suppressed, canon)

		warns = appendUniqueWarns(warns, agentWarns)
	}

	plan.Warnings = warns

	return plan, warns
}

func (e *Engine) buildPluginMCPAgent(plan *pluginMCPPlan, agentID string, ledger pluginLedger, suppressed map[string]string, canon map[string]struct{}) []string {
	items := kind.Items{}
	servers := map[string][]string{}
	owner := map[string]string{}

	var warns []string

	failed := false

	for _, key := range pluginPresentedKeys(ledger, suppressed) {
		pluginItems, pluginWarns, pluginFailed := e.pluginMCPServers(agentID, key, ledger.Plugins[key])

		warns = append(warns, pluginWarns...)

		failed = failed || pluginFailed

		collectPluginServers(items, owner, servers, canon, key, pluginItems, &warns)
	}

	inboundItems, extracted, err := e.inbound(kind.MCP, items)
	if err != nil {
		failed = true
		items = kind.Items{}

		warns = append(warns, mcpPluginPrefix+err.Error())
	} else {
		items = normalize(inboundItems)

		if extracted {
			warns = append(warns, mcpPluginPrefix+"a plugin-sourced server carried a literal secret value; it moved to the vault secret store, the canon keeps a {secret:…} reference")
		}
	}

	plan.Items[agentID] = items

	if failed {
		plan.Failed[agentID] = true
	}

	for key, names := range servers {
		plan.Servers[key] = unionNames(plan.Servers[key], names)
	}

	for name, key := range owner {
		if _, taken := plan.Owner[name]; !taken {
			plan.Owner[name] = key
		}
	}

	return warns
}

// collectPluginServers adds the servers one plugin offers to the agent plan:
// a canon name wins, then the first plugin by key; the winner keeps the
// ownership record.
func collectPluginServers(items kind.Items, owner map[string]string, servers map[string][]string, canon map[string]struct{}, key string, pluginItems kind.Items, warns *[]string) {
	origin, name, ok := strings.Cut(key, "/")
	if !ok || !validPluginKey(origin, name) {
		return
	}

	for _, server := range slices.Sorted(maps.Keys(pluginItems)) {
		if _, isCanon := canon[server]; isCanon {
			*warns = append(*warns, fmt.Sprintf("%sserver %q of %s collides with the vault canon", mcpPluginPrefix, server, key))

			continue
		}

		if keeper, taken := owner[server]; taken {
			*warns = append(*warns, fmt.Sprintf("%sserver %q of %s is already provided by %s", mcpPluginPrefix, server, key, keeper))

			continue
		}

		owner[server] = key
		items[server] = pluginItems[server]
		servers[key] = append(servers[key], server)
	}
}

func appendUniqueWarns(warns, extra []string) []string {
	seen := make(map[string]struct{}, len(warns))

	for _, warn := range warns {
		seen[warn] = struct{}{}
	}

	for _, warn := range extra {
		if _, ok := seen[warn]; ok {
			continue
		}

		seen[warn] = struct{}{}
		warns = append(warns, warn)
	}

	return warns
}

func (e *Engine) pluginMCPServers(agentID, key string, rec pluginLedgerRec) (kind.Items, []string, bool) {
	if _, pinned := e.config.PluginPin(agentID, key); pinned {
		return e.pinnedPluginMCPServers(agentID, key)
	}

	root, targetWarns, ok := e.pluginTargetRoot(key, rec)
	if !ok {
		warns := prefixWarns(mcpPluginPrefix, targetWarns)

		return nil, warns, len(targetWarns) > 0 && !e.pluginCacheReachable(recSource(rec))
	}

	pivot := e.pluginPivotDir(key)

	return e.readPluginMCPServers(key, recSource(rec), rec.Target, pivot, root)
}

func (e *Engine) pinnedPluginMCPServers(agentID, key string) (kind.Items, []string, bool) {
	pivot, ok := e.pluginPivotFor(agentID, key)
	if !ok {
		return nil, []string{mcpPluginPrefix + e.pinnedPivotNote(agentID, key)}, false
	}

	root, err := filepath.EvalSymlinks(filepath.Join(e.home, ".claude", "plugins"))
	if err != nil {
		return nil, []string{fmt.Sprintf("%splugin %s cache cannot be resolved: %v", mcpPluginPrefix, key, err)}, true
	}

	return e.readPluginMCPServers(key, plugin.SourceClaudeCode, pivot, pivot, root)
}

// readPluginMCPServers decodes the MCP document of one plugin. A file-backed
// document must resolve inside the plugin install root; a manifest-backed
// document (Gemini CLI) is synthesized from the manifest.
func (e *Engine) readPluginMCPServers(key, source, manifestDir, expansionRoot, root string) (kind.Items, []string, bool) {
	data, rel, docWarns, ok := plugin.MCPDocument(source, manifestDir)
	if !ok {
		return nil, prefixWarns(mcpPluginPrefix, docWarns), len(docWarns) > 0
	}

	if rel != "" {
		resolved, err := filepath.EvalSymlinks(filepath.Join(manifestDir, rel))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, false
		}

		if err != nil || !underDir(resolved, root) {
			return nil, []string{fmt.Sprintf("%splugin %s mcp config resolves outside the plugin cache", mcpPluginPrefix, key)}, true
		}
	}

	items, warns, err := agent.PluginMCPServersFor(source, data, expansionRoot)
	if err != nil {
		return nil, []string{fmt.Sprintf("%splugin %s mcp config is invalid: %v", mcpPluginPrefix, key, err)}, true
	}

	return items, prefixWarns(mcpPluginPrefix, warns), false
}

func (e *Engine) presentPluginMCP(views []*view, plan pluginMCPPlan, report *KindReport) {
	for _, v := range views {
		proj := project(plan.Items[v.agent.ID], v.surface)

		v.presented = proj.items
		v.presentedFailed = plan.failed(v.agent.ID)

		for _, key := range slices.Sorted(maps.Keys(proj.hidden)) {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%sserver %q is hidden for %s", mcpPluginPrefix, key, v.agent.ID))
		}
	}
}

func (e *Engine) collectOwnedServers(plan pluginMCPPlan, ledger pluginLedger, vaultItems kind.Items, owned map[string]struct{}) {
	for name := range plan.Owner {
		owned[name] = struct{}{}
	}

	for _, key := range slices.Sorted(maps.Keys(ledger.Plugins)) {
		for _, name := range ledger.Plugins[key].Servers {
			owned[name] = struct{}{}
		}
	}

	for name := range vaultItems {
		delete(owned, name)
	}
}

func (e *Engine) persistPluginOwnership(ledger pluginLedger, plan pluginMCPPlan) error {
	if err := e.vault.EnsureGitIgnore(); err != nil {
		return err
	}

	changed := false

	for _, key := range slices.Sorted(maps.Keys(plan.Servers)) {
		rec, ok := ledger.Plugins[key]
		if !ok {
			continue
		}

		merged := unionNames(rec.Servers, plan.Servers[key])
		if slices.Equal(merged, rec.Servers) {
			continue
		}

		rec.Servers = merged
		ledger.Plugins[key] = rec
		changed = true
	}

	if !changed {
		return nil
	}

	return ledger.save(e.vault.PluginsLedgerPath())
}

func unionNames(lists ...[]string) []string {
	names := map[string]struct{}{}

	for _, list := range lists {
		for _, name := range list {
			names[name] = struct{}{}
		}
	}

	return slices.Sorted(maps.Keys(names))
}

func canonKeys(items kind.Items) map[string]struct{} {
	keys := make(map[string]struct{}, len(items))

	for key := range items {
		keys[key] = struct{}{}
	}

	return keys
}

func prefixWarns(prefix string, warns []string) []string {
	out := make([]string, 0, len(warns))

	for _, warn := range warns {
		out = append(out, prefix+warn)
	}

	return out
}

func pluginLedgerKeys(ledger pluginLedger) []string {
	var out []string

	for _, key := range slices.Sorted(maps.Keys(ledger.Plugins)) {
		if key == bundlePluginKey() {
			continue
		}

		out = append(out, key)
	}

	return out
}

// pluginPresentedKeys lists the ledger keys whose presentation another host's
// copy does not already cover.
func pluginPresentedKeys(ledger pluginLedger, suppressed map[string]string) []string {
	keys := pluginLedgerKeys(ledger)

	return slices.DeleteFunc(keys, func(key string) bool {
		_, covered := suppressed[key]

		return covered
	})
}
