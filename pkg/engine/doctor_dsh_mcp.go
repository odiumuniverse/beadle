package engine

import (
	"fmt"
	"maps"
	"slices"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// dshMCPIssues reports what keeps the canon MCP servers from reaching the DSH
// home patch layer: servers the plugin cannot run (a serverName or transport
// outside its schema) and patch-file states that block delivery (a beadle
// record that cannot be used, a foreign record mounting the same serverName).
// Plain drift stays with the dry-run sync report, and a damaged patch file is
// reported there too; this check stays silent when nothing blocks.
func (e *Engine) dshMCPIssues(active []*agent.Agent) []Issue {
	if e.home == "" || !e.config.KindEnabled(kind.MCP) {
		return nil
	}

	dsh := agent.ByID(active, agent.DSHID)
	if dsh == nil {
		return nil
	}

	surface := dsh.Surface(kind.MCP)
	if surface == nil || e.config.ModeFor(agent.DSHID, kind.MCP, surface.Traits().DefaultMode) == config.ModeOff {
		return nil
	}

	canon, _, err := e.loadVault(kind.MCP)
	if err != nil || len(canon) == 0 {
		return nil
	}

	var issues []Issue

	for _, name := range slices.Sorted(maps.Keys(canon)) {
		reason := agent.DSHMCPServerReason(name, canon[name])
		if reason == "" {
			continue
		}

		issues = append(issues, Issue{
			Severity: SeverityWarn, Kind: kind.MCP, Agent: agent.DSHID,
			Message: fmt.Sprintf("DSH cannot run MCP server %q: %s", name, reason),
		})
	}

	blockers, err := agent.DSHMCPBlockers(e.home)
	if err != nil {
		return issues
	}

	for _, name := range slices.Sorted(maps.Keys(blockers)) {
		if _, managed := canon[name]; !managed {
			continue
		}

		issues = append(issues, Issue{
			Severity: SeverityWarn, Kind: kind.MCP, Agent: agent.DSHID,
			Message: fmt.Sprintf("MCP server %q cannot reach DSH: %s; fix or remove the record, beadle does not change it", name, blockers[name]),
		})
	}

	return issues
}
