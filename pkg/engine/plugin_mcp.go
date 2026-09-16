package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odiumuniverse/agents-sync/pkg/agent"
	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
)

const (
	pluginMCPFile   = ".mcp.json"
	mcpPluginPrefix = "plugin mcp: "
)

type pluginMCPPlan struct {
	Items    kind.Items
	Owner    map[string]string
	Servers  map[string][]string
	Warnings []string
	Failed   bool
}

func (e *Engine) ownedPluginMCP(spec kind.Spec, views []*view, plan pluginMCPPlan, ledger pluginLedger, vaultItems kind.Items, report *KindReport) map[string]struct{} {
	owned := map[string]struct{}{}

	if spec.ID != kind.MCP {
		return owned
	}

	e.presentPluginMCP(views, plan, report)
	e.collectOwnedServers(plan, ledger, vaultItems, owned)

	if plan.Failed {
		e.holdPluginMCPFailSafe(spec, views, vaultItems, owned, report)
	}

	return owned
}

func (e *Engine) maybePersistPluginMCP(spec kind.Spec, plan pluginMCPPlan, ledger pluginLedger, opts SyncOptions, report *KindReport) {
	if spec.ID != kind.MCP || opts.DryRun || opts.Direction == config.ModePull || plan.Failed {
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

		return emptyPluginLedger(), pluginMCPPlan{Failed: true}
	}

	plan, warns := e.buildPluginMCPPlan(ledger, canonKeys(vaultItems))

	report.Warnings = append(report.Warnings, warns...)

	return ledger, plan
}

func (e *Engine) buildPluginMCPPlan(ledger pluginLedger, canon map[string]struct{}) (pluginMCPPlan, []string) {
	plan := pluginMCPPlan{
		Items:   kind.Items{},
		Owner:   map[string]string{},
		Servers: map[string][]string{},
	}

	if e.home == "" {
		return plan, nil
	}

	var warns []string

	for _, key := range slices.Sorted(maps.Keys(ledger.Plugins)) {
		marketplace, name, ok := strings.Cut(key, "/")
		if !ok || !validPluginKey(marketplace, name) {
			continue
		}

		items, pluginWarns, failed := e.pluginMCPServers(key, ledger.Plugins[key])

		warns = append(warns, pluginWarns...)

		if failed {
			plan.Failed = true
		}

		for _, name := range slices.Sorted(maps.Keys(items)) {
			if _, isCanon := canon[name]; isCanon {
				warns = append(warns, fmt.Sprintf("%sserver %q of %s collides with the vault canon", mcpPluginPrefix, name, key))

				continue
			}

			if owner, taken := plan.Owner[name]; taken {
				warns = append(warns, fmt.Sprintf("%sserver %q of %s is already provided by %s", mcpPluginPrefix, name, key, owner))

				continue
			}

			plan.Owner[name] = key
			plan.Items[name] = items[name]
			plan.Servers[key] = append(plan.Servers[key], name)
		}
	}

	items, _, err := e.inbound(kind.MCP, plan.Items)
	if err != nil {
		plan.Failed = true
		plan.Items = kind.Items{}

		warns = append(warns, mcpPluginPrefix+err.Error())
	} else {
		plan.Items = normalize(items)
	}

	plan.Warnings = warns

	return plan, warns
}

func (e *Engine) pluginMCPServers(key string, rec pluginLedgerRec) (kind.Items, []string, bool) {
	root, targetWarns, ok := e.pluginTargetRoot(key, rec)
	if !ok {
		warns := prefixWarns(mcpPluginPrefix, targetWarns)

		return nil, warns, len(targetWarns) > 0 && !e.pluginCacheReachable()
	}

	path := filepath.Join(rec.Target, pluginMCPFile)

	resolved, err := filepath.EvalSymlinks(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, false
	}

	if err != nil {
		return nil, []string{fmt.Sprintf("%splugin %s mcp config cannot be read: %v", mcpPluginPrefix, key, err)}, true
	}

	if !underDir(resolved, root) {
		return nil, []string{fmt.Sprintf("%splugin %s mcp config resolves outside the plugin cache", mcpPluginPrefix, key)}, true
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, []string{fmt.Sprintf("%splugin %s mcp config cannot be read: %v", mcpPluginPrefix, key, err)}, true
	}

	marketplace, name, _ := strings.Cut(key, "/")

	items, warns, err := agent.PluginMCPServers(data, filepath.Join(e.vault.PluginsDir(), marketplace, name, farmPivotName))
	if err != nil {
		return nil, []string{fmt.Sprintf("%splugin %s mcp config is invalid: %v", mcpPluginPrefix, key, err)}, true
	}

	return items, prefixWarns(mcpPluginPrefix, warns), false
}

func (e *Engine) pluginCacheReachable() bool {
	_, err := filepath.EvalSymlinks(filepath.Join(e.home, ".claude", "plugins"))

	return err == nil
}

func (e *Engine) presentPluginMCP(views []*view, plan pluginMCPPlan, report *KindReport) {
	for _, v := range views {
		proj := project(plan.Items, v.surface)

		v.presented = proj.items
		v.presentedFailed = plan.Failed

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

func (e *Engine) holdPluginMCPFailSafe(spec kind.Spec, views []*view, vaultItems kind.Items, owned map[string]struct{}, report *KindReport) {
	held := 0

	for _, v := range views {
		for key := range v.snap.Items {
			if _, canon := vaultItems[key]; canon || v.frozen(spec, key) {
				continue
			}

			owned[key] = struct{}{}
			held++
		}
	}

	if held > 0 {
		report.Warnings = append(report.Warnings, mcpPluginPrefix+"the plugin state is unavailable; keeping unmanaged MCP servers untouched")
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
