package engine

import (
	"fmt"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/plugin"
)

// pluginSourceIssues reports every plugin host: how many installed plugins it
// provides, or that the host is not installed. Reader warnings of the
// non-Claude hosts surface here so one broken host is visible without a sync.
func (e *Engine) pluginSourceIssues() []Issue {
	if e.home == "" {
		return nil
	}

	manifest, err := plugin.ReadAll(e.home)
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Message: "plugin sources: " + err.Error()}}
	}

	counts := map[string]int{}

	for _, p := range manifest.Plugins {
		if pluginKey(p.Origin, p.Name) == bundlePluginKey() {
			// Beadle's own rendered bundle is not a user plugin.
			continue
		}

		counts[p.Source]++
	}

	var issues []Issue

	for _, source := range plugin.SourceHosts() {
		if !e.pluginCacheReachable(source) {
			issues = append(issues, Issue{Severity: SeverityInfo, Message: fmt.Sprintf("plugin source %s: not installed", source)})

			continue
		}

		issues = append(issues, Issue{Severity: SeverityInfo, Message: fmt.Sprintf("plugin source %s: %d plugin(s) found", source, counts[source])})
	}

	issues = append(issues, pluginSourceWarnings(manifest.Warnings)...)

	return issues
}

// pluginSourceWarnings surfaces the reader warnings of the non-Claude hosts.
// Claude warnings keep their existing visibility: the registry is reported by
// the pivot and pin checks, and a sync prints the whole list.
func pluginSourceWarnings(warnings []string) []Issue {
	var issues []Issue

	for _, source := range plugin.SourceHosts() {
		if source == plugin.SourceClaudeCode {
			continue
		}

		prefix := source + ": "

		for _, warn := range warnings {
			if rest, ok := strings.CutPrefix(warn, prefix); ok {
				issues = append(issues, Issue{Severity: SeverityWarn, Message: "plugin source " + source + ": " + rest})
			}
		}
	}

	return issues
}

// pluginDuplicateIssues reports the same plugin provided by several hosts
// with different content. Byte-identical duplicates are presented once and
// need no warning; divergent copies would silently hide one of them.
func (e *Engine) pluginDuplicateIssues() []Issue {
	if e.home == "" {
		return nil
	}

	var issues []Issue

	if manifest, err := plugin.ReadAll(e.home); err == nil {
		_, _, warns := groupPlugins(manifest.Plugins)

		for _, warn := range warns {
			issues = append(issues, Issue{Severity: SeverityWarn, Message: warn})
		}
	}

	if ledger, _, err := loadPluginLedger(e.vault.PluginsLedgerPath()); err == nil {
		for _, warn := range e.pluginDedup(ledger).Warns {
			issues = append(issues, Issue{Severity: SeverityWarn, Message: warn})
		}
	}

	return issues
}
