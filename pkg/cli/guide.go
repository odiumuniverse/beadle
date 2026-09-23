package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/guide"
)

func (a *app) newGuideCmd() *cobra.Command {
	var humans bool

	cmd := &cobra.Command{
		Use:   "guide",
		Short: "Print the beadle guide (AI agents by default, humans with --humans)",
		Long: "Print the beadle guide. Without flags it prints the guide for AI agents:\n" +
			"the rules, the defaults, the plugin flow and the workflow an agent needs to\n" +
			"work with the vault safely. `--humans` prints the human guide.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			text := guide.AI()
			if humans {
				text = guide.Humans()
			}

			_, err := fmt.Fprint(cmd.OutOrStdout(), text)

			return err
		},
	}

	cmd.Flags().BoolVar(&humans, "humans", false, "print the human guide instead of the AI one")

	return cmd
}
