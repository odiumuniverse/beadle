package engine

import (
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/hooks"
	"github.com/odiumuniverse/beadle/pkg/plugin"
	"github.com/odiumuniverse/beadle/pkg/secret"
)

const (
	pluginHookPrefix = "plugin hooks: "

	// The Claude compatibility placeholder names every plugin host accepts.
	claudePluginRootVar = "CLAUDE_PLUGIN_ROOT"
	claudePluginDataVar = "CLAUDE_PLUGIN_DATA"
	claudeProjectDirVar = "CLAUDE_PROJECT_DIR"
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
	pluginNameClean = regexp.MustCompile(`[^a-z0-9]+`)
	// unbracedPluginVar matches every known plugin variable without braces: the
	// canon stores shell-ready commands, so a bare $NAME would expand to an
	// empty string in the receiving host.
	unbracedPluginVar = regexp.MustCompile(`\$(PLUGIN_ROOT|PLUGIN_DATA|CLAUDE_[A-Z0-9_]*|CURSOR_PLUGIN_ROOT|extensionPath|workspacePath)`)
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
// The command keeps the plugin's matcher; the host's plugin-root placeholder
// expands to the plugin pivot, and everything the canon cannot express is
// skipped and counted.
func (e *Engine) projectPluginHooks(key, source, installPath string) (pluginHookPlan, error) {
	var plan pluginHookPlan

	doc, warns, err := plugin.ReadHooksFor(source, installPath)
	if err != nil {
		return plan, err
	}

	plan.Warnings = append(plan.Warnings, warns...)

	origin, name, ok := strings.Cut(key, "/")
	if !ok || !validPluginKey(origin, name) {
		return plan, fmt.Errorf("invalid plugin key %q", key)
	}

	pivot := e.pluginPivotDir(key)

	plan.Source = hooks.SourcePluginPrefix + key

	var counters pluginHookCounters

	eventNames := pluginHookEventsFor(source)

	for _, event := range slices.Sorted(maps.Keys(doc)) {
		canonEvent, ok := eventNames[event]
		if !ok {
			for _, group := range doc[event] {
				counters.events += len(group.Hooks)
			}

			continue
		}

		e.projectPluginEvent(key, source, event, canonEvent, doc[event], name, pivot, plan.Source, &plan, &counters)
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
	key, source, event, canonEvent string, groups []plugin.HookGroup, name, pivot, sourceMarker string,
	plan *pluginHookPlan, counters *pluginHookCounters,
) {
	index := 0

	for _, group := range groups {
		for _, handler := range group.Hooks {
			hook, category, warning, ok := projectPluginHandler(key, source, event, canonEvent, group.Matcher, handler, pivot, sourceMarker)
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
	key, source, event, canonEvent, matcher string, handler plugin.HookHandler, pivot, sourceMarker string,
) (hooks.Hook, string, string, bool) {
	if handler.Type != "command" {
		return hooks.Hook{}, "type", "", false
	}

	if len(handler.Unsupported) > 0 {
		return hooks.Hook{}, "fields", fmt.Sprintf(
			"%splugin %s hook %s: unsupported field(s) %s; not rendered",
			pluginHookPrefix, key, event, strings.Join(handler.Unsupported, ", ")), false
	}

	command, bad := expandPluginHookCommand(handler.Command, pivot, source)
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
		Source:  sourceMarker,
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

// expandPluginHookCommand expands the host's plugin-root placeholder to the
// plugin pivot. Host-specific placeholders the canon cannot resolve (plugin
// data directories, workspace paths) are refused, while ordinary shell
// variables stay verbatim.
func expandPluginHookCommand(command, pivot, source string) (string, string) {
	roots, refuse := pluginHookPlaceholders(source)

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
		case roots[name]:
			out.WriteString(rest[:start])
			out.WriteString(pivot)
		case refuse[name] || strings.HasPrefix(name, "user_config."):
			return "", name
		default:
			out.WriteString(rest[:start+end+1])
		}

		rest = rest[start+end+1:]
	}

	expanded := out.String()

	if match := unbracedPluginVar.FindString(expanded); match != "" {
		return "", match
	}

	return expanded, ""
}

// pluginHookKnownVars lists every plugin variable beadle knows. A hook may
// only carry the roots of its own source: everything else — the data and
// workspace directories, and the roots of other hosts — cannot be resolved for
// the receiving host and is refused instead of shipping a command that would
// expand to an empty string.
var pluginHookKnownVars = []string{
	"PLUGIN_ROOT",
	"PLUGIN_DATA",
	claudePluginRootVar,
	claudePluginDataVar,
	claudeProjectDirVar,
	"CURSOR_PLUGIN_ROOT",
	"extensionPath",
	"workspacePath",
}

// pluginHookPlaceholders returns the plugin root placeholders of one source,
// which expand to the plugin pivot, and every other known variable, which the
// canon refuses.
func pluginHookPlaceholders(source string) (map[string]bool, map[string]bool) {
	roots := pluginHookRoots(source)
	refuse := make(map[string]bool, len(pluginHookKnownVars))

	for _, name := range pluginHookKnownVars {
		if !roots[name] {
			refuse[name] = true
		}
	}

	return roots, refuse
}

// pluginHookRoots lists the root placeholders of one plugin source that expand
// to the plugin pivot; the Claude compatibility name works in every host.
func pluginHookRoots(source string) map[string]bool {
	switch source {
	case plugin.SourceCodex:
		return map[string]bool{"PLUGIN_ROOT": true, claudePluginRootVar: true}
	case plugin.SourceGeminiCLI:
		return map[string]bool{"extensionPath": true}
	case plugin.SourceCursor:
		return map[string]bool{"CURSOR_PLUGIN_ROOT": true, claudePluginRootVar: true}
	case plugin.SourceAntigravityCLI:
		return map[string]bool{"PLUGIN_ROOT": true, claudePluginRootVar: true}
	default:
		return map[string]bool{claudePluginRootVar: true}
	}
}

// pluginHookEventsFor maps the hook event names of one host to the canon
// events. Only unambiguous names are mapped: everything else is skipped and
// counted, so the trust gate never blesses a hook with a guessed meaning.
func pluginHookEventsFor(source string) map[string]string {
	switch source {
	case plugin.SourceCodex:
		return map[string]string{
			"PreToolUse":   hooks.EventPreTool,
			"PostToolUse":  hooks.EventPostTool,
			"SessionStart": hooks.EventSessionStart,
			"Stop":         hooks.EventStop,
		}
	case plugin.SourceGeminiCLI:
		return map[string]string{
			"BeforeTool":   hooks.EventPreTool,
			"AfterTool":    hooks.EventPostTool,
			"SessionStart": hooks.EventSessionStart,
			"Notification": hooks.EventNotification,
		}
	case plugin.SourceCursor:
		return map[string]string{
			"preToolUse":   hooks.EventPreTool,
			"postToolUse":  hooks.EventPostTool,
			"sessionStart": hooks.EventSessionStart,
			"stop":         hooks.EventStop,
		}
	default:
		return pluginHookEvents
	}
}

// ApprovePluginHooks copies the command hooks of one installed plugin into the
// canon and approves them. It is the only path plugin hooks take into the
// canon: the scan never writes and nothing is auto-approved.
func (e *Engine) ApprovePluginHooks(key string) (Report, error) {
	report := Report{}

	if key == selfPluginKey {
		return report, fmt.Errorf("plugin %s is beadle's own bundle", key)
	}

	manifest, err := plugin.ReadAll(e.home)
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

	pivot := e.pluginPivotDir(key)

	if !pivotValid(pivot) {
		return report, fmt.Errorf("plugin %s pivot %s is missing; run `beadle sync` first", key, pivot)
	}

	plan, err := e.projectPluginHooks(key, installed.Source, installed.InstallPath)
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

// installedPlugin finds one plugin by its <origin>/<name> key.
func installedPlugin(manifest plugin.Manifest, key string) (plugin.Plugin, bool) {
	for _, p := range manifest.Plugins {
		if pluginKey(p.Origin, p.Name) == key {
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

	manifest, err := plugin.ReadAll(e.home)
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Message: pluginHookPrefix + err.Error()}}
	}

	installed := map[string]plugin.Plugin{}

	for _, p := range manifest.Plugins {
		key := pluginKey(p.Origin, p.Name)
		if _, exists := installed[key]; !exists {
			installed[key] = p
		}
	}

	suppressed := map[string]string{}

	if ledger, _, err := loadPluginLedger(e.vault.PluginsLedgerPath()); err == nil {
		suppressed = e.pluginDedup(ledger).Suppressed
	}

	var issues []Issue

	for _, key := range slices.Sorted(maps.Keys(installed)) {
		if key == selfPluginKey {
			continue
		}

		if _, covered := suppressed[key]; covered {
			continue
		}

		issues = append(issues, e.pluginHookIssueFor(key, installed[key].Source, installed[key].InstallPath, canon)...)
	}

	return append(issues, missingPluginHookIssues(canon, installed)...)
}

// pluginHookIssueFor reports the pending, collided or drifted hooks of one
// plugin, and warns when its pivot is missing so approve cannot work yet.
func (e *Engine) pluginHookIssueFor(key, source, installPath string, canon map[string]hooks.Hook) []Issue {
	plan, err := e.projectPluginHooks(key, source, installPath)
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Message: pluginHookPrefix + key + ": " + err.Error()}}
	}

	if len(plan.Entries) > 0 {
		pivot := e.pluginPivotDir(key)

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

// missingPluginHookIssues flags the approved plugin hooks whose plugin is gone
// from every registry: their renders are dropped on the next sync, and the
// canon entries wait for an explicit revoke — nothing is erased automatically.
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
		issues = append(issues, Issue{Severity: SeverityError, Message: fmt.Sprintf(
			"%s%d approved hook(s) come from plugin %s, which is not installed: %s; their renders are removed on the next sync; revoke with `beadle hooks revoke --plugin %s`",
			pluginHookPrefix, len(missing[key]), key, strings.Join(missing[key], ", "), key)})
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

// renderableHooks keeps the hooks one host should receive: a plugin's hooks
// belong to the host that installed it, which already runs them natively, so
// only the other hosts get a beadle copy. Canon-authored hooks go everywhere.
// A retired plugin (gone from every registry) renders nowhere: its hook
// commands point into a plugin that no longer exists. The Claude bundle no
// longer drops every plugin hook — a Codex plugin's hook must work in Claude
// too — and a stale plugin entry the ledger no longer knows keeps the pre-A-47
// Claude behavior (dropped from the Claude bundle).
func (e *Engine) renderableHooks(host bundle.Host, canon map[string]hooks.Hook) map[string]hooks.Hook {
	sources, retired := e.pluginHookSources()

	out := make(map[string]hooks.Hook, len(canon))

	for name, hook := range canon {
		if key, fromPlugin := hook.PluginKey(); fromPlugin && retired[key] {
			continue
		}

		if !hostRunsHookNatively(host.AgentID(), hook, sources) {
			out[name] = hook
		}
	}

	return out
}

// hostRunsHookNatively reports that the host already runs this hook without
// beadle: the hook comes from a plugin installed in that host — the ledger
// winner or a source whose own copy lost the source conflict — or the ledger
// no longer knows the plugin and the host is Claude Code, the pre-A-47 default
// for entries without a source.
func hostRunsHookNatively(agentID string, hook hooks.Hook, sources map[string]map[string]bool) bool {
	key, fromPlugin := hook.PluginKey()
	if !fromPlugin {
		return false
	}

	hosts, known := sources[key]

	return (known && hosts[agentID]) || (!known && agentID == agent.ClaudeCodeID)
}

// pluginHookSources maps every ledger plugin key to the source hosts that
// installed it — the winner plus the sources whose own copy lost the source
// conflict (`overridden`), because each of those hosts runs its own plugin's
// hooks natively. Retired keys are reported separately: a retired plugin
// renders its hooks nowhere.
func (e *Engine) pluginHookSources() (sources map[string]map[string]bool, retired map[string]bool) {
	ledger, _, _ := loadPluginLedger(e.vault.PluginsLedgerPath())

	sources = make(map[string]map[string]bool, len(ledger.Plugins))
	retired = map[string]bool{}

	for key, rec := range ledger.Plugins {
		if !rec.RetiredAt.IsZero() {
			retired[key] = true

			continue
		}

		set := map[string]bool{recSource(rec): true}
		for _, source := range rec.Overridden {
			set[source] = true
		}

		sources[key] = set
	}

	return sources, retired
}
