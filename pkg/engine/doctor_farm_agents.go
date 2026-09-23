package engine

// FarmAgentIssues reports the plugin-agent farm findings a dry run cannot
// show: nested definitions that stay behind, name collisions between plugins
// and canon shadows. A quarantined plugin is already covered by the lifecycle
// error (pivotLifecycleIssues), so it is not repeated here.
func (e *Engine) FarmAgentIssues() []Issue {
	return e.farmFileIssues(farmAgentSpec)
}

// FarmCommandIssues reports the plugin-command farm findings, mirroring
// FarmAgentIssues for the command kind.
func (e *Engine) FarmCommandIssues() []Issue {
	return e.farmFileIssues(farmCommandSpec)
}

func (e *Engine) farmFileIssues(spec farmFileSpec) []Issue {
	if e.home == "" || !e.config.KindEnabled(spec.Kind) {
		return nil
	}

	ledger, _, err := loadPluginLedger(e.vault.PluginsLedgerPath())
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Message: "plugin farm: " + err.Error()}}
	}

	_, warns := e.buildFarmFilePlan(ledger, spec)

	issues := make([]Issue, 0, len(warns))

	for _, warning := range warns {
		issues = append(issues, Issue{Severity: SeverityWarn, Kind: spec.Kind, Message: warning})
	}

	return issues
}
