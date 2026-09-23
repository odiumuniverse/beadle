package engine

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/hooks"
	"github.com/odiumuniverse/beadle/pkg/plugin"
	"github.com/odiumuniverse/beadle/pkg/secret"
)

const (
	pluginHookPrefix = "plugin hooks: "
)

// selfPluginKey is beadle's own rendered bundle: scanning it would offer the
// canon back to the user as a plugin.
var selfPluginKey = bundlePluginKey()

// pluginHookEvents maps the Claude hook events the canon can express.
var pluginHookEvents = map[string]string{
	"PreToolUse":   hooks.EventPreTool,
	"PostToolUse":  hooks.EventPostTool,
	"SessionStart": hooks.EventSessionStart,
	"Stop":         hooks.EventStop,
	"Notification": hooks.EventNotification,
}

var (
	pluginNameClean   = regexp.MustCompile(`[^a-z0-9]+`)
	unbracedClaudeVar = regexp.MustCompile(`\$CLAUDE_[A-Z0-9_]*`)
)

// pluginHookEntry is one canon hook projected from a plugin.
type pluginHookEntry struct {
	name string
	hook hooks.Hook
}

// pluginHookPlan is the projection of one plugin's hook definitions.
type pluginHookPlan struct {
	Entries []pluginHookEntry
	// Source is the canon source marker of this plugin.
	Source string
	// Warnings are aggregated per plugin, not per hook: a plugin with a dozen
	// unsupported events must not flood the report.
	Warnings []string
	// Skipped counts the hook definitions the canon cannot express.
	Skipped int
}

// pluginHookCounters tallies the skipped definitions for the aggregated note.
type pluginHookCounters struct {
	events     int
	types      int
	fields     int
	duplicates int
}

// projectPluginHooks renders one plugin's command hooks into canon entries.
// The command keeps the plugin's matcher; `${CLAUDE_PLUGIN_ROOT}` expands to
// the plugin pivot, and everything the canon cannot express is skipped and
// counted.
func (e *Engine) projectPluginHooks(key, installPath string) (pluginHookPlan, error) {
	var plan pluginHookPlan

	doc, warns, err := plugin.ReadHooks(installPath)
	if err != nil {
		return plan, err
	}

	plan.Warnings = append(plan.Warnings, warns...)

	marketplace, name, ok := strings.Cut(key, "/")
	if !ok || !validPluginKey(marketplace, name) {
		return plan, fmt.Errorf("invalid plugin key %q", key)
	}

	pivot := filepath.Join(e.vault.PluginsDir(), marketplace, name, farmPivotName)

	plan.Source = hooks.SourcePluginPrefix + key

	var counters pluginHookCounters

	for _, event := range slices.Sorted(maps.Keys(doc)) {
		canonEvent, ok := pluginHookEvents[event]
		if !ok {
			for _, group := range doc[event] {
				counters.events += len(group.Hooks)
			}

			continue
		}

		e.projectPluginEvent(key, event, canonEvent, doc[event], name, pivot, plan.Source, &plan, &counters)
	}

	if skipped := counters.total(); skipped > 0 {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"%splugin %s: skipped %d hook(s) on unsupported events, %d non-command handler(s), %d handler(s) with unsupported fields, %d duplicate(s)",
			pluginHookPrefix, key, counters.events, counters.types, counters.fields, counters.duplicates))

		plan.Skipped = skipped
	}

	return plan, nil
}

// projectPluginEvent projects one plugin event into canon entries.
func (e *Engine) projectPluginEvent(
	key, event, canonEvent string, groups []plugin.HookGroup, name, pivot, source string,
	plan *pluginHookPlan, counters *pluginHookCounters,
) {
	index := 0

	for _, group := range groups {
		for _, handler := range group.Hooks {
			hook, category, warning, ok := projectPluginHandler(key, event, canonEvent, group.Matcher, handler, pivot, source)
			if !ok {
				counters.add(category)

				if warning != "" {
					plan.Warnings = append(plan.Warnings, warning)
				}

				continue
			}

			if warning != "" {
				plan.Warnings = append(plan.Warnings, warning)
			}

			index++

			if slices.ContainsFunc(plan.Entries, func(entry pluginHookEntry) bool { return entry.hook == hook }) {
				counters.duplicates++

				continue
			}

			plan.Entries = append(plan.Entries, pluginHookEntry{name: pluginHookName(name, canonEvent, index), hook: hook})
		}
	}
}

// projectPluginHandler validates one handler and renders the canon hook. It
// reports the skip category ("type" or "fields"), an optional warning and
// whether the handler is renderable.
func projectPluginHandler(
	key, event, canonEvent, matcher string, handler plugin.HookHandler, pivot, source string,
) (hooks.Hook, string, string, bool) {
	if handler.Type != "command" {
		return hooks.Hook{}, "type", "", false
	}

	if len(handler.Unsupported) > 0 {
		return hooks.Hook{}, "fields", fmt.Sprintf(
			"%splugin %s hook %s: unsupported field(s) %s; not rendered",
			pluginHookPrefix, key, event, strings.Join(handler.Unsupported, ", ")), false
	}

	command, bad := expandPluginHookCommand(handler.Command, pivot)
	if bad != "" {
		return hooks.Hook{}, "fields", fmt.Sprintf(
			"%splugin %s hook %s: unsupported variable %q; not rendered", pluginHookPrefix, key, event, bad), false
	}

	if strings.TrimSpace(command) == "" {
		return hooks.Hook{}, "fields", fmt.Sprintf(
			"%splugin %s hook %s: empty command; not rendered", pluginHookPrefix, key, event), false
	}

	timeout := handler.Timeout
	warning := ""

	if timeout > hooks.MaxTimeout {
		warning = fmt.Sprintf(
			"%splugin %s hook %s: timeout %ds exceeds the canon maximum %ds; clamped",
			pluginHookPrefix, key, event, handler.Timeout, hooks.MaxTimeout)

		timeout = hooks.MaxTimeout
	}

	return hooks.Hook{
		Event:   canonEvent,
		Matcher: matcher,
		Command: command,
		Timeout: timeout,
		Source:  source,
	}, "", warning, true
}

// add tallies one skipped handler by category.
func (c *pluginHookCounters) add(category string) {
	switch category {
	case "type":
		c.types++
	default:
		c.fields++
	}
}

// total counts every skipped definition.
func (c *pluginHookCounters) total() int {
	return c.events + c.types + c.fields + c.duplicates
}

// pluginHookName builds the deterministic canon name of a projected hook.
func pluginHookName(pluginName, event string, index int) string {
	name := strings.Trim(pluginNameClean.ReplaceAllString(strings.ToLower(pluginName), "-"), "-")
	if name == "" {
		name = "plugin"
	}

	return fmt.Sprintf("%s--%s-%d", name, event, index)
}

// expandPluginHookCommand expands ${CLAUDE_PLUGIN_ROOT} to the plugin pivot.
// Host-specific placeholders the canon cannot resolve are refused, while
// ordinary shell variables stay verbatim.
func expandPluginHookCommand(command, pivot string) (string, string) {
	var out strings.Builder

	rest := command

	for {
		start := strings.Index(rest, "${")
		if start < 0 {
			out.WriteString(rest)

			break
		}

		end := strings.Index(rest[start:], "}")
		if end < 0 {
			return "", "unterminated ${"
		}

		name := rest[start+2 : start+end]

		switch {
		case name == "CLAUDE_PLUGIN_ROOT":
			out.WriteString(rest[:start])
			out.WriteString(pivot)
		case name == "CLAUDE_PLUGIN_DATA", name == "CLAUDE_PROJECT_DIR", strings.HasPrefix(name, "user_config."):
			return "", name
		default:
			out.WriteString(rest[:start+end+1])
		}

		rest = rest[start+end+1:]
	}

	expanded := out.String()

	if match := unbracedClaudeVar.FindString(expanded); match != "" {
		return "", match
	}

	return expanded, ""
}

// ApprovePluginHooks copies the command hooks of one installed plugin into the
// canon and approves them. It is the only path plugin hooks take into the
// canon: the scan never writes and nothing is auto-approved.
func (e *Engine) ApprovePluginHooks(key string) (Report, error) {
	report := Report{}

	if key == selfPluginKey {
		return report, fmt.Errorf("plugin %s is beadle's own bundle", key)
	}

	manifest, err := plugin.Read(e.home)
	if err != nil {
		return report, err
	}

	installed, ok := installedPlugin(manifest, key)
	if !ok {
		return report, fmt.Errorf("plugin %s is not installed", key)
	}

	info, err := os.Stat(installed.InstallPath)
	if err != nil || !info.IsDir() {
		return report, fmt.Errorf("plugin %s install path %s is missing; reinstall the plugin", key, installed.InstallPath)
	}

	marketplace, name, _ := strings.Cut(key, "/")
	pivot := filepath.Join(e.vault.PluginsDir(), marketplace, name, farmPivotName)

	if !pivotValid(pivot) {
		return report, fmt.Errorf("plugin %s pivot %s is missing; run `beadle sync` first", key, pivot)
	}

	plan, err := e.projectPluginHooks(key, installed.InstallPath)
	report.Warnings = append(report.Warnings, plan.Warnings...)

	if err != nil {
		return report, err
	}

	canon, err := hooks.Load(e.vault.HooksPath())
	if err != nil {
		return report, err
	}

	approved, refreshed, warns := upsertPluginHooks(canon, plan)
	report.Warnings = append(report.Warnings, warns...)

	removed := removeStalePluginHooks(canon, plan)

	if err := hooks.Save(e.vault.HooksPath(), canon); err != nil {
		return report, err
	}

	approvePlanEntries(e.config, canon, plan)

	if err := e.config.Save(e.vault.ConfigPath()); err != nil {
		return report, err
	}

	report.Notes = append(report.Notes, fmt.Sprintf(
		"%s%s: %d approved, %d refreshed, %d removed, %d skipped",
		pluginHookPrefix, key, approved, refreshed, removed, plan.Skipped))

	return report, nil
}

// upsertPluginHooks writes the plan into the canon: new names are approved,
// entries from the same plugin are refreshed, and anything else is a collision
// the canon wins.
func upsertPluginHooks(canon map[string]hooks.Hook, plan pluginHookPlan) (approved, refreshed int, warns []string) {
	for _, entry := range plan.Entries {
		existing, exists := canon[entry.name]

		switch {
		case !exists:
			canon[entry.name] = entry.hook
			approved++
		case existing.Source == plan.Source:
			if existing != entry.hook {
				canon[entry.name] = entry.hook
				refreshed++
			}
		default:
			warns = append(warns, fmt.Sprintf(
				"%shook %s already exists in the canon (source %q); left alone", pluginHookPrefix, entry.name, existing.Source))
		}
	}

	return approved, refreshed, warns
}

// removeStalePluginHooks deletes the canon entries of this plugin that its
// current hooks no longer produce, so re-running approve resolves drift.
func removeStalePluginHooks(canon map[string]hooks.Hook, plan pluginHookPlan) int {
	keep := map[string]bool{}

	for _, entry := range plan.Entries {
		keep[entry.name] = true
	}

	removed := 0

	for _, name := range slices.Sorted(maps.Keys(canon)) {
		if canon[name].Source == plan.Source && !keep[name] {
			delete(canon, name)

			removed++
		}
	}

	return removed
}

// approvePlanEntries approves the plan entries beadle actually wrote: a name
// taken by another source (collision) stays unapproved, so approving one
// plugin never blesses a user's or another plugin's hook.
func approvePlanEntries(cfg *config.Config, canon map[string]hooks.Hook, plan pluginHookPlan) {
	for _, entry := range plan.Entries {
		if existing, ok := canon[entry.name]; ok && existing.Source == plan.Source {
			cfg.ApproveHook(entry.name)
		}
	}
}

// installedPlugin finds one plugin by its <marketplace>/<name> key.
func installedPlugin(manifest plugin.Manifest, key string) (plugin.Plugin, bool) {
	for _, p := range manifest.Plugins {
		if pluginKey(p.Marketplace, p.Name) == key {
			return p, true
		}
	}

	return plugin.Plugin{}, false
}

// pluginHookIssues reports the plugin hooks beadle can offer and the approved
// ones that went stale: nothing is approved automatically, so the pending
// plugins wait as Info until the user runs `beadle hooks approve --plugin`.
func (e *Engine) pluginHookIssues() []Issue {
	if e.home == "" {
		return nil
	}

	canon, err := hooks.Load(e.vault.HooksPath())
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Message: "cannot read the hooks canon: " + err.Error()}}
	}

	manifest, err := plugin.Read(e.home)
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Message: pluginHookPrefix + err.Error()}}
	}

	installed := map[string]plugin.Plugin{}

	for _, p := range manifest.Plugins {
		key := pluginKey(p.Marketplace, p.Name)
		if _, exists := installed[key]; !exists {
			installed[key] = p
		}
	}

	var issues []Issue

	for _, key := range slices.Sorted(maps.Keys(installed)) {
		if key == selfPluginKey {
			continue
		}

		issues = append(issues, e.pluginHookIssueFor(key, installed[key].InstallPath, canon)...)
	}

	return append(issues, missingPluginHookIssues(canon, installed)...)
}

// pluginHookIssueFor reports the pending, collided or drifted hooks of one
// plugin, and warns when its pivot is missing so approve cannot work yet.
func (e *Engine) pluginHookIssueFor(key, installPath string, canon map[string]hooks.Hook) []Issue {
	plan, err := e.projectPluginHooks(key, installPath)
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Message: pluginHookPrefix + key + ": " + err.Error()}}
	}

	if len(plan.Entries) > 0 {
		marketplace, name, _ := strings.Cut(key, "/")
		pivot := filepath.Join(e.vault.PluginsDir(), marketplace, name, farmPivotName)

		if !pivotValid(pivot) {
			return []Issue{{Severity: SeverityWarn, Message: fmt.Sprintf(
				"%splugin %s pivot %s is missing; run `beadle sync` to create it", pluginHookPrefix, key, pivot)}}
		}
	}

	drift, collisions := plan.Analyze(canon)

	collided := map[string]bool{}

	for _, name := range collisions {
		collided[name] = true
	}

	pending := 0

	for _, entry := range plan.Entries {
		// A collided name is never written by approve, so it must not keep
		// the pending Info alive after the user ran the command.
		if collided[entry.name] {
			continue
		}

		if !e.config.HookApproved(entry.name) {
			pending++
		}
	}

	var issues []Issue

	if len(collisions) > 0 {
		issues = append(issues, Issue{Severity: SeverityWarn, Message: fmt.Sprintf(
			"%shook name(s) %s are taken by other canon hooks; the canon copy is not written — rename or revoke the other hook",
			pluginHookPrefix, strings.Join(collisions, ", "))})
	}

	switch {
	case pending > 0:
		issues = append(issues, Issue{Severity: SeverityInfo, Message: fmt.Sprintf(
			"%splugin %s ships %d hook(s); approve with `beadle hooks approve --plugin %s`%s",
			pluginHookPrefix, key, pending, key, skippedNote(plan.Skipped))})
	case len(plan.Entries) == 0 && plan.Skipped > 0:
		issues = append(issues, Issue{Severity: SeverityInfo, Message: fmt.Sprintf(
			"%splugin %s: no expressible hook(s); %d skipped", pluginHookPrefix, key, plan.Skipped)})
	case drift:
		issues = append(issues, Issue{Severity: SeverityInfo, Message: fmt.Sprintf(
			"%splugin %s hooks changed since approval; re-run `beadle hooks approve --plugin %s`", pluginHookPrefix, key, key)})
	}

	return issues
}

// missingPluginHookIssues warns about approved plugin hooks whose plugin is
// gone; nothing is removed automatically.
func missingPluginHookIssues(canon map[string]hooks.Hook, installed map[string]plugin.Plugin) []Issue {
	missing := map[string][]string{}

	for _, name := range slices.Sorted(maps.Keys(canon)) {
		key, fromPlugin := canon[name].PluginKey()
		if !fromPlugin || key == selfPluginKey {
			continue
		}

		if _, ok := installed[key]; ok {
			continue
		}

		missing[key] = append(missing[key], name)
	}

	var issues []Issue

	for _, key := range slices.Sorted(maps.Keys(missing)) {
		issues = append(issues, Issue{Severity: SeverityWarn, Message: fmt.Sprintf(
			"%s%d approved hook(s) come from plugin %s, which is not installed: %s; they may not run; revoke with `beadle hooks revoke <name>`",
			pluginHookPrefix, len(missing[key]), key, strings.Join(missing[key], ", "))})
	}

	return issues
}

// Analyze reports the drift and the name collisions of this plan against the
// canon: a name taken by another source is a collision the canon wins, not a
// drift, so it is reported once and never as a stale entry.
func (p pluginHookPlan) Analyze(canon map[string]hooks.Hook) (drift bool, collisions []string) {
	entries := map[string]hooks.Hook{}

	for _, entry := range p.Entries {
		entries[entry.name] = entry.hook
	}

	for _, name := range slices.Sorted(maps.Keys(entries)) {
		existing, ok := canon[name]

		switch {
		case !ok:
			drift = true
		case existing.Source != p.Source:
			collisions = append(collisions, name)
		case existing != entries[name]:
			drift = true
		}
	}

	for name, hook := range canon {
		if hook.Source != p.Source {
			continue
		}

		if _, ours := entries[name]; !ours {
			drift = true
		}
	}

	return drift, collisions
}

// skippedNote renders the aggregated skip count for one Info line.
func skippedNote(skipped int) string {
	if skipped == 0 {
		return ""
	}

	return fmt.Sprintf(" (%d skipped: unsupported events, handler types or fields)", skipped)
}

// hookSecretIssues warns about secret-like lines in the hooks canon. The gate
// covers mcp, memory and project files only; hooks are not scanned.
func (e *Engine) hookSecretIssues() []Issue {
	canon, err := hooks.Load(e.vault.HooksPath())
	if err != nil {
		return nil
	}

	var issues []Issue

	for _, name := range slices.Sorted(maps.Keys(canon)) {
		if hits := secret.ScanText([]byte(canon[name].Command)); len(hits) > 0 {
			issues = append(issues, Issue{Severity: SeverityWarn, Message: fmt.Sprintf(
				"hook %s holds %d secret-like line(s); the secret gate covers mcp, memory and project files only — keep tokens out of hooks",
				name, len(hits))})
		}
	}

	return issues
}

// renderableHooks drops plugin-sourced hooks from the Claude bundle: the
// source plugin already runs them natively there, so a beadle copy would run
// them twice. Every other channel gets the full canon.
func (e *Engine) renderableHooks(host bundle.Host, canon map[string]hooks.Hook) map[string]hooks.Hook {
	if host != bundle.Claude {
		return canon
	}

	out := map[string]hooks.Hook{}

	for name, hook := range canon {
		if _, fromPlugin := hook.PluginKey(); fromPlugin {
			continue
		}

		out[name] = hook
	}

	return out
}
