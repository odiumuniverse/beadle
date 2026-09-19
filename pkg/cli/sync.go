package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
)

func (a *app) newSyncCmd() *cobra.Command {
	var (
		dryRun  bool
		kinds   []string
		refresh bool
	)

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Take agent changes into the vault and write the result into every agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runSync(cmd, engine.SyncOptions{DryRun: dryRun, Refresh: refresh}, kinds)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without writing anything")
	cmd.Flags().StringSliceVar(&kinds, "kind", nil, "only these kinds (rules, mcp, skills, permissions, memory, projects)")
	cmd.Flags().BoolVar(&refresh, "refresh-digest", false, "overwrite the frozen memory digest block even if it was edited by hand")

	return cmd
}

func (a *app) newPullCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Take agent changes into the vault without writing any agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runSync(cmd, engine.SyncOptions{DryRun: dryRun, Direction: config.ModePull}, nil)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without writing anything")

	return cmd
}

func (a *app) newPushCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "push",
		Short: "Write the vault into every agent, overwriting their local changes",
		Long: "push makes every agent hold exactly the vault content. Changes made in an agent\n" +
			"since the last sync are overwritten, not merged: use `beadle sync` for that.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runSync(cmd, engine.SyncOptions{DryRun: dryRun, Direction: config.ModePush}, nil)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without writing anything")

	return cmd
}

func (a *app) runSync(cmd *cobra.Command, opts engine.SyncOptions, kindNames []string) error {
	ids, err := parseKinds(kindNames)
	if err != nil {
		return err
	}

	opts.Kinds = ids

	e, err := a.engine()
	if err != nil {
		return err
	}

	report, err := e.Sync(cmd.Context(), opts)
	if err != nil {
		return err
	}

	printReport(cmd.OutOrStdout(), report)

	if errs := report.Errors(); len(errs) > 0 {
		return fmt.Errorf("%d error(s) during sync", len(errs))
	}

	return nil
}
