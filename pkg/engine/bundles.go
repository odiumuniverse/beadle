package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/hooks"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/secret"
	"github.com/odiumuniverse/beadle/pkg/state"
)

type BundleResult struct {
	Host       string   `json:"host"`
	Action     string   `json:"action"`
	Version    string   `json:"version,omitempty"`
	Registered bool     `json:"registered,omitempty"`
	Tier       string   `json:"tier,omitempty"`
	Note       string   `json:"note,omitempty"`
	Withdrawn  []string `json:"withdrawn,omitempty"`
	Kept       []string `json:"kept,omitempty"`
	Auto       bool     `json:"auto,omitempty"`
}

const (
	bundleEnabled   = "enabled"
	bundleGenerated = "generated"
	bundleDisabled  = "disabled"
	bundleFailed    = "failed"
	bundlePending   = "pending"
	bundleNoop      = "noop"
	cliPlugin       = "plugin"
	bundleNoteLimit = 400
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
	case bundle.Antigravity:
		return [][]string{
			{cliPlugin, "install", dir},
			{cliPlugin, "enable", bundle.PluginName},
		}
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
	case bundle.Antigravity:
		return [][]string{{cliPlugin, "uninstall", bundle.PluginName}}
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
		return fmt.Sprintf("link the plugin into an antigravity customization root: ln -s %s %s (CLI) or %s (IDE/2.0)",
			dir,
			filepath.Join(home, ".gemini", "antigravity-cli", "plugins", bundle.PluginName),
			filepath.Join(home, ".gemini", "config", "plugins", bundle.PluginName))
	}
}

func bundleUnregisterInstructions(host bundle.Host, dir, home string) string {
	switch host {
	case bundle.Claude:
		return fmt.Sprintf("run: claude plugin uninstall %s@%s && claude plugin marketplace rm %s", bundle.PluginName, bundle.MarketplaceName, bundle.MarketplaceName)
	case bundle.Gemini:
		return "run: gemini extensions unlink " + dir
	default:
		return fmt.Sprintf("remove the plugin link first: %s, %s, then run beadle bundles disable antigravity",
			filepath.Join(home, ".gemini", "antigravity-cli", "plugins", bundle.PluginName),
			filepath.Join(home, ".gemini", "config", "plugins", bundle.PluginName))
	}
}

func bundleRetry(host bundle.Host) string {
	return fmt.Sprintf("run beadle bundles enable %s", host)
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

	if st.BundleOptedOut(string(host)) {
		// The user asks for the bundle again: the unattended attempt may take
		// the host over afterwards.
		st.ClearBundleOptOut(string(host))

		if err := st.Save(e.vault.StatePath()); err != nil {
			return report, err
		}
	}

	if err := e.bundleEnable(ctx, host, st, &report, false); err != nil {
		return report, err
	}

	return report, nil
}

// AutoEnableBundles makes one unattended enable attempt per eligible host: the
// host agent is enabled and detected, and the host was never touched before.
// It is the `beadle init` entry point; a full forward sync calls the same core
// when the engine was built WithBundleAutoEnable.
func (e *Engine) AutoEnableBundles(ctx context.Context) (Report, error) {
	var report Report

	release, err := e.lock(ctx)
	if err != nil {
		return report, err
	}

	defer release()

	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		return report, err
	}

	active, err := e.activeAgents(ctx)
	if err != nil {
		return report, err
	}

	if err := e.autoEnableBundlesInto(ctx, active, st, &report); err != nil {
		return report, err
	}

	return report, nil
}

// autoEnableBundles attempts one unattended enable per host the user has never
// touched: a host with an existing state entry is left alone, so an explicit
// `beadle bundles disable` is never overridden and a failed probe is not
// retried on every sync. It returns the hosts it handled, so the same sync
// does not refresh them twice.
func (e *Engine) autoEnableBundles(ctx context.Context, active []*agent.Agent, st *state.State, report *Report) (map[string]bool, error) {
	if e.home == "" {
		return nil, nil
	}

	handled := map[string]bool{}

	for _, host := range bundle.Hosts() {
		if agent.ByID(active, host.AgentID()) == nil {
			continue
		}

		if !e.autoAttemptDue(host, st) {
			continue
		}

		handled[string(host)] = true

		if err := e.bundleEnable(ctx, host, st, report, true); err != nil {
			return handled, err
		}
	}

	return handled, nil
}

// autoAttemptDue reports whether a host deserves an unattended attempt: only
// a host that was never touched does. An explicit `beadle bundles disable`
// records an opt-out, and any existing entry (a failed probe, a validation
// failure) waits for `beadle bundles enable <host>`.
func (e *Engine) autoAttemptDue(host bundle.Host, st *state.State) bool {
	if st.BundleOptedOut(string(host)) {
		return false
	}

	_, known := st.Bundles[string(host)]

	return !known
}

// autoEnableBundlesInto is the error-only form used by the explicit init
// entry point.
func (e *Engine) autoEnableBundlesInto(ctx context.Context, active []*agent.Agent, st *state.State, report *Report) error {
	_, err := e.autoEnableBundles(ctx, active, st, report)

	return err
}

// bundleEnable runs the R-7 sequence for one host: render → validate →
// register → probe → flip. auto marks an unattended attempt, recorded in the
// state so it is not retried on every sync.
func (e *Engine) bundleEnable(ctx context.Context, host bundle.Host, st *state.State, report *Report, auto bool) error {
	req, cov, notes, reqWarns, err := e.bundleRenderRequest(host)
	report.Notes = append(report.Notes, notes...)
	report.Warnings = append(report.Warnings, reqWarns...)
	report.Warnings = append(report.Warnings, cov.Warnings...)

	if err != nil {
		return err
	}

	result, err := bundle.Render(e.vault.BundlesDir(), req)
	report.Warnings = append(report.Warnings, result.Warnings...)

	if err != nil {
		return err
	}

	dir := filepath.Join(e.vault.BundlesDir(), string(host))

	if note, ok := e.validateBundle(host, dir); !ok {
		report.Bundles = append(report.Bundles, BundleResult{
			Host: string(host), Action: bundleFailed, Version: result.Version, Auto: auto,
			Note: joinBundleNotes(note, bundleRetry(host)),
		})
		report.Warnings = append(report.Warnings, fmt.Sprintf("bundles: the %s bundle did not validate; nothing was registered", host))

		if auto {
			// Record the attempt even though nothing was registered: the
			// eligibility gate keys on the state entry, so without it every
			// full sync would render and re-validate the same broken bundle.
			entry := st.Bundles[string(host)]
			entry.Enabled = false
			entry.VerifyTier = state.VerifyFailed
			entry.ProbeNote = note
			entry.AutoAttempt = &state.AutoAttempt{Version: result.Version, At: e.now().UTC(), Tier: state.VerifyFailed}
			st.Bundles[string(host)] = entry

			if err := st.Save(e.vault.StatePath()); err != nil {
				return err
			}
		}

		return nil
	}

	entry := st.Bundles[string(host)]
	entry.Enabled = true

	action, note := e.registerBundle(host, dir, &entry, result.Version, report)

	tier, probeNote := e.bundleVerification(host, dir, result.Version, entry.Registered)
	entry.VerifyTier = tier
	entry.ProbeNote = probeNote

	if auto {
		entry.AutoAttempt = &state.AutoAttempt{Version: result.Version, At: e.now().UTC(), Tier: tier}
	} else {
		syncAttemptTier(&entry)
	}

	if entry.Registered && tier == state.VerifyExecuted {
		return e.enableVerifiedBundle(ctx, host, st, req, cov, entry, result, action, note, auto, report)
	}

	st.Bundles[string(host)] = entry

	if err := st.Save(e.vault.StatePath()); err != nil {
		return err
	}

	if tier == state.VerifyFailed {
		action = bundleFailed
		note = joinBundleNotes(note, probeNote, bundleRetry(host))
	} else if note == "" {
		note = probeNote
	}

	report.Bundles = append(report.Bundles, BundleResult{
		Host: string(host), Action: action, Version: reportedVersion(entry, result),
		Registered: entry.Registered, Tier: tier, Note: note, Auto: auto,
	})

	return nil
}

// reportedVersion prefers the version the host actually serves over the
// freshly rendered one, so a failed update does not claim the new version.
func reportedVersion(entry state.BundleState, result bundle.Result) string {
	if entry.Version != "" {
		return entry.Version
	}

	return result.Version
}

func (e *Engine) enableVerifiedBundle(
	ctx context.Context, host bundle.Host, st *state.State, req bundle.Request, cov coverage,
	entry state.BundleState, result bundle.Result, action, note string, auto bool, report *Report,
) error {
	plans, kept, warnList := e.planBundleWithdrawal(ctx, host, st, req, cov)
	report.Warnings = append(report.Warnings, warnList...)

	if auto {
		warnKeptCopies(host, kept, report)
	}

	planned := plannedWithdrawn(plans)

	// Checkpoint the plan before anything is flipped or deleted: a crash or
	// a failed write must leave enough state for disable to restore.
	entry.PendingWithdrawal = true
	entry.Withdrawn = mergeWithdrawn(entry.Withdrawn, planned)
	st.Bundles[string(host)] = entry

	if err := st.Save(e.vault.StatePath()); err != nil {
		return err
	}

	if err := e.disableBundleKinds(host, &entry, report); err != nil {
		return err
	}

	withdrawn, applyWarns, complete := e.applyBundleWithdrawal(ctx, plans)
	report.Warnings = append(report.Warnings, applyWarns...)

	entry.PendingWithdrawal = !complete
	entry.Withdrawn = mergeWithdrawn(entry.Withdrawn, withdrawn)

	st.Bundles[string(host)] = entry

	if err := st.Save(e.vault.StatePath()); err != nil {
		return err
	}

	report.Bundles = append(report.Bundles, BundleResult{
		Host: string(host), Action: action, Version: result.Version, Registered: true,
		Tier: entry.VerifyTier, Note: note, Withdrawn: withdrawNames(withdrawn), Kept: kept, Auto: auto,
	})

	return nil
}

func (e *Engine) registerBundle(host bundle.Host, dir string, entry *state.BundleState, version string, report *Report) (string, string) {
	switch {
	case host == bundle.Antigravity && e.binaryMissing(host):
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

func (e *Engine) bundleVerification(host bundle.Host, dir, version string, registered bool) (string, string) {
	switch {
	case registered:
		return e.probeBundle(host, dir, version)
	case host == bundle.Antigravity && e.binaryMissing(host):
		// Antigravity linking without the CLI is manual by design.
		return state.VerifyUnverifiable, "the antigravity plugin is not linked yet"
	case e.binaryMissing(host):
		return state.VerifyUnverifiable, host.Binary() + " CLI not found; the bundle stays unverified"
	default:
		return state.VerifyFailed, "the registration command failed"
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
		return e.bundleDisableNoop(host, st, &report)
	}

	dir := filepath.Join(e.vault.BundlesDir(), string(host))

	restored, restoreNotes, restoreWarns, err := e.rematerializeBundle(ctx, host, entry)
	report.Notes = append(report.Notes, restoreNotes...)
	report.Warnings = append(report.Warnings, restoreWarns...)

	if err != nil {
		report.Bundles = append(report.Bundles, BundleResult{
			Host: string(host), Action: bundleFailed, Version: entry.Version,
			Registered: entry.Registered, Tier: entry.VerifyTier,
			Note: joinBundleNotes(err.Error(), fmt.Sprintf("run beadle bundles disable %s again", host)),
		})

		return report, nil
	}

	unregistered, note := e.unregisterBundle(host, dir, entry, &report)

	if !unregistered {
		entry.Enabled = false
		st.Bundles[string(host)] = entry
		st.OptOutBundle(string(host))

		if err := st.Save(e.vault.StatePath()); err != nil {
			return report, err
		}

		report.Bundles = append(report.Bundles, BundleResult{Host: string(host), Action: bundleFailed, Version: entry.Version, Registered: entry.Registered, Tier: entry.VerifyTier, Note: note})

		return report, nil
	}

	if err := e.restoreBundleKinds(host, &entry, &report); err != nil {
		return report, err
	}

	delete(st.Bundles, string(host))

	st.OptOutBundle(string(host))

	if err := st.Save(e.vault.StatePath()); err != nil {
		return report, err
	}

	report.Bundles = append(report.Bundles, BundleResult{
		Host: string(host), Action: bundleDisabled, Version: entry.Version,
		Registered: entry.Registered, Tier: entry.VerifyTier,
		Note: joinBundleNotes(note, restoredNote(restored)),
	})

	return report, nil
}

// bundleDisableNoop records the explicit opt-out when there is nothing to
// unregister: the unattended attempt must not enable the host afterwards (the
// entry may be missing because the agent appeared after init).
func (e *Engine) bundleDisableNoop(host bundle.Host, st *state.State, report *Report) (Report, error) {
	if !st.BundleOptedOut(string(host)) {
		st.OptOutBundle(string(host))

		if err := st.Save(e.vault.StatePath()); err != nil {
			return *report, err
		}
	}

	report.Bundles = append(report.Bundles, BundleResult{Host: string(host), Action: bundleNoop, Note: "no enabled bundle for this host; it stays opted out"})

	return *report, nil
}

func restoredNote(restored []string) string {
	if len(restored) == 0 {
		return ""
	}

	return "restored: " + strings.Join(restored, ", ")
}

func (e *Engine) unregisterBundle(host bundle.Host, dir string, entry state.BundleState, report *Report) (bool, string) {
	switch {
	case host == bundle.Antigravity && e.binaryMissing(host):
		if e.antigravityLinked() {
			report.Warnings = append(report.Warnings, "bundles: the antigravity plugin is still linked")

			return false, bundleUnregisterInstructions(host, dir, e.home)
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

	if host == bundle.Antigravity && e.antigravityLinked() {
		report.Warnings = append(report.Warnings, "bundles: the antigravity plugin is still linked")

		return false, bundleUnregisterInstructions(host, dir, e.home)
	}

	return true, ""
}

func (e *Engine) binaryMissing(host bundle.Host) bool {
	_, err := bundlesLookPath(host.Binary())

	return err != nil
}

func (e *Engine) antigravityLinks() []string {
	return []string{
		filepath.Join(e.home, ".gemini", "antigravity-cli", "plugins", bundle.PluginName),
		filepath.Join(e.home, ".gemini", "config", "plugins", bundle.PluginName),
	}
}

func (e *Engine) antigravityLinked() bool {
	return slices.ContainsFunc(e.antigravityLinks(), isDir)
}

func (e *Engine) runBundleCommands(host bundle.Host, commands [][]string) (bool, string) {
	binary := host.Binary()

	for _, args := range commands {
		stdout, code, err := bundlesRunner.Run(binary, args, nil)
		if err != nil || code != 0 {
			return false, runOutput(err, stdout)
		}
	}

	return true, ""
}

// runOutput renders one runner result without leaking more than the caller
// asked for.
func runOutput(err error, stdout []byte) string {
	output := strings.TrimSpace(string(stdout))
	if err != nil {
		output = fmt.Sprintf("%v: %s", err, output)
	}

	return truncateNote(output)
}

func truncateNote(note string) string {
	note = strings.TrimSpace(note)
	if len(note) <= bundleNoteLimit {
		return note
	}

	cut := bundleNoteLimit
	for cut > 0 && !utf8.RuneStart(note[cut]) {
		cut--
	}

	return note[:cut] + "…"
}

func joinBundleNotes(notes ...string) string {
	var kept []string

	for _, note := range notes {
		if note = strings.TrimSpace(note); note != "" {
			kept = append(kept, note)
		}
	}

	return strings.Join(kept, "; ")
}

// secretRefMarker is the canon syntax of a secret reference. A server whose
// configuration carries one never enters a native bundle: the bundle is a
// distributable package, while the host MCP surface delivers the same server
// with resolved values.
const secretRefMarker = "{secret:"

func (e *Engine) bundleRequest(host bundle.Host) (bundle.Request, []string, []string, error) {
	req := bundle.Request{
		Host:     host,
		Skills:   map[string]map[string][]byte{},
		Servers:  kind.Items{},
		Hooks:    map[string]hooks.Hook{},
		Approved: hooks.Approved(e.config),
	}

	var (
		notes []string
		warns []string
	)

	canon, err := hooks.Load(e.vault.HooksPath())
	if err != nil {
		return req, notes, warns, err
	}

	req.Hooks = e.renderableHooks(host, canon)

	if slices.Contains(host.ContentKinds(), kind.Skills) {
		items, _, err := e.loadVault(kind.Skills)
		if err != nil {
			return req, notes, warns, err
		}

		skills, skillWarns := bundleSkills(items)
		warns = append(warns, skillWarns...)
		req.Skills = skills
	}

	if slices.Contains(host.ContentKinds(), kind.MCP) {
		items, _, err := e.loadVault(kind.MCP)
		if err != nil {
			return req, notes, warns, err
		}

		items, skipped := splitSecretBearingServers(items)
		for _, name := range skipped {
			notes = append(notes, fmt.Sprintf("mcp %s carries secrets; delivered via the host config, not the %s bundle", name, host))
		}

		resolved, _, err := e.outbound(kind.MCP, items)
		if err != nil {
			return req, notes, warns, err
		}

		for _, name := range slices.Sorted(maps.Keys(resolved)) {
			req.Servers[name] = resolved[name]
		}
	}

	return req, notes, warns, nil
}

// splitSecretBearingServers keeps the canon servers a native bundle may carry.
// A server whose configuration references a secret is skipped: everything
// shipped in a plugin is readable by everyone who installs it, so the value
// stays out of the bundle and reaches the hosts through their MCP surfaces.
func splitSecretBearingServers(items kind.Items) (kind.Items, []string) {
	kept := kind.Items{}

	var skipped []string

	for name, data := range items {
		if bytes.Contains(data, []byte(secretRefMarker)) {
			skipped = append(skipped, name)

			continue
		}

		kept[name] = data
	}

	slices.Sort(skipped)

	return kept, skipped
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

	for _, name := range slices.Sorted(maps.Keys(skills)) {
		if _, ok := skills[name][farmSkillFile]; ok {
			continue
		}

		warns = append(warns, fmt.Sprintf("bundles: skill %s has no root %s; not rendered", name, farmSkillFile))

		delete(skills, name)
	}

	return skills, warns
}

func fullForwardSync(opts SyncOptions) bool {
	return (opts.Direction == "" || opts.Direction == config.ModeSync) && len(opts.Kinds) == 0
}

func (e *Engine) refreshBundles(ctx context.Context, st *state.State, report *Report, opts SyncOptions, skip map[string]bool) {
	if e.home == "" || opts.DryRun || !fullForwardSync(opts) {
		return
	}

	for _, hostName := range slices.Sorted(maps.Keys(st.Bundles)) {
		if skip[hostName] {
			continue
		}

		e.refreshBundle(st, report, hostName)
	}
}

// refreshBundle renders one enabled host and updates it when the rendered
// version moved.
func (e *Engine) refreshBundle(st *state.State, report *Report, hostName string) {
	entry := st.Bundles[hostName]
	if !entry.Enabled {
		return
	}

	host, err := bundle.ParseHost(hostName)
	if err != nil {
		report.Warnings = append(report.Warnings, "bundles: "+err.Error())

		return
	}

	req, cov, notes, warns, err := e.bundleRenderRequest(host)
	report.Notes = append(report.Notes, notes...)
	report.Warnings = append(report.Warnings, warns...)
	report.Warnings = append(report.Warnings, cov.Warnings...)

	if err != nil {
		report.Warnings = append(report.Warnings, "bundles: "+err.Error())

		return
	}

	result, err := bundle.Render(e.vault.BundlesDir(), req)
	report.Warnings = append(report.Warnings, result.Warnings...)

	if err != nil {
		report.Warnings = append(report.Warnings, "bundles: "+err.Error())

		return
	}

	if entry.AutoAttempt != nil && entry.VerifyTier != state.VerifyExecuted && entry.Version == result.Version {
		// The unattended attempt never verified: refresh only when the
		// rendered bundle actually changes, so a failed or unverifiable
		// probe is not retried on every sync. The current tier decides (an
		// explicit enable or a successful update lifts the hold), not the
		// snapshot the first attempt left behind. The retry is explicit:
		// `beadle bundles enable <host>`.
		return
	}

	if !entry.Registered {
		return
	}

	e.refreshBundleHost(host, dirOf(e.vault.BundlesDir(), host), entry, result, st, report)
}

func (e *Engine) refreshBundleHost(host bundle.Host, dir string, entry state.BundleState, result bundle.Result, st *state.State, report *Report) {
	if entry.Version == result.Version {
		// The render is back at the served version: nothing waits anymore.
		if entry.Pending != nil {
			entry.Pending = nil
			st.Bundles[string(host)] = entry
		}

		if entry.VerifyTier == state.VerifyExecuted {
			return
		}

		tier, note := e.probeBundle(host, dir, result.Version)
		entry.VerifyTier, entry.ProbeNote = tier, note
		syncAttemptTier(&entry)
		st.Bundles[string(host)] = entry

		if tier == state.VerifyFailed {
			report.Warnings = append(report.Warnings, fmt.Sprintf("bundles: %s bundle probe failed: %s; %s", host, note, bundleRetry(host)))
		}

		return
	}

	if e.binaryMissing(host) {
		e.deferBundleRefresh(host, entry, result.Version, st, report)

		return
	}

	// This run reaches the CLI, so it takes whatever waited: its outcome is
	// the new truth, a failure included.
	entry.Pending = nil

	if note, ok := e.validateBundle(host, dir); !ok {
		entry.VerifyTier = state.VerifyFailed
		entry.ProbeNote = note
		syncAttemptTier(&entry)
		st.Bundles[string(host)] = entry

		report.Warnings = append(report.Warnings, fmt.Sprintf("bundles: the %s bundle did not validate; %s", host, bundleRetry(host)))
		report.Bundles = append(report.Bundles, BundleResult{Host: string(host), Action: bundleFailed, Version: entry.Version, Registered: true, Tier: entry.VerifyTier, Note: note})

		return
	}

	ok, output := e.runBundleCommands(host, bundleUpdateCommands(host, dir))
	if !ok {
		entry.VerifyTier = state.VerifyFailed
		entry.ProbeNote = truncateNote(output)
		syncAttemptTier(&entry)
		st.Bundles[string(host)] = entry

		report.Warnings = append(report.Warnings, fmt.Sprintf("bundles: updating %s failed: %s; %s", host, output, bundleRetry(host)))
		report.Bundles = append(report.Bundles, BundleResult{Host: string(host), Action: bundleFailed, Version: entry.Version, Registered: true, Tier: entry.VerifyTier, Note: output})

		return
	}

	tier, note := e.probeBundle(host, dir, result.Version)

	entry.Version = result.Version
	entry.VerifyTier = tier
	entry.ProbeNote = note

	syncAttemptTier(&entry)

	st.Bundles[string(host)] = entry

	if tier != state.VerifyExecuted {
		report.Warnings = append(report.Warnings, fmt.Sprintf("bundles: %s bundle updated to %s but not verified: %s; %s", host, result.Version, note, bundleRetry(host)))
	}

	report.Bundles = append(report.Bundles, BundleResult{Host: string(host), Action: bundleEnabled, Version: result.Version, Registered: true, Tier: tier, Note: note})
}

// deferBundleRefresh handles a refresh the syncing process cannot run because
// the host CLI is out of its reach (a service runs without the user's shell
// PATH). Per the contract a missing CLI is unverifiable, never failed: nothing
// is validated or run, Version and VerifyTier keep describing what the host
// still serves, and the rendered version waits in Pending until a run that
// reaches the CLI takes it. The warning fires once per rendered version.
func (e *Engine) deferBundleRefresh(host bundle.Host, entry state.BundleState, version string, st *state.State, report *Report) {
	if entry.Pending != nil && entry.Pending.Version == version {
		return
	}

	entry.Pending = &state.PendingRefresh{Version: version, Since: e.now().UTC()}
	st.Bundles[string(host)] = entry

	note := host.Binary() + " CLI not found"

	report.Warnings = append(report.Warnings, fmt.Sprintf(
		"bundles: %s; the %s host keeps serving %s until a run that reaches the CLI delivers %s; %s",
		note, host, entry.Version, version, pendingRetry(host)))
	report.Bundles = append(report.Bundles, BundleResult{
		Host: string(host), Action: bundlePending, Version: version, Registered: true, Tier: state.VerifyUnverifiable, Note: note,
	})
}

// pendingRetry is the remedy for a deferred refresh: any run that reaches the
// host CLI takes the waiting version.
func pendingRetry(host bundle.Host) string {
	return fmt.Sprintf("run beadle sync from a shell that has %s on PATH", host.Binary())
}

// syncAttemptTier keeps the attempt record honest: once a probe (explicit or
// refreshed) knows the current tier, the record follows it instead of the
// snapshot the first unattended attempt left behind.
func syncAttemptTier(entry *state.BundleState) {
	if entry.AutoAttempt != nil {
		entry.AutoAttempt.Tier = entry.VerifyTier
	}
}

func dirOf(root string, host bundle.Host) string {
	return filepath.Join(root, string(host))
}

func (e *Engine) disableBundleKinds(host bundle.Host, entry *state.BundleState, report *Report) error {
	if entry.SavedModes == nil {
		saved := map[kind.ID]config.Mode{}

		for _, k := range host.Kinds() {
			mode := e.agentMode(host.AgentID(), k)
			if mode == config.ModeOff {
				mode = e.surfaceDefaultMode(host.AgentID(), k)
				report.Warnings = append(report.Warnings, fmt.Sprintf(
					"bundles: restoring the default %s mode %q for %s (the previous off is indistinguishable from bundle-managed)", k, mode, host))
			}

			saved[k] = mode
		}

		entry.SavedModes = saved
	}

	for _, k := range host.Kinds() {
		e.config.SetMode(host.AgentID(), k, config.ModeOff)
	}

	return e.config.Save(e.vault.ConfigPath())
}

func (e *Engine) restoreBundleKinds(host bundle.Host, entry *state.BundleState, report *Report) error {
	for _, k := range host.Kinds() {
		mode, ok := entry.SavedModes[k]
		if !ok || mode == config.ModeOff {
			mode = e.surfaceDefaultMode(host.AgentID(), k)
			report.Warnings = append(report.Warnings, fmt.Sprintf(
				"bundles: restoring the default %s mode %q for %s (the saved mode was off and is indistinguishable from bundle-managed)", k, mode, host))
		}

		e.config.SetMode(host.AgentID(), k, mode)
	}

	return e.config.Save(e.vault.ConfigPath())
}

func (e *Engine) agentMode(agentID string, k kind.ID) config.Mode {
	return e.config.ModeFor(agentID, k, e.surfaceDefaultMode(agentID, k))
}

func (e *Engine) surfaceDefaultMode(agentID string, k kind.ID) config.Mode {
	if a := agent.ByID(e.agents, agentID); a != nil {
		if surface := a.Surface(k); surface != nil {
			return surface.Traits().DefaultMode
		}
	}

	return config.ModeSync
}

type validationIssue struct {
	Message string `json:"message"`
}

type validationContent struct {
	Type   string            `json:"type"`
	Errors []validationIssue `json:"errors"`
}

type validationReport struct {
	Success  bool `json:"success"`
	Manifest struct {
		Errors []validationIssue `json:"errors"`
	} `json:"manifest"`
	Contents []validationContent `json:"contents"`
}

func (e *Engine) validateBundle(host bundle.Host, dir string) (string, bool) {
	if host != bundle.Claude || e.binaryMissing(host) {
		return "", true
	}

	return e.validateClaudeBundle(dir)
}

func (e *Engine) validateClaudeBundle(dir string) (string, bool) {
	// The plugin directory is what Claude Code loads; the marketplace root is
	// validated as well, because `claude plugin validate` does not descend
	// into its plugins on its own.
	for _, target := range []string{filepath.Join(dir, "plugins", bundle.PluginName), dir} {
		stdout, code, err := bundlesRunner.Run("claude", []string{cliPlugin, "validate", "--json", "--strict", target}, nil)

		message, valid := parseClaudeValidation(stdout)

		switch {
		case !valid:
			if message == "" {
				message = "claude plugin validate reported a failure"
			}

			return message, false
		case err != nil || code != 0:
			return runOutput(err, stdout), false
		}
	}

	return "", true
}

func parseClaudeValidation(stdout []byte) (string, bool) {
	var report validationReport

	if err := json.Unmarshal(stdout, &report); err != nil {
		return truncateNote("claude plugin validate output is not parseable: " + err.Error()), false
	}

	if report.Success {
		return "", true
	}

	var messages []string

	for _, issue := range report.Manifest.Errors {
		messages = append(messages, issue.Message)
	}

	for _, content := range report.Contents {
		for _, issue := range content.Errors {
			messages = append(messages, fmt.Sprintf("%s: %s", content.Type, issue.Message))
		}
	}

	if len(messages) == 0 {
		messages = append(messages, "claude plugin validate reported a failure")
	}

	return truncateNote(strings.Join(messages, "; ")), false
}

func (e *Engine) bundleIssues(ctx context.Context) []Issue {
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

	for _, host := range bundle.Hosts() {
		hostName := string(host)
		entry, exists := st.Bundles[hostName]

		issues = append(issues, e.bundleHostStateIssues(ctx, st, host, hostName, entry, exists)...)
	}

	issues = append(issues, e.validateActiveClaudeBundle(st)...)

	return issues
}

func (e *Engine) bundleHostStateIssues(ctx context.Context, st *state.State, host bundle.Host, hostName string, entry state.BundleState, exists bool) []Issue {
	issues := e.bundleZeroDeliveryIssues(host, hostName, entry, exists)

	if !exists {
		return issues
	}

	if !entry.Enabled {
		if entry.Registered {
			issues = append(issues, Issue{Severity: SeverityWarn, Message: fmt.Sprintf("bundle %s is still registered; run beadle bundles disable %s", hostName, hostName)})
		}

		if entry.AutoAttempt != nil && entry.AutoAttempt.Tier != state.VerifyExecuted && !st.BundleOptedOut(hostName) {
			issues = append(issues, Issue{Severity: SeverityInfo, Message: fmt.Sprintf("bundle %s did not verify after the automatic attempt (%s); %s", hostName, entry.AutoAttempt.Tier, bundleRetry(host))})
		}

		return issues
	}

	issues = append(issues, e.bundleRegistrationIssues(hostName, host, entry)...)

	req, cov, notes, _, err := e.bundleRenderRequest(host)
	if err != nil {
		return append(issues, Issue{Severity: SeverityWarn, Message: "bundles: " + err.Error()})
	}

	for _, note := range notes {
		issues = append(issues, Issue{Severity: SeverityInfo, Message: note})
	}

	issues = append(issues, coverageIssues(host, cov)...)

	fresh, _, err := bundle.Plan(req)
	if err != nil {
		return append(issues, Issue{Severity: SeverityWarn, Message: "bundles: " + err.Error()})
	}

	if entry.Registered && entry.Version != fresh.Version {
		issues = append(issues, Issue{Severity: SeverityWarn, Message: staleBundleMessage(host, hostName, entry, fresh.Version)})
	}

	issues = append(issues, e.bundlePresentationIssues(ctx, st, host, hostName, entry, req)...)

	return issues
}

func (e *Engine) bundleZeroDeliveryIssues(host bundle.Host, hostName string, entry state.BundleState, exists bool) []Issue {
	if exists && entry.Serves() {
		// A registered bundle may still serve its last installed copy even
		// when the fresh probe failed: the presentation issues warn about it.
		return nil
	}

	var issues []Issue

	for _, k := range host.Kinds() {
		if e.agentMode(host.AgentID(), k) != config.ModeOff {
			continue
		}

		issues = append(issues, Issue{
			Severity: SeverityError, Kind: k, Agent: host.AgentID(),
			Message: e.zeroDeliveryMessage(host, hostName, k),
		})
	}

	return issues
}

func (e *Engine) zeroDeliveryMessage(host bundle.Host, hostName string, k kind.ID) string {
	if e.binaryMissing(host) {
		return fmt.Sprintf("bundle %s is off for %s and nothing delivers the canon; install the %s CLI and %s, or restore the %s mode",
			hostName, k, host.Binary(), bundleRetry(host), k)
	}

	return fmt.Sprintf("bundle %s is off for %s and nothing delivers the canon; %s", hostName, k, bundleRetry(host))
}

func (e *Engine) bundlePresentationIssues(ctx context.Context, st *state.State, host bundle.Host, hostName string, entry state.BundleState, req bundle.Request) []Issue {
	if !entry.Registered {
		return nil
	}

	var issues []Issue

	switch entry.VerifyTier {
	case state.VerifyExecuted:
		for _, k := range host.Kinds() {
			if e.agentMode(host.AgentID(), k) != config.ModeOff {
				issues = append(issues, Issue{
					Severity: SeverityError, Kind: k, Agent: host.AgentID(),
					Message: fmt.Sprintf("bundle %s is registered but %s is still synced as files; %s", hostName, k, bundleRetry(host)),
				})
			}
		}

		alive, readIssues := e.canonAliveInSurface(ctx, st, host, req)
		issues = append(issues, readIssues...)

		for _, element := range alive {
			issues = append(issues, Issue{
				Severity: SeverityWarn, Kind: element.kind, Agent: host.AgentID(),
				Message: fmt.Sprintf("bundle %s is verified but %s %s is still present in the file surface (double delivery); %s",
					hostName, element.kind, element.name, bundleRetry(host)),
			})
		}

		if entry.PendingWithdrawal {
			issues = append(issues, Issue{Severity: SeverityWarn, Message: fmt.Sprintf(
				"bundle %s left a withdrawal unfinished; %s", hostName, bundleRetry(host))})
		}
	default:
		issues = append(issues, Issue{Severity: SeverityWarn, Message: e.unverifiedBundleMessage(host, hostName, entry)})
	}

	return issues
}

// staleBundleMessage explains why the host serves an older version than the
// canon renders: a sync has not run yet, or runs could not reach the host CLI.
func staleBundleMessage(host bundle.Host, hostName string, entry state.BundleState, fresh string) string {
	message := fmt.Sprintf("bundle %s is stale (registered %s, canon %s)", hostName, entry.Version, fresh)

	if entry.Pending == nil {
		return message + "; run beadle sync"
	}

	return fmt.Sprintf("%s: the syncing process could not reach the %s CLI since %s (a service runs without your shell PATH); %s",
		message, host.Binary(), entry.Pending.Since.Format(time.RFC3339), pendingRetry(host))
}

// unverifiedBundleMessage describes a registered bundle whose last probe did
// not execute, from the file modes as they are: an enable that never verified
// left them on, while a refresh that failed after a verified enable finds them
// off and the host on its last installed copy.
func (e *Engine) unverifiedBundleMessage(host bundle.Host, hostName string, entry state.BundleState) string {
	var off []string

	for _, k := range host.Kinds() {
		if e.agentMode(host.AgentID(), k) == config.ModeOff {
			off = append(off, string(k))
		}
	}

	delivery := "modes left on"
	if len(off) > 0 {
		delivery = fmt.Sprintf("the %s file modes are off, so the host keeps serving its last installed copy (%s)",
			strings.Join(off, ", "), entry.Version)
	}

	message := fmt.Sprintf("bundle %s is registered but unverified, %s; %s", hostName, delivery, bundleRetry(host))
	if entry.ProbeNote != "" {
		message += " (" + entry.ProbeNote + ")"
	}

	return message
}

type aliveElement struct {
	kind kind.ID
	name string
}

// canonAliveInSurface lists the canon elements a verified enable would
// actually withdraw but that are still present: writable items (never a
// symlink or another read-only copy) that beadle owns according to the
// state base. Read-only copies and unmanaged directories are not double
// delivery — enable leaves them alone by design.
func (e *Engine) canonAliveInSurface(ctx context.Context, st *state.State, host bundle.Host, req bundle.Request) ([]aliveElement, []Issue) {
	var (
		alive  []aliveElement
		issues []Issue
	)

	for _, k := range host.Kinds() {
		surface := e.bundleSurface(host, k)
		if surface == nil {
			continue
		}

		snap, err := surface.Read(ctx)
		if err != nil {
			issues = append(issues, Issue{
				Severity: SeverityWarn, Kind: k, Agent: host.AgentID(),
				Message: fmt.Sprintf("bundles: cannot read the %s surface: %v", k, err),
			})

			continue
		}

		items, _ := writableItems(snap, k)

		base, _ := st.Base(k, host.AgentID())

		for _, name := range e.canonNames(k, req) {
			if !baseOwns(base, k, name) {
				continue
			}

			if surfaceHasName(items, k, name) {
				alive = append(alive, aliveElement{kind: k, name: name})
			}
		}
	}

	return alive, issues
}

func (e *Engine) canonNames(k kind.ID, req bundle.Request) []string {
	if k == kind.Skills {
		return slices.Sorted(maps.Keys(req.Skills))
	}

	return slices.Sorted(maps.Keys(req.Servers))
}

func surfaceHasName(items kind.Items, k kind.ID, name string) bool {
	if k == kind.Skills {
		prefix := name + "/"

		for key := range items {
			if strings.HasPrefix(key, prefix) {
				return true
			}
		}

		return false
	}

	_, ok := items[name]

	return ok
}

func (e *Engine) bundleSurface(host bundle.Host, k kind.ID) agent.Surface {
	a := agent.ByID(e.agents, host.AgentID())
	if a == nil {
		return nil
	}

	return a.Surface(k)
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

// coverageIssues renders the coverage scan for the doctor: fork warnings per
// name and one Info line per host.
func coverageIssues(host bundle.Host, cov coverage) []Issue {
	var issues []Issue

	for _, warn := range cov.Warnings {
		issues = append(issues, Issue{Severity: SeverityWarn, Kind: kind.Skills, Agent: host.AgentID(), Message: warn})
	}

	if len(cov.Skills) == 0 {
		return issues
	}

	names := slices.Sorted(maps.Keys(cov.Skills))

	issues = append(issues, Issue{
		Severity: SeverityInfo, Kind: kind.Skills, Agent: host.AgentID(),
		Message: fmt.Sprintf("%d skill(s) covered by other tools: %s", len(names), summarizeFarmNames(names)),
	})

	return issues
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

func bundlePluginKey() string {
	return bundle.MarketplaceName + "/" + bundle.PluginName
}

func dropBundleLedgerEntry(ledger pluginLedger) bool {
	key := bundlePluginKey()

	if _, ok := ledger.Plugins[key]; !ok {
		return false
	}

	delete(ledger.Plugins, key)

	return true
}
