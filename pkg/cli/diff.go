package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/spf13/cobra"

	"github.com/odiumuniverse/agents-sync/pkg/engine"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
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

	cmd.Flags().StringSliceVar(&kinds, "kind", nil, "only these kinds (rules, mcp, skills, permissions, memory, projects)")
	cmd.Flags().StringVar(&agentID, "agent", "", "only this agent")

	return cmd
}

func printDiff(w io.Writer, report *engine.Report, agentID string) {
	empty := true

	for _, kr := range report.Kinds {
		for _, change := range kr.Pulled {
			if agentID != "" && change.Agent != agentID {
				continue
			}

			empty = false

			fmt.Fprintf(w, "%s: %s changed %s%s (goes into the vault on sync)\n", kr.Kind, change.Agent, opSymbol(change.Op), change.Key)
		}

		for _, result := range kr.Agents {
			if agentID != "" && result.Agent != agentID {
				continue
			}

			for _, change := range result.Changes {
				empty = false

				printItemDiff(w, kr.Kind, result.Agent, change)
			}
		}
	}

	for _, c := range report.Conflicts {
		if agentID != "" && c.Agent != agentID {
			continue
		}

		empty = false

		fmt.Fprintf(w, "%s: conflict %s on %s with %s (%s): agent-sync conflicts %s\n", c.Kind, c.ID(), c.TargetKey(), c.Agent, c.Reason, c.ID())
	}

	if empty {
		fmt.Fprintln(w, "no differences")
	}
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
