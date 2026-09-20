package cli

import (
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/skills"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

func (a *app) newInitCmd() *cobra.Command {
	var agentIDs []string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create the vault and enable the agents installed on this machine",
		Long: "init creates the vault (default ~/.beadle) and enables every detected agent.\n" +
			"It writes nothing into any agent: run `beadle sync --dry-run` to preview the\n" +
			"first synchronization, then `beadle sync`.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := vault.ResolveRoot(a.vaultPath, os.Getenv(vault.EnvHome))
			if err != nil {
				return err
			}

			v := vault.New(root)
			if err := v.Init(); err != nil {
				return err
			}

			cfg, err := config.Load(v.ConfigPath())
			if err != nil {
				return err
			}

			agents, err := allAgents()
			if err != nil {
				return err
			}

			if err := validateAgentIDs(agents, agentIDs); err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			fmt.Fprintf(out, "vault: %s\n\nagents:\n", v.Root())

			for _, ag := range agents {
				if err := enableOnInit(out, cfg, ag, agentIDs); err != nil {
					return err
				}
			}

			if err := cfg.Save(v.ConfigPath()); err != nil {
				return err
			}

			if _, err := skills.Seed(v.SkillsDir(), false); err != nil {
				return err
			}

			fmt.Fprintln(out)
			printModes(out, cfg, agents)
			fmt.Fprint(out, "\nnext:\n"+
				"  beadle sync --dry-run   # preview the first synchronization, nothing is written\n"+
				"  beadle sync             # synchronize; conflicts wait for `beadle resolve`\n"+
				"  beadle daemon install   # keep everything in sync in the background\n")

			return nil
		},
	}

	cmd.Flags().StringSliceVar(&agentIDs, "agents", nil, "enable exactly these agents instead of the detected ones")

	return cmd
}

func enableOnInit(out io.Writer, cfg *config.Config, ag *agent.Agent, requested []string) error {
	detected, err := ag.Detect()
	if err != nil {
		return err
	}

	enable := detected && !ag.OptIn
	if len(requested) > 0 {
		enable = slices.Contains(requested, ag.ID)
	}

	switch {
	case enable:
		cfg.Enable(ag.ID)
		fmt.Fprintf(out, "  [x] %-34s %s\n", ag.Name, ag.ID)
	case ag.OptIn:
		fmt.Fprintf(out, "  [ ] %-34s %s (opt-in: beadle agents enable %s)\n", ag.Name, ag.ID, ag.ID)
	case !detected:
		fmt.Fprintf(out, "  [ ] %-34s %s (not installed)\n", ag.Name, ag.ID)
	default:
		fmt.Fprintf(out, "  [ ] %-34s %s\n", ag.Name, ag.ID)
	}

	return nil
}

func validateAgentIDs(agents []*agent.Agent, ids []string) error {
	for _, id := range ids {
		if agent.ByID(agents, id) == nil {
			return fmt.Errorf("unknown agent %q (see beadle agents)", id)
		}
	}

	return nil
}

func printModes(out io.Writer, cfg *config.Config, agents []*agent.Agent) {
	fmt.Fprintln(out, "what is synchronized:")

	for _, ag := range agents {
		if !cfg.Agents[ag.ID].Enabled {
			continue
		}

		fmt.Fprintf(out, "  %s\n", ag.Name)

		for _, surface := range ag.Surfaces {
			mode := cfg.ModeFor(ag.ID, surface.Kind(), surface.Traits().DefaultMode)
			if !cfg.KindEnabled(surface.Kind()) {
				mode = config.ModeOff + " (kind disabled)"
			}

			line := fmt.Sprintf("    %-12s %-22s %s", surface.Kind(), mode, surface.Path())
			if note := surface.Traits().Note; note != "" {
				line += "\n" + fmt.Sprintf("    %-12s %-22s %s", "", "", "note: "+note)
			}

			fmt.Fprintln(out, line)
		}
	}
}
