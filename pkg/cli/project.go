package cli

import (
	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/engine"
)

func (a *app) newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage the current project scope (one git repository or directory)",
		Long: "Project files (.mcp.json, .cursor/mcp.json, .cursor/rules/*.mdc,\n" +
			".agents/mcp_config.json, AGENTS.md, GEMINI.md) sync only when enabled in\n" +
			"the vault-side policy of the current project. Nothing is touched without it.",
	}

	cmd.AddCommand(
		a.newProjectStatusCmd(),
		a.newProjectEnableCmd(),
		a.newProjectDisableCmd(),
		a.newProjectForgetCmd(),
	)

	return cmd
}

func (a *app) newProjectStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the project identity and the state of every project file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}

			status, err := eng.ProjectStatus(cmd.Context())
			if err != nil {
				return err
			}

			cmd.Printf("project: %s\n", status.ID)

			switch {
			case status.Remote != "":
				cmd.Printf("remote: %s\n", status.Remote)
			case status.Slugs:
				cmd.Printf("remote: none (path slug; not a git checkout)\n")
			default:
				cmd.Printf("remote: none\n")
			}

			cmd.Printf("%-26s %-8s %-12s %s\n", "FILE", "ENABLED", "PUBLISHABLE", "PRESENT")

			for _, file := range status.Files {
				cmd.Printf("%-26s %-8s %-12s %s\n",
					file.Rel, yesNo(file.Enabled), yesNo(file.Publishable), yesNo(file.Present))
			}

			return nil
		},
	}
}

func (a *app) newProjectEnableCmd() *cobra.Command {
	var (
		allowSecrets bool
		servers      []string
	)

	cmd := &cobra.Command{
		Use:   "enable <file>",
		Short: "Enable a project file in the vault-side policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}

			if _, err := eng.ProjectEnable(cmd.Context(), args[0], engine.ProjectOptions{
				AllowSecrets: allowSecrets,
				KeepSecrets:  !cmd.Flags().Changed("allow-secrets"),
				Servers:      servers,
			}); err != nil {
				return err
			}

			cmd.Printf("enabled %s; run beadle sync to materialize it\n", args[0])

			return nil
		},
	}

	cmd.Flags().BoolVar(&allowSecrets, "allow-secrets", false, "materialize secret values even in a git-tracked directory")
	cmd.Flags().StringArrayVar(&servers, "server", nil, "restrict materialized secrets to these MCP server names (repeatable)")

	return cmd
}

func (a *app) newProjectDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <file>",
		Short: "Stop syncing a project file (the files on disk stay as they are)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}

			if _, err := eng.ProjectDisable(cmd.Context(), args[0]); err != nil {
				return err
			}

			cmd.Printf("disabled %s\n", args[0])

			return nil
		},
	}
}

func (a *app) newProjectForgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget <file>",
		Short: "Drop a project file from the canon, the policy and the sync base, then delete it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := a.engine()
			if err != nil {
				return err
			}

			report, err := eng.ProjectForget(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			cmd.Printf("forgot %s\n", args[0])

			for _, warning := range report.Warnings {
				cmd.Printf("warning: %s\n", warning)
			}

			return nil
		},
	}
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}

	return "no"
}
