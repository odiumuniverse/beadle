package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func (a *app) newDiffCmd() *cobra.Command {
	var (
		kinds   []string
		agentID string
	)

	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Show what a sync would change, item by item",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ids, err := parseKinds(kinds)
			if err != nil {
				return err
			}

			e, err := a.engine()
			if err != nil {
				return err
			}

			report, err := e.Sync(cmd.Context(), engine.SyncOptions{DryRun: true, Kinds: ids})
			if err != nil {
				return err
			}

			printDiff(cmd.OutOrStdout(), report, agentID)

			return nil
		},
	}

	cmd.Flags().StringSliceVar(&kinds, "kind", nil, "only these kinds (rules, mcp, skills, permissions, memory, projects, subagents, commands)")
	cmd.Flags().StringVar(&agentID, "agent", "", "only this agent")

	return cmd
}

func printDiff(w io.Writer, report *engine.Report, agentID string) {
	printed := printReportErrors(w, report)

	for _, kr := range report.Kinds {
		if printKindDiff(w, kr, agentID) {
			printed = true
		}
	}

	for _, c := range report.Conflicts {
		if agentID != "" && c.Agent != agentID {
			continue
		}

		printed = true

		fmt.Fprintf(w, "%s: conflict %s on %s with %s (%s): beadle conflicts %s\n", c.Kind, c.ID(), c.TargetKey(), c.Agent, c.Reason, c.ID())
	}

	if !printed {
		fmt.Fprintln(w, "no differences")
	}
}

// printKindDiff renders one kind's pending changes and reports whether it
// printed anything. A surface a sync will not write (a missing config file the
// host never initializes) is marked instead of shown as pending: its item diff
// is empty.
func printKindDiff(w io.Writer, kr engine.KindReport, agentID string) bool {
	printed := false

	for _, change := range kr.Pulled {
		if agentID != "" && change.Agent != agentID {
			continue
		}

		printed = true

		fmt.Fprintf(w, "%s: %s changed %s%s (goes into the vault on sync)\n", kr.Kind, change.Agent, opSymbol(change.Op), change.Key)
	}

	for _, result := range kr.Agents {
		if agentID != "" && result.Agent != agentID {
			continue
		}

		for _, change := range result.Changes {
			printed = true

			printItemDiff(w, kr.Kind, result.Agent, change)
		}

		if result.Action == engine.ActionSkipped && result.Note != "" {
			printed = true

			fmt.Fprintf(w, "%s: %s skipped: %s\n", kr.Kind, result.Agent, result.Note)
		}
	}

	return printed
}

func printReportErrors(w io.Writer, report *engine.Report) bool {
	printed := false

	for _, kr := range report.Kinds {
		if kr.Err == "" {
			continue
		}

		printed = true

		fmt.Fprintf(w, "%s: error: %s\n", kr.Kind, kr.Err)
	}

	return printed
}

func printItemDiff(w io.Writer, k kind.ID, agentID string, change engine.ItemChange) {
	fmt.Fprintf(w, "--- %s → %s: %s %s\n", k, agentID, change.Op, change.Key)

	if !kind.IsText(change.Before) || !kind.IsText(change.After) {
		fmt.Fprintln(w, "(binary content)")

		return
	}

	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(readable(k, change.Before)),
		B:        difflib.SplitLines(readable(k, change.After)),
		FromFile: "agent:" + agentID,
		ToFile:   "vault",
		Context:  3,
	})
	if err != nil || diff == "" {
		return
	}

	fmt.Fprint(w, diff)

	if !strings.HasSuffix(diff, "\n") {
		fmt.Fprintln(w)
	}
}

func readable(k kind.ID, data []byte) string {
	if data == nil {
		return ""
	}

	if k == kind.MCP {
		var out bytes.Buffer

		if err := json.Indent(&out, data, "", "  "); err == nil {
			return out.String() + "\n"
		}
	}

	return string(data)
}
