package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/agents-sync/pkg/agent"
	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/engine"
	"github.com/odiumuniverse/agents-sync/pkg/state"
	"github.com/odiumuniverse/agents-sync/pkg/vault"
)

func (a *app) newStatusCmd() *cobra.Command {
	var check bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show agents, synchronization modes and open conflicts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			root, err := vault.ResolveRoot(a.vaultPath, os.Getenv(vault.EnvHome))
			if err != nil {
				return err
			}

			v := vault.New(root)

			fmt.Fprintf(out, "vault: %s\n", v.Root())

			if !v.Initialized() {
				fmt.Fprintln(out, "state: not initialized (run agent-sync init)")

				return nil
			}

			cfg, err := config.Load(v.ConfigPath())
			if err != nil {
				return err
			}

			agents, err := allAgents()
			if err != nil {
				return err
			}

			if err := printAgents(out, cfg, agents); err != nil {
				return err
			}

			st, err := state.Load(v.StatePath())
			if err != nil {
				return err
			}

			if conflicts := st.OpenConflicts(); len(conflicts) > 0 {
				fmt.Fprintf(out, "conflicts: %d open (agent-sync conflicts)\n", len(conflicts))
			} else {
				fmt.Fprintln(out, "conflicts: none")
			}

			if !check {
				fmt.Fprintln(out, "pending changes: agent-sync status --check (or agent-sync diff)")

				return nil
			}

			e, err := a.engine()
			if err != nil {
				return err
			}

			report, err := e.Sync(cmd.Context(), engine.SyncOptions{DryRun: true})
			if err != nil {
				return err
			}

			fmt.Fprintln(out)
			printReport(out, report)

			return nil
		},
	}

	cmd.Flags().BoolVar(&check, "check", false, "also compute pending changes (dry run)")

	return cmd
}

func printAgents(out interface{ Write([]byte) (int, error) }, cfg *config.Config, agents []*agent.Agent) error {
	fmt.Fprintln(out, "agents:")

	for _, ag := range agents {
		detected, err := ag.Detect()
		if err != nil {
			return err
		}

		fmt.Fprintf(out, "  %-12s %-9s %-14s %s\n", ag.ID,
			onOff(cfg.Agents[ag.ID].Enabled, "enabled", "disabled"),
			onOff(detected, "installed", "not installed"),
			modesOf(cfg, ag))
	}

	return nil
}

func modesOf(cfg *config.Config, ag *agent.Agent) string {
	parts := make([]string, 0, len(ag.Surfaces))

	for _, surface := range ag.Surfaces {
		mode := cfg.ModeFor(ag.ID, surface.Kind(), surface.Traits().DefaultMode)
		if !cfg.KindEnabled(surface.Kind()) {
			mode = config.ModeOff
		}

		parts = append(parts, fmt.Sprintf("%s:%s", surface.Kind(), mode))
	}

	return strings.Join(parts, " ")
}

func onOff(value bool, yes, no string) string {
	if value {
		return yes
	}

	return no
}
