package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/skills"
)

func (a *app) newSkillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Manage the skills that ship with the vault",
	}

	var force bool

	seed := &cobra.Command{
		Use:   "seed",
		Short: "Copy the built-in beadle-conflicts skill into the vault canon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			v, err := a.resolveVault()
			if err != nil {
				return err
			}

			written, err := skills.Seed(v.SkillsDir(), force)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			if !written {
				fmt.Fprintln(out, "beadle-conflicts is already in the vault (use --force to overwrite)")

				return nil
			}

			fmt.Fprintln(out, "seeded beadle-conflicts")

			return nil
		},
	}

	seed.Flags().BoolVar(&force, "force", false, "overwrite an existing skill")
	cmd.AddCommand(seed)

	return cmd
}
