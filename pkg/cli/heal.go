package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func (a *app) newHealCmd() *cobra.Command {
	// The command is kept for one release as a redirect: it only existed
	// because the farm could dangle, and the farm is gone. It exits 0 — a user
	// typing an old workflow must not get an error for using it once.
	return &cobra.Command{
		Use:        "heal",
		Short:      "The plugin farm is gone; check the plugin manager instead",
		Deprecated: "the plugin farm is gone; use beadle plugins list",
		Args:       cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			fmt.Fprintln(out, "the plugin farm is gone; run beadle plugins list, or `verger status` for the full matrix")
			fmt.Fprintln(out, "if you are migrating, the backup of your vault state is under <vault>/state/backups/")

			return nil
		},
	}
}
