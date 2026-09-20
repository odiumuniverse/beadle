package engine

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/hooks"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/secret"
	"github.com/odiumuniverse/beadle/pkg/state"
)

type BundleResult struct {
	Host       string `json:"host"`
	Action     string `json:"action"`
	Version    string `json:"version,omitempty"`
	Registered bool   `json:"registered,omitempty"`
	Note       string `json:"note,omitempty"`
}

const (
	bundleEnabled   = "enabled"
	bundleGenerated = "generated"
	bundleDisabled  = "disabled"
	bundleFailed    = "failed"
	bundleNoop      = "noop"
	cliPlugin       = "plugin"
)

var (
	bundlesRunner   secret.Runner = secret.ExecRunner{}
	bundlesLookPath               = exec.LookPath
)

func bundleRegisterCommands(host bundle.Host, dir string) [][]string {
	switch host {
	case bundle.Claude:
		return [][]string{
			{cliPlugin, "marketplace", "add", dir},
			{cliPlugin, "install", bundle.PluginName + "@" + bundle.MarketplaceName},
		}
	case bundle.Gemini:
		return [][]string{{"extensions", "link", dir}}
	default:
		return nil
	}
}

func bundleUpdateCommands(host bundle.Host, dir string) [][]string {
	if host == bundle.Claude {
		return [][]string{
			{cliPlugin, "marketplace", "update", bundle.MarketplaceName},
			{cliPlugin, "update", bundle.PluginName + "@" + bundle.MarketplaceName},
		}
	}

	return bundleRegisterCommands(host, dir)
}

func bundleUnregisterCommands(host bundle.Host, dir string) [][]string {
	switch host {
	case bundle.Claude:
		return [][]string{
			{cliPlugin, "uninstall", bundle.PluginName + "@" + bundle.MarketplaceName},
			{cliPlugin, "marketplace", "rm", bundle.MarketplaceName},
		}
	case bundle.Gemini:
		return [][]string{{"extensions", "unlink", dir}}
	default:
		return nil
	}
}

func bundleInstructions(host bundle.Host, dir, home string) string {
	switch host {
	case bundle.Claude:
		return fmt.Sprintf("run: claude plugin marketplace add %s && claude plugin install %s@%s", dir, bundle.PluginName, bundle.MarketplaceName)
	case bundle.Gemini:
		return "run: gemini extensions link " + dir
	default:
		return fmt.Sprintf("link the plugin into the antigravity customization root: ln -s %s %s/.gemini/antigravity-cli/plugins/%s", dir, home, bundle.PluginName)
	}
}

func bundleUnregisterInstructions(host bundle.Host, dir, home string) string {
	switch host {
	case bundle.Claude:
		return fmt.Sprintf("run: claude plugin uninstall %s@%s && claude plugin marketplace rm %s", bundle.PluginName, bundle.MarketplaceName, bundle.MarketplaceName)
	case bundle.Gemini:
		return "run: gemini extensions unlink " + dir
	default:
		return fmt.Sprintf("remove the plugin link: rm %s/.gemini/antigravity-cli/plugins/%s", home, bundle.PluginName)
	}
}

func (e *Engine) BundlesEnable(ctx context.Context, hostName string) (Report, error) {
	var report Report

	release, err := e.lock(ctx)
	if err != nil {
		return report, err
	}

	defer release()

	host, err := bundle.ParseHost(hostName)
	if err != nil {
		return report, err
	}

	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		return report, err
	}

	req, reqWarns, err := e.bundleRequest(host)
	report.Warnings = append(report.Warnings, reqWarns...)

	if err != nil {
		return report, err
	}

	result, err := bundle.Render(e.vault.BundlesDir(), req)
	report.Warnings = append(report.Warnings, result.Warnings...)

	if err != nil {
		return report, err
	}

	dir := filepath.Join(e.vault.BundlesDir(), string(host))
	entry := st.Bundles[string(host)]
	entry.Enabled = true

	action, note := e.registerBundle(host, dir, &entry, result.Version, &report)

	if entry.Registered {
		if err := e.disableBundleKinds(host, &entry); err != nil {
			return report, err
		}
	}

	st.Bundles[string(host)] = entry

	if err := st.Save(e.vault.StatePath()); err != nil {
		return report, err
	}

	version := result.Version
	if note != "" && entry.Version != "" {
		version = entry.Version
	}

	report.Bundles = append(report.Bundles, BundleResult{Host: string(host), Action: action, Version: version, Registered: entry.Registered, Note: note})

	return report, nil
}

func (e *Engine) registerBundle(host bundle.Host, dir string, entry *state.BundleState, version string, report *Report) (string, string) {
	switch {
	case host == bundle.Antigravity:
		entry.Registered = e.antigravityLinked()
		if !entry.Registered {
			report.Warnings = append(report.Warnings, "bundles: antigravity plugin is not linked yet")

			return bundleGenerated, bundleInstructions(host, dir, e.home)
		}

		if entry.Version == version {
			return bundleNoop, ""
		}

		entry.Version = version

		return bundleEnabled, ""
	case e.binaryMissing(host):
		report.Warnings = append(report.Warnings, fmt.Sprintf("bundles: %s CLI not found; register the bundle manually", host.Binary()))

		return bundleGenerated, bundleInstructions(host, dir, e.home)
	case entry.Registered && entry.Version == version:
		return bundleNoop, ""
	case entry.Registered:
		ok, output := e.runBundleCommands(host, bundleUpdateCommands(host, dir))
		if !ok {
			report.Warnings = append(report.Warnings, fmt.Sprintf("bundles: updating %s failed: %s", host, output))

			return bundleEnabled, output
		}

		entry.Version = version

		return bundleEnabled, ""
	default:
		ok, output := e.runBundleCommands(host, bundleRegisterCommands(host, dir))
		entry.Registered = ok

		if !ok {
			report.Warnings = append(report.Warnings, fmt.Sprintf("bundles: registering %s failed: %s", host, output))

			return bundleGenerated, output
		}

		entry.Version = version

		return bundleEnabled, ""
	}
}

func (e *Engine) BundlesDisable(ctx context.Context, hostName string) (Report, error) {
	var report Report

	release, err := e.lock(ctx)
	if err != nil {
		return report, err
	}

	defer release()

	host, err := bundle.ParseHost(hostName)
	if err != nil {
		return report, err
	}

	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		return report, err
	}

	entry, ok := st.Bundles[string(host)]
	if !ok || (!entry.Enabled && !entry.Registered) {
		report.Bundles = append(report.Bundles, BundleResult{Host: string(host), Action: bundleNoop, Note: "no enabled bundle for this host"})

		return report, nil
	}

	dir := filepath.Join(e.vault.BundlesDir(), string(host))

	unregistered, note := e.unregisterBundle(host, dir, entry, &report)

	if !unregistered {
		entry.Enabled = false
		st.Bundles[string(host)] = entry

		if err := st.Save(e.vault.StatePath()); err != nil {
			return report, err
		}

		report.Bundles = append(report.Bundles, BundleResult{Host: string(host), Action: bundleFailed, Version: entry.Version, Registered: entry.Registered, Note: note})

		return report, nil
	}

	if err := e.restoreBundleKinds(host, entry); err != nil {
		return report, err
	}

	delete(st.Bundles, string(host))

	if err := st.Save(e.vault.StatePath()); err != nil {
		return report, err
	}

	report.Bundles = append(report.Bundles, BundleResult{Host: string(host), Action: bundleDisabled, Version: entry.Version, Note: note})

	return report, nil
}

func (e *Engine) unregisterBundle(host bundle.Host, dir string, entry state.BundleState, report *Report) (bool, string) {
	switch {
	case host == bundle.Antigravity:
		if e.antigravityLinked() {
			report.Warnings = append(report.Warnings, "bundles: the antigravity plugin is still linked")

			return false, fmt.Sprintf("remove the plugin link first: rm %s/.gemini/antigravity-cli/plugins/%s, then run beadle bundles disable %s", e.home, bundle.PluginName, host)
		}

		return true, ""
	case e.binaryMissing(host):
		report.Warnings = append(report.Warnings, fmt.Sprintf("bundles: %s CLI not found; unregister the bundle manually", host.Binary()))

		if entry.Registered {
			return false, bundleUnregisterInstructions(host, dir, e.home)
		}

		return true, ""
	case entry.Registered:
		ok, output := e.runBundleCommands(host, bundleUnregisterCommands(host, dir))
		if !ok {
			report.Warnings = append(report.Warnings, fmt.Sprintf("bundles: unregistering %s failed: %s", host, output))

			return false, output
		}
	}

	return true, ""
}

func (e *Engine) binaryMissing(host bundle.Host) bool {
	_, err := bundlesLookPath(host.Binary())

	return err != nil
}

func (e *Engine) antigravityLinked() bool {
	return isDir(filepath.Join(e.home, ".gemini", "antigravity-cli", "plugins", bundle.PluginName))
}

func (e *Engine) runBundleCommands(host bundle.Host, commands [][]string) (bool, string) {
	binary := host.Binary()

	for _, args := range commands {
		stdout, code, err := bundlesRunner.Run(binary, args, nil)
		if err != nil || code != 0 {
			output := strings.TrimSpace(string(stdout))
			if err != nil {
				output = fmt.Sprintf("%v: %s", err, output)
			}

			return false, output
		}
	}

	return true, ""
}

func (e *Engine) bundleRequest(host bundle.Host) (bundle.Request, []string, error) {
	req := bundle.Request{
		Host:     host,
		Skills:   map[string]map[string][]byte{},
		Servers:  kind.Items{},
		Hooks:    map[string]hooks.Hook{},
		Approved: hooks.Approved(e.config),
	}

	var warns []string

	canon, err := hooks.Load(e.vault.HooksPath())
	if err != nil {
		return req, warns, err
	}

	req.Hooks = canon

	if slices.Contains(host.ContentKinds(), kind.Skills) {
		items, _, err := e.loadVault(kind.Skills)
		if err != nil {
			return req, warns, err
		}

		skills, skillWarns := bundleSkills(items)
		warns = append(warns, skillWarns...)
		req.Skills = skills
	}

	if slices.Contains(host.ContentKinds(), kind.MCP) {
		items, _, err := e.loadVault(kind.MCP)
		if err != nil {
			return req, warns, err
		}

		resolved, _, err := e.outbound(kind.MCP, items)
		if err != nil {
			return req, warns, err
		}

		for _, name := range slices.Sorted(maps.Keys(resolved)) {
			if bytes.Contains(resolved[name], []byte("{secret:")) {
				warns = append(warns, fmt.Sprintf("bundles: server %s has unresolved secrets; not rendered", name))

				continue
			}

			req.Servers[name] = resolved[name]
		}
	}

	return req, warns, nil
}

func bundleSkills(items kind.Items) (map[string]map[string][]byte, []string) {
	skills := map[string]map[string][]byte{}

	var warns []string

	for _, key := range slices.Sorted(maps.Keys(items)) {
		name, rel, ok := strings.Cut(key, "/")
		if !ok || !filepath.IsLocal(rel) {
			warns = append(warns, fmt.Sprintf("bundles: skill entry %s is not portable; skipped", key))

			continue
		}

		if skills[name] == nil {
			skills[name] = map[string][]byte{}
		}

		skills[name][rel] = items[key]
	}

	return skills, warns
}

func (e *Engine) refreshBundles(st *state.State, report *Report, opts SyncOptions) {
	if e.home == "" || opts.DryRun || opts.Direction == config.ModePull {
		return
	}

	for _, hostName := range slices.Sorted(maps.Keys(st.Bundles)) {
		entry := st.Bundles[hostName]
		if !entry.Enabled {
			continue
		}

		host, err := bundle.ParseHost(hostName)
		if err != nil {
			report.Warnings = append(report.Warnings, "bundles: "+err.Error())

			continue
		}

		req, warns, err := e.bundleRequest(host)
		report.Warnings = append(report.Warnings, warns...)

		if err != nil {
			report.Warnings = append(report.Warnings, "bundles: "+err.Error())

			continue
		}

		result, err := bundle.Render(e.vault.BundlesDir(), req)
		report.Warnings = append(report.Warnings, result.Warnings...)

		if err != nil {
			report.Warnings = append(report.Warnings, "bundles: "+err.Error())
		}
	}
}

func (e *Engine) disableBundleKinds(host bundle.Host, entry *state.BundleState) error {
	if entry.SavedModes == nil {
		saved := map[kind.ID]config.Mode{}

		for _, k := range host.Kinds() {
			saved[k] = e.agentMode(host.AgentID(), k)
		}

		entry.SavedModes = saved
	}

	for _, k := range host.Kinds() {
		e.config.SetMode(host.AgentID(), k, config.ModeOff)
	}

	return e.config.Save(e.vault.ConfigPath())
}

func (e *Engine) restoreBundleKinds(host bundle.Host, entry state.BundleState) error {
	for k, mode := range entry.SavedModes {
		e.config.SetMode(host.AgentID(), k, mode)
	}

	return e.config.Save(e.vault.ConfigPath())
}

func (e *Engine) agentMode(agentID string, k kind.ID) config.Mode {
	fallback := config.ModeSync

	if a := agent.ByID(e.agents, agentID); a != nil {
		if surface := a.Surface(k); surface != nil {
			fallback = surface.Traits().DefaultMode
		}
	}

	return e.config.ModeFor(agentID, k, fallback)
}

func (e *Engine) validateClaudeBundle(dir string) (string, bool) {
	stdout, code, err := bundlesRunner.Run("claude", []string{cliPlugin, "validate", dir}, nil)

	output := strings.TrimSpace(string(stdout))
	if err != nil {
		output = fmt.Sprintf("%v: %s", err, output)
	}

	if err != nil || code != 0 {
		return output, false
	}

	return output, true
}

func (e *Engine) bundleIssues() []Issue {
	if e.home == "" {
		return nil
	}

	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Message: "cannot read the bundle state: " + err.Error()}}
	}

	var issues []Issue

	if canon, err := hooks.Load(e.vault.HooksPath()); err != nil {
		issues = append(issues, Issue{Severity: SeverityWarn, Message: "cannot read the hooks canon: " + err.Error()})
	} else {
		pending := 0

		for name := range canon {
			if !e.config.HookApproved(name) {
				pending++
			}
		}

		if pending > 0 {
			issues = append(issues, Issue{Severity: SeverityInfo, Message: fmt.Sprintf("%d hook(s) await approval; run beadle hooks approve <name>", pending)})
		}
	}

	for _, hostName := range slices.Sorted(maps.Keys(st.Bundles)) {
		entry := st.Bundles[hostName]
		if !entry.Enabled {
			if entry.Registered {
				issues = append(issues, Issue{Severity: SeverityWarn, Message: fmt.Sprintf("bundle %s is still registered; run beadle bundles disable %s", hostName, hostName)})
			}

			continue
		}

		issues = append(issues, e.bundleHostIssues(hostName, entry)...)
	}

	issues = append(issues, e.validateActiveClaudeBundle(st)...)

	return issues
}

func (e *Engine) validateActiveClaudeBundle(st *state.State) []Issue {
	if _, ok := st.Bundles[string(bundle.Claude)]; !ok {
		return nil
	}

	if _, err := bundlesLookPath("claude"); err != nil {
		return nil
	}

	dir := filepath.Join(e.vault.BundlesDir(), string(bundle.Claude))
	if !isDir(dir) {
		return nil
	}

	if output, ok := e.validateClaudeBundle(dir); !ok {
		return []Issue{{Severity: SeverityError, Message: "claude plugin validate failed: " + output}}
	}

	return nil
}

func (e *Engine) bundleRegistrationIssues(hostName string, host bundle.Host, entry state.BundleState) []Issue {
	dir := filepath.Join(e.vault.BundlesDir(), string(host))

	switch {
	case host == bundle.Antigravity:
		switch {
		case !e.antigravityLinked():
			return []Issue{{Severity: SeverityWarn, Message: fmt.Sprintf("bundle %s is not linked yet; %s", hostName, bundleInstructions(host, dir, e.home))}}
		case !entry.Registered:
			return []Issue{{Severity: SeverityWarn, Message: fmt.Sprintf("bundle %s is linked but not registered; run beadle bundles enable %s", hostName, hostName)}}
		}
	case e.binaryMissing(host):
		return []Issue{{Severity: SeverityWarn, Message: fmt.Sprintf("bundle %s: %s CLI not found; %s", hostName, host.Binary(), bundleInstructions(host, dir, e.home))}}
	case !entry.Registered:
		return []Issue{{Severity: SeverityWarn, Message: fmt.Sprintf("bundle %s is enabled but not registered; run beadle bundles enable %s", hostName, hostName)}}
	}

	return nil
}

func (e *Engine) bundleHostIssues(hostName string, entry state.BundleState) []Issue {
	host, err := bundle.ParseHost(hostName)
	if err != nil {
		return nil
	}

	var issues []Issue

	issues = append(issues, e.bundleRegistrationIssues(hostName, host, entry)...)

	for _, k := range host.Kinds() {
		if entry.Registered && e.agentMode(host.AgentID(), k) != config.ModeOff {
			issues = append(issues, Issue{
				Severity: SeverityError, Kind: k, Agent: host.AgentID(),
				Message: fmt.Sprintf("bundle %s is registered but %s is still synced as files; run beadle bundles enable %s", hostName, k, hostName),
			})
		}
	}

	req, _, err := e.bundleRequest(host)
	if err != nil {
		return append(issues, Issue{Severity: SeverityWarn, Message: "bundles: " + err.Error()})
	}

	fresh, _, err := bundle.Plan(req)
	if err != nil {
		return append(issues, Issue{Severity: SeverityWarn, Message: "bundles: " + err.Error()})
	}

	if entry.Registered && entry.Version != fresh.Version {
		issues = append(issues, Issue{Severity: SeverityWarn, Message: fmt.Sprintf("bundle %s is stale (registered %s, canon %s); run beadle bundles enable %s", hostName, entry.Version, fresh.Version, hostName)})
	}

	return issues
}
