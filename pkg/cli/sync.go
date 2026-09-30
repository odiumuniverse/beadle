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
		asJSON  bool
		kinds   []string
		refresh bool
	)

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Take agent changes into the vault and write the result into every agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runSync(cmd, engine.SyncOptions{DryRun: dryRun, Refresh: refresh}, kinds, asJSON)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without writing anything")
	cmd.Flags().StringSliceVar(&kinds, "kind", nil, "only these kinds (rules, mcp, skills, permissions, memory, projects, subagents, commands)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	cmd.Flags().BoolVar(&refresh, "refresh-digest", false, "overwrite the frozen memory digest block even if it was edited by hand")

	return cmd
}

func (a *app) newPullCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Take agent changes into the vault without writing any agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runSync(cmd, engine.SyncOptions{DryRun: dryRun, Direction: config.ModePull}, nil, false)
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
			return a.runSync(cmd, engine.SyncOptions{DryRun: dryRun, Direction: config.ModePush}, nil, false)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without writing anything")

	return cmd
}

// runSync runs the engine and prints its report. asJSON switches the channel to
// the machine document: the same report, in the shared envelope, and nothing
// else on stdout.
func (a *app) runSync(cmd *cobra.Command, opts engine.SyncOptions, kindNames []string, asJSON bool) error {
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

	if asJSON {
		if err := writeJSON(cmd.OutOrStdout(), newSyncDocument(report, opts.Direction)); err != nil {
			return err
		}
	} else {
		printReport(cmd.OutOrStdout(), report)
	}

	if errs := report.Errors(); len(errs) > 0 {
		return fmt.Errorf("%d error(s) during sync", len(errs))
	}

	// A run that left conflicts open did not do what it was asked to do, and
	// the table above already says so in words a human reads. A script reads
	// the exit code, so the refusal has to reach it too: the report is printed
	// first, and the error names the command that settles it.
	//
	// A dry run is the exception, and it is the whole point of one: it writes
	// nothing, and it is what a user runs BEFORE settling anything. Failing it
	// for the conflicts it just displayed would make the preview useless.
	if len(report.Conflicts) > 0 && !opts.DryRun {
		return engine.OpenConflictsError{Conflicts: len(report.Conflicts)}
	}

	return nil
}
