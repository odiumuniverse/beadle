package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func (a *app) newHealCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "heal",
		Short: "Remove quarantined plugin artifacts and their stubs",
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
				fmt.Fprintln(out, "no quarantine entries")

				return nil
			}

			for _, result := range results {
				line := fmt.Sprintf("  ✓ %s stubs=%d", result.Key, result.Stubs)
				if result.Note != "" {
					line += " — " + result.Note
				}

				fmt.Fprintln(out, line)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be removed without removing anything")

	return cmd
}
