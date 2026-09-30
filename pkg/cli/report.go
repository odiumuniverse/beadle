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

	// One footnote block for the whole run: a reason is read once, after the
	// table that refers to it.
	rs := newReasons()

	for _, kr := range report.Kinds {
		printKind(w, kr, report.ConflictsOf(kr.Kind), rs, report.NoActiveAgents)
	}

	if report.Packages != nil {
		printPackages(w, report.Packages)
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
		fmt.Fprintf(w, "%s %d change(s) could not be applied — review with `beadle conflicts`, settle with `beadle resolve <id> --take vault|agent`\n",
			rs.word(wordBlocked, "an open conflict holds a change back until you settle it"), n)
	}

	rs.print(w)

	printRulingEvents(w, report)

	printBundleSection(w, report.Bundles)

	for _, note := range report.Notes {
		fmt.Fprintf(w, "note: %s\n", note)
	}

	for _, warning := range report.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warning)
	}
}

// printPackages says what the vault's package spec asked for and what this
// machine did. It prints even when there was nothing to apply: "no plugins
// named" and "named, and nothing delivered" are different facts, and a silent
// run is how a vault ends up carrying a spec no machine applies. The heading
// is `plugins` - the word the user types - while the rows keep the package ids
// the spec named.
func printPackages(w io.Writer, packages *state.PackagesReport) {
	if !packages.Spec {
		if packages.LookedAt != "" {
			fmt.Fprintf(w, "plugins: none named (no spec at %s)\n", packages.LookedAt)

			return
		}

		fmt.Fprintln(w, "plugins: none named (the vault carries no package spec)")

		return
	}

	if packages.Error != "" {
		fmt.Fprintf(w, "plugins %s: %s\n", wordBlocked, packages.Error)

		return
	}

	if len(packages.Results) == 0 {
		fmt.Fprintf(w, "plugins %s: the spec named nothing this machine can apply\n", wordSkipped)

		return
	}

	fmt.Fprintln(w, "plugins")

	for _, result := range packages.Results {
		line := fmt.Sprintf("  %-24s %-12s %s", result.Package, result.Host, result.State)
		if result.Note != "" {
			line += "  " + result.Note
		}

		fmt.Fprintln(w, line)
	}
}

// bundleWord is a bundle action in the reader's words. What beadle calls
// `enabled`, `withdrawn` or `executed` is a state of the bundle, and the reader
// only needs to know whether it is in place and what to do about it.
func bundleWord(action string) string {
	switch action {
	case "enabled", "generated":
		return wordDelivered
	case "disabled", "pending", "noop":
		return wordSkipped
	case "failed":
		return wordFailed
	default:
		return action
	}
}

// bundleLines renders one line per bundle action; the same lines serve
// `beadle sync`, `beadle init` and `beadle bundles`.
func bundleLines(results []engine.BundleResult) []string {
	var lines []string

	for _, result := range results {
		line := fmt.Sprintf("  %-13s %s", result.Host, bundleWord(result.Action))

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

// digestLines renders one row per digest result: the agent, where the digest
// went, what happened to it, and the notes that make the line actionable.
func digestLines(results []engine.DigestResult) []string {
	var lines []string

	for _, result := range results {
		line := fmt.Sprintf("  %-13s %s: %s", result.Agent, result.Path, result.Action)

		lines = append(lines, line)
	}

	return lines
}

// inboxLines summarises the inbox across every resource: how many items each
// agent consumed and how many it skipped. A count per agent, not a row per
// item - a vault with fifty memories would otherwise bury the rest of the
// report under fifty lines that all say the same thing.
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

// pushLines renders one resource's push to one agent: the agent, what changed,
// and the host's own reload note when it has one. The arrow marks the row as
// something that was written, which is the fact a reader is scanning for.
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

// printKind writes one resource's block. Every line carries a word the reader
// can act on, and a line that is not `delivered` points at the footnote that
// says why - the reason is never an internal action name.
func printKind(w io.Writer, kr engine.KindReport, conflicts []state.Conflict, rs *reasons, noActiveAgents bool) {
	var lines []string

	// A change the user made and beadle took in: their edit, reported as such.
	for _, change := range kr.Pulled {
		lines = append(lines, fmt.Sprintf("  %-13s %s %s", wordYourEdit, change.Agent, change.Key))
	}

	for _, result := range kr.Agents {
		switch result.Action {
		case engine.ActionPushed, engine.ActionWouldPush:
			lines = append(lines, pushLines(kr.Kind, result)...)
		case engine.ActionSkipped, engine.ActionAlias:
			reason := result.Note
			if reason == "" {
				reason = fmt.Sprintf("nothing was written for %s here", result.Agent)
			}

			lines = append(lines, fmt.Sprintf("  %-13s %s: %s",
				rs.word(wordSkipped, reason), result.Agent, result.Note))
		case engine.ActionError:
			lines = append(lines, fmt.Sprintf("  %-13s %s: %s",
				rs.word(wordFailed, fmt.Sprintf("%s: %s", kr.Kind, result.Note)), result.Agent, result.Note))
		case engine.ActionNoop, engine.ActionPullOnly:
		}
	}

	for _, c := range conflicts {
		lines = append(lines, fmt.Sprintf("  %-13s %s on %s — %s",
			rs.word(wordBlocked, fmt.Sprintf("a conflict on %s: %s", c.TargetKey(), c.Reason)),
			c.Agent, c.TargetKey(), c.Reason))
	}

	for _, warning := range kr.Warnings {
		lines = append(lines, "  "+rs.word(wordBlocked, warning))
	}

	if kr.Err != "" {
		lines = append(lines, "  "+rs.word(wordFailed, kr.Err))
	}

	if len(lines) == 0 {
		printQuietKind(w, kr, noActiveAgents)

		return
	}

	fmt.Fprintf(w, "%s\n", kr.Kind)

	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
}

// printQuietKind writes the line for a kind that has nothing to report line by
// line. What it says is a claim about the run, not a filler: `delivered` only
// when a file was written, `in sync` when the vault and the hosts already agree,
// `skipped` with the reason when there was no agent to write to. The JSON
// document reads the same decision (kindStatus), so the two channels cannot
// disagree about one run.
func printQuietKind(w io.Writer, kr engine.KindReport, noActiveAgents bool) {
	word, detail := kindStatus(kr, noActiveAgents)
	if detail != "" {
		fmt.Fprintf(w, "%-12s %s — %s\n", kr.Kind, word, detail)

		return
	}

	fmt.Fprintf(w, "%-12s %s\n", kr.Kind, word)
}

func summarizeChanges(k kind.ID, changes []engine.ItemChange) string {
	var names []string

	for _, change := range changes {
		name := change.Key

		if k == kind.Skills || k == kind.Memory || k == kind.Projects {
			group, _, _ := strings.Cut(change.Key, "/")
			name = group
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
