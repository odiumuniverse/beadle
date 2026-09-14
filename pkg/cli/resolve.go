package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	syncer "github.com/odiumuniverse/agents-sync/pkg/sync"
)

func (a *app) newResolveCmd() *cobra.Command {
	var keepAgent bool

	var agent string

	cmd := &cobra.Command{
		Use:   "resolve <rules|mcp|skills|permissions>",
		Short: "Clear a conflict and propagate the resolved state to agents",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			switch resource {
			case syncer.ResourceRules, syncer.ResourceMCP, syncer.ResourceSkills, syncer.ResourcePermissions:
			default:
				return fmt.Errorf("unknown resource %q (expected rules, mcp, skills or permissions)", resource)
			}

			engine, err := a.engine()
			if err != nil {
				return err
			}

			if err := engine.Resolve(cmd.Context(), resource, syncer.ResolveOptions{KeepAgent: keepAgent, Agent: agent}); err != nil {
				return err
			}

			cmd.Println("resolved")

			return nil
		},
	}

	cmd.Flags().BoolVar(&keepAgent, "keep-agent", false, "prefer the agent variant (mcp, skills, permissions)")
	cmd.Flags().StringVar(&agent, "agent", "", "agent whose variant wins (mcp, skills, permissions)")

	return cmd
}
