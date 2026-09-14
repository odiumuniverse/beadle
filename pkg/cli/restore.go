package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/agents-sync/pkg/cas"
	syncer "github.com/odiumuniverse/agents-sync/pkg/sync"
)

func (a *app) newRestoreCmd() *cobra.Command {
	var rev string

	cmd := &cobra.Command{
		Use:   "restore <rules|mcp|skills|permissions>",
		Short: "Restore a resource from its base snapshot and push it to agents",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]

			switch resource {
			case syncer.ResourceRules, syncer.ResourceMCP, syncer.ResourceSkills, syncer.ResourcePermissions:
			default:
				return fmt.Errorf("unknown resource %q (expected rules, mcp, skills or permissions)", resource)
			}

			opts := syncer.RestoreOptions{}

			if rev != "" {
				hash, err := cas.ParseHash(rev)
				if err != nil {
					return err
				}

				opts.Rev = hash
			}

			engine, err := a.engine()
			if err != nil {
				return err
			}

			if err := engine.Restore(cmd.Context(), resource, opts); err != nil {
				return err
			}

			cmd.Println("restored")

			return nil
		},
	}

	cmd.Flags().StringVar(&rev, "rev", "", "restore a specific blob hash instead of the last base")

	return cmd
}
