package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/skills"
)

func (a *app) newSkillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Manage the skills that ship with the vault",
	}

	cmd.AddCommand(a.newSkillsSeedCmd(), a.newSkillsAdoptCmd(), a.newSkillsUnadoptCmd())

	return cmd
}

func (a *app) newSkillsSeedCmd() *cobra.Command {
	var force bool

	seed := &cobra.Command{
		Use:   "seed",
		Short: "Copy the built-in beadle skills into the vault canon",
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

			if len(written) == 0 {
				fmt.Fprintln(out, "the built-in skills are already in the vault (use --force to overwrite)")

				return nil
			}

			fmt.Fprintln(out, "seeded "+strings.Join(written, ", "))

			return nil
		},
	}

	seed.Flags().BoolVar(&force, "force", false, "overwrite an existing skill")

	return seed
}

func (a *app) newSkillsAdoptCmd() *cobra.Command {
	var (
		host   string
		dryRun bool
	)

	cmd := &cobra.Command{
		Use:   "adopt <skill>",
		Short: "Replace a foreign copy of a canon skill with beadle delivery",
		Long: "Adopt moves the foreign copy of a canon skill (a user symlink or an\n" +
			"unmanaged directory) into the vault stash, so the next sync delivers the\n" +
			"skill through beadle. Nothing is deleted: skills unadopt moves the\n" +
			"original back.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := a.engine()
			if err != nil {
				return err
			}

			report, err := e.Adopt(cmd.Context(), args[0], host, dryRun)
			if err != nil {
				return err
			}

			printAdoptReport(cmd, report)

			out := cmd.OutOrStdout()

			if dryRun {
				fmt.Fprintln(out, "  re-run without --dry-run to adopt")

				return nil
			}

			fmt.Fprintln(out, "  run beadle sync to deliver the canon through beadle")

			return nil
		},
	}

	cmd.Flags().StringVar(&host, "host", "", "agent id or bundle host (default: every active host)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be adopted without touching anything")

	return cmd
}

func (a *app) newSkillsUnadoptCmd() *cobra.Command {
	var (
		host   string
		dryRun bool
	)

	cmd := &cobra.Command{
		Use:   "unadopt <skill>",
		Short: "Move an adopted skill copy back to its surface",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := a.engine()
			if err != nil {
				return err
			}

			report, err := e.Unadopt(cmd.Context(), args[0], host, dryRun)
			if err != nil {
				return err
			}

			printAdoptReport(cmd, report)

			return nil
		},
	}

	cmd.Flags().StringVar(&host, "host", "", "agent id or bundle host (default: every adoption record)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be restored without touching anything")

	return cmd
}

func printAdoptReport(cmd *cobra.Command, report engine.Report) {
	out := cmd.OutOrStdout()

	for _, result := range report.Adoptions {
		line := fmt.Sprintf("  %-13s %s %s", result.Agent, result.Action, result.Name)

		if result.Provider != "" {
			line += " (" + result.Provider + ")"
		}

		if result.Note != "" {
			line += ": " + result.Note
		}

		fmt.Fprintln(out, line)
	}

	for _, warning := range report.Warnings {
		fmt.Fprintln(out, "  ! "+warning)
	}
}
