package cli

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func (a *app) newExplainCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "explain <skill>",
		Short: "Explain how a canon skill reaches every host",
		Long: "Explain lists every copy of a canon skill each active host can read:\n" +
			"the channel the copy belongs to, its digest and its role (the copy that\n" +
			"delivers the skill, a redundant duplicate, or a fork).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := a.engine()
			if err != nil {
				return err
			}

			rows, err := e.Explain(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			if asJSON {
				data, err := json.MarshalIndent(map[string]any{"skill": args[0], "rows": rows}, "", "  ")
				if err != nil {
					return err
				}

				fmt.Fprintln(out, string(data))

				return nil
			}

			table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)

			fmt.Fprintln(table, "host\tchannel\tpath\tdigest\trole")

			for _, row := range rows {
				fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", row.Host, row.Channel, row.Path, row.Digest, row.Role)
			}

			return table.Flush()
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "print the rows as JSON")

	return cmd
}
