package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func (a *app) newAgentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "List agents and choose what is synchronized with each of them",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, cfg, err := a.loadConfig()
			if err != nil {
				return err
			}

			agents, err := allAgents()
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			if err := printAgents(out, cfg, agents); err != nil {
				return err
			}

			fmt.Fprintln(out)
			printModes(out, cfg, agents)

			return nil
		},
	}

	cmd.AddCommand(a.newAgentsToggleCmd(true), a.newAgentsToggleCmd(false), a.newAgentsModeCmd())

	return cmd
}

func (a *app) newAgentsToggleCmd(enable bool) *cobra.Command {
	use, short := "disable <agent>...", "Stop synchronizing agents"
	if enable {
		use, short = "enable <agent>...", "Start synchronizing agents"
	}

	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, cfg, err := a.loadConfig()
			if err != nil {
				return err
			}

			agents, err := allAgents()
			if err != nil {
				return err
			}

			if err := validateAgentIDs(agents, args); err != nil {
				return err
			}

			for _, id := range args {
				if enable {
					cfg.Enable(id)
				} else {
					cfg.Disable(id)
				}
			}

			if err := cfg.Save(v.ConfigPath()); err != nil {
				return err
			}

			if enable {
				fmt.Fprintln(cmd.OutOrStdout(), "enabled; preview the first sync with: beadle sync --dry-run")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "disabled; its files are left as they are")
			}

			return nil
		},
	}
}

func (a *app) newAgentsModeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mode <agent> <kind> <sync|pull|push|off>",
		Short: "Choose the direction of one kind for one agent",
		Long: "sync  both directions (default)\n" +
			"pull  only take the agent's changes into the vault; never write the agent\n" +
			"push  only write the vault into the agent; its local edits are overwritten\n" +
			"off   ignore this kind for the agent",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, cfg, err := a.loadConfig()
			if err != nil {
				return err
			}

			agents, err := allAgents()
			if err != nil {
				return err
			}

			ag := agent.ByID(agents, args[0])
			if ag == nil {
				return fmt.Errorf("unknown agent %q", args[0])
			}

			k, err := kind.Parse(args[1])
			if err != nil {
				return err
			}

			if ag.Surface(k) == nil {
				return fmt.Errorf("%s has no %s to synchronize", ag.Name, k)
			}

			mode, err := config.ParseMode(args[2])
			if err != nil {
				return err
			}

			cfg.SetMode(ag.ID, k, mode)

			if err := cfg.Save(v.ConfigPath()); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s\n", ag.ID, k, mode)

			return nil
		},
	}
}

func (a *app) newKindsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kinds",
		Short: "List resource kinds and switch them on or off for every agent",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, cfg, err := a.loadConfig()
			if err != nil {
				return err
			}

			for _, spec := range kind.All() {
				fmt.Fprintf(cmd.OutOrStdout(), "  %-12s %s\n", spec.ID, onOff(cfg.KindEnabled(spec.ID), "on", "off"))
			}

			return nil
		},
	}

	cmd.AddCommand(a.newKindsToggleCmd(true), a.newKindsToggleCmd(false))

	return cmd
}

func (a *app) newKindsToggleCmd(enable bool) *cobra.Command {
	use, short, mode := "disable <kind>...", "Stop synchronizing kinds", config.ModeOff
	if enable {
		use, short, mode = "enable <kind>...", "Start synchronizing kinds", config.ModeSync
	}

	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, cfg, err := a.loadConfig()
			if err != nil {
				return err
			}

			ids, err := parseKinds(args)
			if err != nil {
				return err
			}

			for _, id := range ids {
				cfg.SetKind(id, mode)
			}

			if err := cfg.Save(v.ConfigPath()); err != nil {
				return err
			}

			fmt.Fprintln(cmd.OutOrStdout(), "saved")

			return nil
		},
	}
}
