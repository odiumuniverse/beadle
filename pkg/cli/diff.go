package cli

import (
	"maps"
	"slices"

	"github.com/spf13/cobra"

	syncer "github.com/odiumuniverse/agents-sync/pkg/sync"
)

func (a *app) newDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff",
		Short: "Show differences between agents and the vault",
		RunE: func(cmd *cobra.Command, _ []string) error {
			engine, err := a.engine()
			if err != nil {
				return err
			}

			report, err := engine.Diff(cmd.Context())
			if err != nil {
				return err
			}

			for _, id := range agentIDs(report) {
				if diff := report.Rules[id]; diff != "" {
					cmd.Printf("--- rules: agent:%s vs vault\n%s", id, diff)
				}

				for _, change := range report.MCP[id] {
					cmd.Printf("--- mcp: agent:%s %s %s\n", id, change.Status, change.Server)
				}

				for _, change := range report.Skills[id] {
					cmd.Printf("--- skill: agent:%s %s %s\n", id, change.Status, change.Skill)
				}

				for _, change := range report.Permissions[id] {
					cmd.Printf("--- perm: agent:%s %s %s\n", id, change.Status, change.Key)
				}
			}

			return nil
		},
	}
}

func agentIDs(report syncer.DiffReport) []string {
	seen := map[string]struct{}{}

	for id := range report.Rules {
		seen[id] = struct{}{}
	}

	for id := range report.MCP {
		seen[id] = struct{}{}
	}

	for id := range report.Skills {
		seen[id] = struct{}{}
	}

	for id := range report.Permissions {
		seen[id] = struct{}{}
	}

	return slices.Sorted(maps.Keys(seen))
}
