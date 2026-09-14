package cli

import (
	"maps"
	"slices"

	"github.com/spf13/cobra"

	syncer "github.com/odiumuniverse/agents-sync/pkg/sync"
)

func (a *app) newSyncCmd() *cobra.Command {
	var prune bool

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Pull agent changes, merge them into the vault and push the result back",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runSync(cmd, syncer.ModeSync, prune)
		},
	}

	cmd.Flags().BoolVar(&prune, "prune", false, "delete agent skills that are absent from the vault")

	return cmd
}

func (a *app) newPullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Short: "Pull agent changes into the vault only",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runSync(cmd, syncer.ModePull, false)
		},
	}
}

func (a *app) newPushCmd() *cobra.Command {
	var prune bool

	cmd := &cobra.Command{
		Use:   "push",
		Short: "Push vault content to agents only",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runSync(cmd, syncer.ModePush, prune)
		},
	}

	cmd.Flags().BoolVar(&prune, "prune", false, "delete agent skills that are absent from the vault")

	return cmd
}

func (a *app) runSync(cmd *cobra.Command, mode syncer.Mode, prune bool) error {
	var opts []syncer.Option

	if prune {
		opts = append(opts, syncer.WithPrune())
	}

	engine, err := a.engine(opts...)
	if err != nil {
		return err
	}

	report, runErr := engine.Run(cmd.Context(), mode)

	printReport(cmd, report)

	return runErr
}

func printReport(cmd *cobra.Command, report syncer.Report) {
	cmd.Printf("rules:  changed=%t conflicts=%d\n", report.Rules.Changed, report.Rules.ConflictCount())
	cmd.Printf("mcp:    changed=%t conflicts=%d\n", report.MCP.Changed, report.MCP.ConflictCount())
	cmd.Printf("skills: changed=%t conflicts=%d\n", report.Skills.Changed, report.Skills.ConflictCount())
	cmd.Printf("perms:  changed=%t conflicts=%d\n", report.Permissions.Changed, report.Permissions.ConflictCount())

	for _, id := range slices.Sorted(maps.Keys(report.Actions)) {
		actions := report.Actions[id]

		if actions.Rules == syncer.ActionNoop && actions.MCP == syncer.ActionNoop &&
			actions.Skills == syncer.ActionNoop && actions.Permissions == syncer.ActionNoop {
			cmd.Printf("%s: no changes\n", id)

			continue
		}

		cmd.Printf("%s: rules=%s mcp=%s skills=%s perms=%s\n", id,
			actionName(actions.Rules), actionName(actions.MCP), actionName(actions.Skills), actionName(actions.Permissions))
	}

	if report.Rules.ConflictCount() > 0 || report.MCP.ConflictCount() > 0 ||
		report.Skills.ConflictCount() > 0 || report.Permissions.ConflictCount() > 0 {
		cmd.Println("conflicts detected: resolve them with `agent-sync resolve <rules|mcp|skills|permissions>`")
	}
}

func actionName(action string) string {
	if action == "" {
		return "-"
	}

	return action
}
