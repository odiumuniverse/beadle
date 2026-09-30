package engine

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/hooks"
	"github.com/odiumuniverse/beadle/pkg/plugin"
	"github.com/odiumuniverse/beadle/pkg/secret"
	"github.com/odiumuniverse/verger/pkg/digest"
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
// expands to the plugin's install directory - the same directory the plugin
// manager gives the host - and everything the canon cannot express is skipped
// and counted.
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

	root := installPath

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

		e.projectPluginEvent(key, source, event, canonEvent, doc[event], name, root, plan.Source, &plan, &counters)
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

	// The farm's pivot used to sit between the plugin and beadle: a symlink to
	// this very install path. The projection takes the install path, so the
	// pivot is not consulted any more - and a vault that still has one is not
	// a vault that needs one.
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

	// The consent itself is the library's: beadle records what the user
	// approved against the hash of the hooks they were shown, so an edited
	// hook asks again. Nothing plugin-owned stays in config.json.
	if err := e.recordPluginHookConsent(key, canon); err != nil {
		return report, err
	}

	moduleApproved, moduleDropped, moduleWarns := approveHookModules(e.config, key, installed.Source, installed.InstallPath)
	report.Warnings = append(report.Warnings, moduleWarns...)

	if err := e.config.Save(e.vault.ConfigPath()); err != nil {
		return report, err
	}

	note := fmt.Sprintf(
		"%s%s: %d approved, %d refreshed, %d removed, %d skipped",
		pluginHookPrefix, key, approved, refreshed, removed, plan.Skipped)

	if moduleApproved > 0 || moduleDropped > 0 {
		note += fmt.Sprintf("; %d hook module(s) approved, %d stale module approval(s) dropped", moduleApproved, moduleDropped)
	}

	report.Notes = append(report.Notes, note)

	return report, nil
}

// approveHookModules approves the current hook modules of one omp plugin and
// drops the approvals of modules whose digest changed or that vanished, so a
// changed module asks for consent again. A module's consent key names the
// plugin and carries its digest; approving a plugin with no modules is a no-op.
func approveHookModules(cfg *config.Config, key, source, installPath string) (approved, dropped int, warns []string) {
	modules, scanWarns := plugin.HookModules(source, installPath)
	warns = append(warns, scanWarns...)

	current := map[string]bool{}

	for _, module := range modules {
		data, err := os.ReadFile(module.Path) //nolint:gosec // G304: the module path is resolved from the plugin install path
		if err != nil {
			warns = append(warns, fmt.Sprintf("%s%s: cannot read hooks/%s/%s: %v",
				pluginHookPrefix, key, module.Phase, module.Name, err))

			continue
		}

		consent := hooks.HookModuleKey(key, module.Phase, module.Name, cas.HashOf(data))
		current[consent] = true

		if !cfg.HookApproved(consent) {
			cfg.ApproveHook(consent)

			approved++
		}
	}

	for _, entry := range slices.Clone(cfg.ApprovedHooks) {
		if moduleKey, ok := hooks.HookModulePlugin(entry); ok && moduleKey == key && !current[entry] {
			cfg.RevokeHook(entry)

			dropped++
		}
	}

	return approved, dropped, warns
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

// pluginConsentID is the package id a plugin's hook consent is recorded under.
// It is the plugin's package id, so the record is the one the library itself
// would find when it gates the package.
func pluginConsentID(key string) string { return hooks.SourcePluginPrefix + key }

// pluginHookSetHash is the content hash of every canon hook one plugin
// contributed, by name. The library holds one approval per package, so the
// consent covers the set: an edited, added or dropped hook changes the hash
// and asks for consent again instead of riding on the old one.
func pluginHookSetHash(canon map[string]hooks.Hook, key string) (digest.Hash, bool) {
	type entry struct {
		Name    string `json:"name"`
		Event   string `json:"event"`
		Matcher string `json:"matcher,omitempty"`
		Command string `json:"command"`
		Timeout int    `json:"timeout,omitempty"`
	}

	entries := []entry{}

	for _, name := range slices.Sorted(maps.Keys(canon)) {
		hook := canon[name]

		owner, fromPlugin := hook.PluginKey()
		if !fromPlugin || owner != key {
			continue
		}

		entries = append(entries, entry{
			Name:    name,
			Event:   hook.Event,
			Matcher: hook.Matcher,
			Command: hook.Command,
			Timeout: hook.Timeout,
		})
	}

	if len(entries) == 0 {
		return "", false
	}

	data, err := json.Marshal(entries)
	if err != nil {
		return "", false
	}

	return digest.Hash(cas.HashOf(data)), true
}

// recordPluginHookConsent asks the library to remember that the user approved
// one plugin's hooks as they are now. Without a library there is nowhere to
// keep the consent, so the approval is refused rather than silently dropped.
func (e *Engine) recordPluginHookConsent(key string, canon map[string]hooks.Hook) error {
	if e.manager == nil {
		return fmt.Errorf("plugin %s: the plugin library is not available, so its hook approval cannot be recorded", key)
	}

	hash, ok := pluginHookSetHash(canon, key)
	if !ok {
		return nil
	}

	if _, err := e.manager.ApproveHooksFor(pluginConsentID(key), "", hash); err != nil {
		return fmt.Errorf("plugin %s: cannot record hook consent: %w", key, err)
	}

	return nil
}

// ApprovePluginHookSet records the user's consent for the hooks one plugin
// already has in the canon. Approving a single canon entry is the same act as
// approving the package, because the library holds one consent per package -
// and unlike `beadle hooks approve --plugin` it needs no installed plugin:
// there is nothing to project, the hooks are already in the canon.
func (e *Engine) ApprovePluginHookSet(key string) error {
	canon, err := hooks.Load(e.vault.HooksPath())
	if err != nil {
		return err
	}

	return e.recordPluginHookConsent(key, canon)
}

// RevokePluginHookConsent takes back the consent the user gave one plugin's
// hooks. The canon entries stay where they are; only the approval goes, so the
// doctor asks again.
func (e *Engine) RevokePluginHookConsent(key string) error {
	if e.manager == nil {
		return fmt.Errorf("plugin %s: the plugin library is not available, so its hook approval cannot be revoked", key)
	}

	if err := e.manager.RevokeHooksFor(pluginConsentID(key), ""); err != nil {
		return fmt.Errorf("plugin %s: cannot revoke hook consent: %w", key, err)
	}

	return nil
}

// PluginHookApprovedFor reports whether the library holds consent for the
// hooks one plugin contributed to the canon as it stands. An edited or dropped
// hook changes the hash, so the old consent stops covering it.
func (e *Engine) PluginHookApprovedFor(canon map[string]hooks.Hook, key string) bool {
	if e.manager == nil {
		return false
	}

	hash, ok := pluginHookSetHash(canon, key)
	if !ok {
		return false
	}

	approved, err := e.manager.HooksApprovedFor(pluginConsentID(key), "", hash)
	if err != nil {
		return false
	}

	return approved
}

// approvedHooksForRender is the set the renderers gate on: beadle's own
// name-keyed approvals, plus every plugin hook the library holds consent for.
// A plugin hook reaches a host only through the library's answer, so a hook
// whose consent lapsed stops travelling without touching config.json.
func (e *Engine) approvedHooksForRender(canon map[string]hooks.Hook) map[string]bool {
	approved := hooks.Approved(e.config)

	if e.manager == nil {
		return approved
	}

	owners := pluginHookOwners(canon)
	live := map[string]bool{}

	for _, key := range slices.Sorted(maps.Values(owners)) {
		if !live[key] {
			live[key] = e.PluginHookApprovedFor(canon, key)
		}
	}

	for name, key := range owners {
		if live[key] {
			approved[name] = true
		}
	}

	return approved
}

// pluginHookOwners maps every canon hook name to the plugin that contributed
// it. Hooks beadle owns itself do not appear.
func pluginHookOwners(canon map[string]hooks.Hook) map[string]string {
	owners := map[string]string{}

	for name, hook := range canon {
		if key, fromPlugin := hook.PluginKey(); fromPlugin {
			owners[name] = key
		}
	}

	return owners
}

// migratePluginHookConsent moves the name-keyed plugin hook approvals of an
// older vault into the library's consent store, once, and says so: hook
// approval is never taken silently. Approvals beadle still owns — a user's own
// hooks, a hook module keyed by its digest — are left exactly where they are.
func (e *Engine) migratePluginHookConsent(canon map[string]hooks.Hook, report *Report) error {
	if e.manager == nil || len(e.config.ApprovedHooks) == 0 {
		return nil
	}

	byPlugin := map[string][]string{}

	for _, name := range slices.Clone(e.config.ApprovedHooks) {
		key, fromPlugin := canon[name].PluginKey()
		if !fromPlugin {
			continue
		}

		hash, ok := pluginHookSetHash(canon, key)
		if !ok {
			continue
		}

		approved, err := e.manager.HooksApprovedFor(pluginConsentID(key), "", hash)
		if err != nil {
			return fmt.Errorf("plugin %s: cannot read hook consent: %w", key, err)
		}

		if !approved {
			if _, err := e.manager.ApproveHooksFor(pluginConsentID(key), "", hash); err != nil {
				return fmt.Errorf("plugin %s: cannot move hook consent into the library: %w", key, err)
			}

			byPlugin[key] = append(byPlugin[key], name)
		}
	}

	if len(byPlugin) == 0 {
		return nil
	}

	for _, key := range slices.Sorted(maps.Keys(byPlugin)) {
		for _, name := range byPlugin[key] {
			e.config.RevokeHook(name)
		}

		report.Notes = append(report.Notes, fmt.Sprintf(
			"%s moved %d hook approval(s) of %s into the plugin library's consent store, keyed by content hash",
			pluginHookPrefix, len(byPlugin[key]), key))
	}

	return e.config.Save(e.vault.ConfigPath())
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

	suppressed := e.pluginDedup().Suppressed

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

	drift, collisions := plan.Analyze(canon)

	collided := map[string]bool{}

	for _, name := range collisions {
		collided[name] = true
	}

	// The consent is the library's and it covers one package, so the hooks are
	// pending as a set: either the canon as it stands is approved, or every
	// expressible hook asks again.
	pending := 0

	if !e.PluginHookApprovedFor(canon, key) {
		for _, entry := range plan.Entries {
			// A collided name is never written by approve, so it must not keep
			// the pending Info alive after the user ran the command.
			if collided[entry.name] {
				continue
			}

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

// pluginHookSources reports, per plugin key, the hosts that read that plugin
// natively - the host that installed it, and any host whose own copy lost the
// same-key source conflict. A plugin hook never travels to those hosts: they
// already run it from the plugin manager. The host registries answer this;
// the farm's ledger no longer exists to be asked.
func (e *Engine) pluginHookSources() (sources map[string]map[string]bool, retired map[string]bool) {
	return e.pluginDedup().LoserHosts, map[string]bool{}
}

// loadCanonForRender reads the canon for a renderer that does not have it. A
// malformed document simply means no plugin hook travels.
func loadCanonForRender(e *Engine) map[string]hooks.Hook {
	canon, err := hooks.Load(e.vault.HooksPath())
	if err != nil {
		return nil
	}

	return canon
}
