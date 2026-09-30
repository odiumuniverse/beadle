package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func (a *app) newAgentsCmd() *cobra.Command {
	var asJSON bool

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

			if asJSON {
				return writeJSON(out, newAgentsDocument(cfg, agents))
			}

			if err := printAgents(out, cfg, agents); err != nil {
				return err
			}

			fmt.Fprintln(out)
			printModes(out, cfg, agents)

			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "print the document as JSON")

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

			ids, err := resolveAgentIDs(agents, args)
			if err != nil {
				return err
			}

			for _, id := range ids {
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

			out := cmd.OutOrStdout()
			rs := newReasons()

			for _, spec := range kind.All() {
				fmt.Fprintf(out, "  %-12s %s\n", spec.ID, kindWord(rs, cfg, spec.ID))
			}

			rs.print(out)

			return nil
		},
	}

	cmd.AddCommand(a.newKindsToggleCmd(true), a.newKindsToggleCmd(false))

	return cmd
}

// kindWord is one resource's state in the reader's words. A resource that is
// switched on is what beadle keeps in step with every agent; one that is off is
// skipped, with the command that changes it.
func kindWord(rs *reasons, cfg *config.Config, id kind.ID) string {
	if cfg.KindEnabled(id) {
		return wordDelivered
	}

	return rs.word(wordSkipped, fmt.Sprintf("not synchronized — run `beadle kinds enable %s`", id))
}

func (a *app) newKindsToggleCmd(enable bool) *cobra.Command {
	use, short, mode := "disable <kind>...", "Stop synchronizing kinds", config.ModeOff
	if enable {
		use, short, mode = "enable <kind>...", "Start synchronizing kinds", config.ModeSync
	}

	// --agent narrows the change to the agents named, and may be given more
	// than once so one run reaches several. Without it the kind's global
	// default is what changes, which is what this command always did; with it
	// the global default is left alone and only the named agents move, so
	// "turn skills off for cursor" does not quietly turn them off everywhere.
	var agents []string

	cmd := &cobra.Command{
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

			known, err := knownAgentIDs(agents)
			if err != nil {
				return err
			}

			for _, id := range ids {
				if len(known) == 0 {
					cfg.SetKind(id, mode)

					continue
				}

				for _, agent := range known {
					cfg.SetMode(agent, id, mode)
				}
			}

			if err := cfg.Save(v.ConfigPath()); err != nil {
				return err
			}

			if len(known) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "saved")
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "saved for %s\n", strings.Join(known, ", "))
			}

			return nil
		},
	}

	cmd.Flags().StringArrayVar(&agents, "agent", nil, "only these agents; repeatable. Without it the kind's default changes for every agent")

	return cmd
}

// knownAgentIDs checks the names --agent was given against the agents
// beadle knows. A typo would otherwise write a setting for an agent that does
// not exist, which reads back as an applied change and changes nothing.
func knownAgentIDs(names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}

	all, err := allAgents()
	if err != nil {
		return nil, err
	}

	known := make([]string, 0, len(names))

	for _, name := range names {
		id := strings.TrimSpace(name)
		if id == "" {
			return nil, errors.New("beadle kinds: --agent was given an empty name")
		}

		found := false

		for _, agent := range all {
			if agent.ID == id {
				found = true

				break
			}
		}

		if !found {
			return nil, fmt.Errorf("beadle kinds: unknown agent %q; known agents: %s", id, strings.Join(agentIDs(all), ", "))
		}

		known = append(known, id)
	}

	return known, nil
}

// agentIDs lists the ids of the agents beadle knows, for an error message
// that can say what the name should have been.
func agentIDs(agents []*agent.Agent) []string {
	ids := make([]string, 0, len(agents))
	for _, a := range agents {
		ids = append(ids, a.ID)
	}

	slices.Sort(ids)

	return ids
}
