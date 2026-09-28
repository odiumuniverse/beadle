package cli

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
	"github.com/odiumuniverse/beadle/pkg/vault"
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

			if !vaultDirExists(v.Root()) {
				fmt.Fprintln(out, "state: not initialized (run beadle init)")

				return nil
			}

			if err := a.ensureConfig(v); err != nil {
				return err
			}

			cfg, err := config.Load(v.ConfigPath())
			if err != nil {
				return err
			}

			a.reportConfigMigration(cfg)

			agents, err := allAgents()
			if err != nil {
				return err
			}

			st, err := state.Load(v.StatePath())
			if err != nil {
				return err
			}

			if err := printAgents(out, cfg, agents); err != nil {
				return err
			}

			printBundleDelivery(out, cfg, st, agents)

			if conflicts := st.OpenConflicts(); len(conflicts) > 0 {
				fmt.Fprintf(out, "conflicts: %d open (beadle conflicts)\n", len(conflicts))
			} else {
				fmt.Fprintln(out, "conflicts: none")
			}

			if !check {
				fmt.Fprintln(out, "pending changes: beadle status --check (or beadle diff)")

				return nil
			}

			e, err := a.engineWith(v, cfg, agents)
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

// effectiveMode resolves the mode the config asks for, with a globally
// disabled kind forced off. modesOf and the bundle-delivery report share it so
// the two never disagree about what "off" means for one agent.
func effectiveMode(cfg *config.Config, ag *agent.Agent, k kind.ID, fallback config.Mode) config.Mode {
	mode := cfg.ModeFor(ag.ID, k, fallback)
	if !cfg.KindEnabled(k) {
		mode = config.ModeOff
	}

	return mode
}

// printBundleDelivery names the kinds whose file surface reads "off" while a
// verified bundle still delivers them, so a bare "off" is not read as "nothing
// is written". A bundle-managed MCP surface keeps writing the canon's
// secret-bearing servers through the host file, because a bundle cannot carry
// them; those copies are listed so the delivery is visible in the report.
func printBundleDelivery(out interface{ Write([]byte) (int, error) }, cfg *config.Config, st *state.State, agents []*agent.Agent) {
	for _, host := range bundle.Hosts() {
		entry := st.Bundles[string(host)]
		if !entry.Enabled || !entry.Verified() {
			continue
		}

		ag := agentByID(agents, host.AgentID())
		if ag == nil {
			continue
		}

		for _, k := range host.Kinds() {
			if effectiveMode(cfg, ag, k, config.ModeSync) != config.ModeOff {
				continue
			}

			names := entry.Complement[k]
			if len(names) == 0 {
				fmt.Fprintf(out, "bundles: %s %s=off is delivered by the bundle, not the file surface\n", host.AgentID(), k)

				continue
			}

			fmt.Fprintf(out, "bundles: %s %s=off is delivered by the bundle; the servers it cannot carry go to the host file: %s\n",
				host.AgentID(), k, strings.Join(sortedNames(names), ", "))
		}
	}
}

func agentByID(agents []*agent.Agent, id string) *agent.Agent {
	for _, ag := range agents {
		if ag.ID == id {
			return ag
		}
	}

	return nil
}

// sortedNames orders the recorded complement names so the report does not
// depend on the order the vault happened to write them in.
func sortedNames(names []string) []string {
	out := slices.Clone(names)
	slices.Sort(out)

	return out
}

func modesOf(cfg *config.Config, ag *agent.Agent) string {
	seen := map[kind.ID]bool{}
	parts := make([]string, 0, len(ag.Surfaces))

	for _, surface := range ag.Surfaces {
		if seen[surface.Kind()] {
			continue
		}

		seen[surface.Kind()] = true

		mode := effectiveMode(cfg, ag, surface.Kind(), surface.Traits().DefaultMode)

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
