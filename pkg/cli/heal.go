package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func (a *app) newHealCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "heal",
		Short: "Heal quarantined plugins and migrate versioned skill links to pivots",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := a.engine()
			if err != nil {
				return err
			}

			results, err := e.Heal(cmd.Context(), dryRun)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			if len(results) == 0 {
				fmt.Fprintln(out, "nothing to heal")

				return nil
			}

			for _, result := range results {
				line := "  ✓ " + result.Key

				if result.Stubs > 0 {
					line += fmt.Sprintf(" stubs=%d", result.Stubs)
				}

				if result.Migrated > 0 {
					line += fmt.Sprintf(" migrated=%d", result.Migrated)
				}

				if result.Cleaned > 0 {
					line += fmt.Sprintf(" cleaned=%d", result.Cleaned)
				}

				if result.Retired > 0 {
					line += fmt.Sprintf(" retired=%d", result.Retired)
				}

				if result.Pruned > 0 {
					line += fmt.Sprintf(" pruned=%d", result.Pruned)
				}

				if result.Note != "" {
					line += " — " + result.Note
				}

				fmt.Fprintln(out, line)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be migrated or removed without changing anything")

	return cmd
}
