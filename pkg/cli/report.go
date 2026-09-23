package cli

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
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

	if lines := digestLines(report.Digest); len(lines) > 0 {
		fmt.Fprintln(w, "\ndigest")

		for _, line := range lines {
			fmt.Fprintln(w, line)
		}
	}

	if lines := inboxLines(report.Kinds); len(lines) > 0 {
		fmt.Fprintln(w, "\ninbox")

		for _, line := range lines {
			fmt.Fprintln(w, line)
		}
	}

	if n := len(report.Conflicts); n > 0 {
		fmt.Fprintf(w, "\n%d open conflict(s): review with `beadle conflicts`, settle with `beadle resolve <id> --take vault|agent`\n", n)
	}

	printRulingEvents(w, report)

	printBundleSection(w, report.Bundles)

	for _, note := range report.Notes {
		fmt.Fprintf(w, "note: %s\n", note)
	}

	for _, warning := range report.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warning)
	}
}

// bundleLines renders one line per bundle action; the same lines serve
// `beadle sync`, `beadle init` and `beadle bundles`.
func bundleLines(results []engine.BundleResult) []string {
	var lines []string

	for _, result := range results {
		line := fmt.Sprintf("  %-13s %s", result.Host, result.Action)

		if result.Auto {
			line += " (auto)"
		}

		if result.Version != "" {
			line += " " + result.Version
		}

		if result.Registered {
			line += " (registered)"
		}

		if result.Tier != "" {
			line += " " + result.Tier
		}

		if result.Note != "" {
			line += ": " + result.Note
		}

		lines = append(lines, line)

		if len(result.Withdrawn) > 0 {
			lines = append(lines, "    withdrew: "+strings.Join(result.Withdrawn, ", "))
		}

		if len(result.Kept) > 0 {
			lines = append(lines, "    kept: "+strings.Join(result.Kept, ", "))
		}
	}

	return lines
}

// printBundleSection renders the bundle lines of a report with a header.
func printBundleSection(w io.Writer, results []engine.BundleResult) {
	lines := bundleLines(results)
	if len(lines) == 0 {
		return
	}

	fmt.Fprintln(w, "\nbundles")

	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
}

func inboxLines(kinds []engine.KindReport) []string {
	consumed := map[string]int{}
	skipped := map[string]int{}

	for _, kr := range kinds {
		for _, result := range kr.Inbox {
			switch result.Action {
			case engine.InboxCreated, engine.InboxWouldCreate:
				consumed[result.Agent]++
			case engine.InboxSkipped:
				skipped[result.Agent]++
			}
		}
	}

	agents := map[string]struct{}{}
	for agentID := range consumed {
		agents[agentID] = struct{}{}
	}

	for agentID := range skipped {
		agents[agentID] = struct{}{}
	}

	var lines []string

	for _, agentID := range slices.Sorted(maps.Keys(agents)) {
		lines = append(lines, fmt.Sprintf("  inbox: %-13s consumed=%d skipped=%d", agentID, consumed[agentID], skipped[agentID]))
	}

	return lines
}

func digestLines(results []engine.DigestResult) []string {
	var lines []string

	for _, result := range results {
		lines = append(lines, fmt.Sprintf("  %-13s %s: %s", result.Agent, result.Path, result.Action))
	}

	return lines
}

func pluginLines(results []engine.PluginResult, farm []engine.FarmResult) []string {
	var lines []string

	for _, result := range results {
		switch result.Action {
		case engine.PluginCreated, engine.PluginRepointed:
			lines = append(lines, fmt.Sprintf("  → %s %s %s", result.Key, result.Version, result.Action))
		case engine.PluginQuarantined:
			lines = append(lines, fmt.Sprintf("  ✗ %s %s quarantined: %s", result.Key, result.Version, result.Note))
		case engine.PluginSkipped:
			lines = append(lines, fmt.Sprintf("  ! %s skipped: %s", result.Key, result.Note))
		case engine.PluginRetired:
			lines = append(lines, fmt.Sprintf("  ✂ %s retired: %s", result.Key, result.Note))
		case engine.PluginNoop:
		}
	}

	for _, result := range farm {
		switch result.Action {
		case engine.FarmLinked:
			lines = append(lines, fmt.Sprintf("  ⇢ %-13s %-9s %s: linked %d", result.Agent, result.Kind, result.Plugin, result.Count))
		case engine.FarmStubbed:
			lines = append(lines, fmt.Sprintf("  ⚑ %-13s %-9s %s: stubbed %d", result.Agent, result.Kind, result.Plugin, result.Count))
		case engine.FarmPruned:
			lines = append(lines, fmt.Sprintf("  ⇠ %-13s %-9s %s: pruned %d", result.Agent, result.Kind, result.Plugin, result.Count))
		case engine.FarmSkipped:
			lines = append(lines, fmt.Sprintf("  ! %-13s %-9s %s: skipped (%s)", result.Agent, result.Kind, result.Plugin, result.Note))
		case engine.FarmNoop:
		}
	}

	return lines
}

func pushLines(k kind.ID, result engine.AgentResult) []string {
	line := fmt.Sprintf("  → %-13s %s", result.Agent, summarizeChanges(k, result.Changes))
	if result.Action == engine.ActionWouldPush {
		line += " (dry run)"
	}

	if result.Note != "" {
		line += " — " + result.Note
	}

	lines := []string{line}

	if result.ReloadHint != "" {
		lines = append(lines, "  ↻ "+result.ReloadHint)
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
			lines = append(lines, pushLines(kr.Kind, result)...)
		case engine.ActionSkipped, engine.ActionError, engine.ActionAlias:
			lines = append(lines, fmt.Sprintf("  ! %-13s %s: %s", result.Agent, result.Action, result.Note))
		case engine.ActionNoop, engine.ActionPullOnly:
		}
	}

	for _, c := range conflicts {
		lines = append(lines, fmt.Sprintf("  ⚠ %-13s conflict %s on %s (%s)", c.Agent, c.ID(), c.TargetKey(), c.Reason))
	}

	for _, warning := range kr.Warnings {
		lines = append(lines, "  ! "+warning)
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

		if k == kind.Skills || k == kind.Memory || k == kind.Projects {
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

func printRulingEvents(w io.Writer, report *engine.Report) {
	for _, event := range report.RulingsApplied {
		fmt.Fprintf(w, "ruling applied: %s %s on %s (%s)\n", event.Ruling, event.Signature.Hash(), event.Key, event.Signature.Scope)
	}

	for _, event := range report.RulingSuggestions {
		fmt.Fprintf(w, "ruling suggests %s for %s (run `beadle rulings show %s`)\n", event.Ruling, event.Signature.Target, event.Signature.Hash())
	}

	for _, event := range report.RulingsDemoted {
		fmt.Fprintf(w, "ruling demoted: %s for %s was resolved against (run `beadle rulings show %s`)\n", event.Ruling, event.Signature.Target, event.Signature.Hash())
	}
}
