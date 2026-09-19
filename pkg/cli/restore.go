package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/kind"
)

func (a *app) newHistoryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "history <kind>",
		Short: "List the vault snapshots of a kind",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := kind.Parse(args[0])
			if err != nil {
				return err
			}

			e, err := a.engine()
			if err != nil {
				return err
			}

			history, err := e.History(k)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			if len(history) == 0 {
				fmt.Fprintf(out, "%s has no snapshots yet\n", k)

				return nil
			}

			for i, snap := range history {
				marker := ""
				if i == len(history)-1 {
					marker = "  (current)"
				}

				fmt.Fprintf(out, "%3d  %s  %s%s\n", i-len(history), snap.At.Local().Format(time.DateTime), string(snap.Manifest)[:12], marker)
			}

			fmt.Fprintf(out, "\nrestore one: beadle restore %s --to <number>\n", k)

			return nil
		},
	}
}

func (a *app) newRestoreCmd() *cobra.Command {
	var to int

	cmd := &cobra.Command{
		Use:   "restore <kind>",
		Short: "Restore a kind from a vault snapshot and push it to every agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := kind.Parse(args[0])
			if err != nil {
				return err
			}

			e, err := a.engine()
			if err != nil {
				return err
			}

			report, err := e.Restore(cmd.Context(), k, to)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			fmt.Fprintf(out, "restored %s\n\n", k)
			printReport(out, report)

			return nil
		},
	}

	cmd.Flags().IntVar(&to, "to", -2, "snapshot number from `beadle history` (-1 is current, -2 the previous state)")

	return cmd
}
