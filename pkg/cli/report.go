package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/odiumuniverse/agents-sync/pkg/engine"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
	"github.com/odiumuniverse/agents-sync/pkg/state"
)

const maxListed = 5

func printReport(w io.Writer, report *engine.Report) {
	if report.DryRun {
		fmt.Fprintln(w, "dry run: nothing was written")
	}

	for _, kr := range report.Kinds {
		printKind(w, kr, report.ConflictsOf(kr.Kind))
	}

	if lines := pluginLines(report.Plugins, report.Farm); len(lines) > 0 {
		fmt.Fprintln(w, "\nplugins")

		for _, line := range lines {
			fmt.Fprintln(w, line)
		}
	}

	if n := len(report.Conflicts); n > 0 {
		fmt.Fprintf(w, "\n%d open conflict(s): review with `agent-sync conflicts`, settle with `agent-sync resolve <id> --take vault|agent`\n", n)
	}

	for _, warning := range report.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warning)
	}
}

func pluginLines(results []engine.PluginResult, farm []engine.FarmResult) []string {
	var lines []string

	for _, result := range results {
		switch result.Action {
		case engine.PluginCreated, engine.PluginRepointed:
			lines = append(lines, fmt.Sprintf("  → %s %s %s", result.Key, result.Version, result.Action))
		case engine.PluginSkipped:
			lines = append(lines, fmt.Sprintf("  ! %s skipped: %s", result.Key, result.Note))
		case engine.PluginNoop:
		}
	}

	for _, result := range farm {
		switch result.Action {
		case engine.FarmLinked:
			lines = append(lines, fmt.Sprintf("  ⇢ %-13s %s: linked %d", result.Agent, result.Plugin, result.Count))
		case engine.FarmPruned:
			lines = append(lines, fmt.Sprintf("  ⇠ %-13s %s: pruned %d", result.Agent, result.Plugin, result.Count))
		case engine.FarmSkipped:
			lines = append(lines, fmt.Sprintf("  ! %-13s %s: skipped (%s)", result.Agent, result.Plugin, result.Note))
		case engine.FarmNoop:
		}
	}

	return lines
}

func printKind(w io.Writer, kr engine.KindReport, conflicts []state.Conflict) {
	var lines []string

	for _, change := range kr.Pulled {
		lines = append(lines, fmt.Sprintf("  ← %-13s %s%s", change.Agent, opSymbol(change.Op), change.Key))
	}

	for _, result := range kr.Agents {
		switch result.Action {
		case engine.ActionPushed, engine.ActionWouldPush:
			line := fmt.Sprintf("  → %-13s %s", result.Agent, summarizeChanges(kr.Kind, result.Changes))
			if result.Action == engine.ActionWouldPush {
				line += " (dry run)"
			}

			if result.Note != "" {
				line += " — " + result.Note
			}

			lines = append(lines, line)
		case engine.ActionSkipped, engine.ActionError, engine.ActionAlias:
			lines = append(lines, fmt.Sprintf("  ! %-13s %s: %s", result.Agent, result.Action, result.Note))
		case engine.ActionNoop, engine.ActionPullOnly:
		}
	}

	for _, c := range conflicts {
		lines = append(lines, fmt.Sprintf("  ⚠ %-13s conflict %s on %s (%s)", c.Agent, c.ID(), c.TargetKey(), c.Reason))
	}

	if kr.Err != "" {
		lines = append(lines, "  ✗ "+kr.Err)
	}

	if len(lines) == 0 {
		fmt.Fprintf(w, "%-12s in sync\n", kr.Kind)

		return
	}

	fmt.Fprintf(w, "%s\n", kr.Kind)

	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
}

func summarizeChanges(k kind.ID, changes []engine.ItemChange) string {
	var names []string

	for _, change := range changes {
		name := opSymbol(change.Op) + change.Key

		if k == kind.Skills {
			group, _, _ := strings.Cut(change.Key, "/")
			name = "~" + group
		}

		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}

	if len(names) > maxListed {
		return strings.Join(names[:maxListed], " ") + fmt.Sprintf(" and %d more", len(names)-maxListed)
	}

	return strings.Join(names, " ")
}

func opSymbol(op string) string {
	switch op {
	case engine.OpAdded:
		return "+"
	case engine.OpDeleted:
		return "-"
	default:
		return "~"
	}
}
