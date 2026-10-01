package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

func (a *app) newStatusCmd() *cobra.Command {
	var check, asJSON bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show agents, synchronization modes and open conflicts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			root, err := vault.ResolveRoot(a.vaultPath, fsutil.RootEnv(vault.EnvHome))
			if err != nil {
				return err
			}

			v := vault.New(root)

			// In --json mode stdout carries the document and nothing else: a
			// header line in front of it would break every consumer that pipes
			// it, which is the whole promise of the flag.
			if !asJSON {
				fmt.Fprintf(out, "vault: %s\n", v.Root())
			}

			// The same question, the same answer: a vault that travelled as its
			// tracked canon is a vault, and a directory that is neither is not.
			// `status` reports that state and exits 0, which is why this is not
			// resolveVault: a report about a machine with no vault yet is an
			// answer, not a failure.
			if !vaultLooksInitialized(v) {
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

			if asJSON {
				return writeJSON(out, buildStatusDocument(jsonSchemaName(cmd), root, cfg, agents, st))
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
				fmt.Fprintln(out, "pending changes: beadle status --outdated-only (or beadle diff)")

				return nil
			}

			return a.printOutdated(cmd, v, cfg, agents)
		},
	}

	// --outdated-only is the name the user reads. --check was the old one and
	// stays accepted so a script written against it keeps working, hidden so
	// the surface carries one name rather than two.
	cmd.Flags().BoolVar(&check, "outdated-only", false, "compute pending changes (dry run)")
	cmd.Flags().BoolVar(&check, "check", false, "compute pending changes (dry run)")
	_ = cmd.Flags().MarkHidden("check")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the document as JSON")

	return jsonForm(cmd, "beadle.status")
}

// printOutdated is the `--outdated-only` path: what a sync would do, without
// doing any of it. It is its own function so the command body carries one
// decision — compute, or not — instead of the whole compute.
func (a *app) printOutdated(cmd *cobra.Command, v *vault.Vault, cfg *config.Config, agents []*agent.Agent) error {
	e, err := a.engineWith(v, cfg, agents)
	if err != nil {
		return err
	}

	report, err := e.Sync(cmd.Context(), engine.SyncOptions{DryRun: true})
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()

	fmt.Fprintln(out)
	printReport(out, report)

	return nil
}

// statusDocument is what `beadle status --json` prints. The words are the
// user's: an agent is on or off, and it is on this machine or it is not. What
// beadle calls a kind or a surface stays out of it - a consumer can ask
// `beadle explain` for that.
type statusDocument struct {
	withSchema

	Root      string            `json:"root"`
	Agents    []agentRow        `json:"agents"`
	Conflicts []statusConflict  `json:"conflicts"`
	KindsOff  map[string]string `json:"kinds_off,omitempty"`
}

type statusConflict struct {
	Kind   string `json:"kind"`
	Agent  string `json:"agent"`
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

func buildStatusDocument(name, root string, cfg *config.Config, agents []*agent.Agent, st *state.State) statusDocument {
	doc := statusDocument{
		withSchema: newEnvelope(name),
		Root:       root,
		Agents:     []agentRow{},
		Conflicts:  []statusConflict{},
	}

	doc.Agents = agentRows(cfg, agents)

	for _, conflict := range st.OpenConflicts() {
		doc.Conflicts = append(doc.Conflicts, statusConflict{
			Kind:   string(conflict.Kind),
			Agent:  conflict.Agent,
			Key:    conflict.Key,
			Reason: conflict.Reason,
		})
	}

	return doc
}

func printAgents(out interface{ Write([]byte) (int, error) }, cfg *config.Config, agents []*agent.Agent) error {
	fmt.Fprintln(out, "agents:")

	rs := newReasons()

	for _, ag := range agents {
		detected, err := ag.Detect()
		if err != nil {
			return err
		}

		fmt.Fprintf(out, "  %-12s %-14s %s\n", ag.ID, agentWord(rs, cfg, ag, detected), modesOf(cfg, ag))
	}

	rs.print(out)

	return nil
}

// agentWord is one agent's state in the reader's words. What beadle calls
// "disabled" and "not installed" are two reasons a user can do something about,
// so they are reasons with a command, not states of their own.
func agentWord(rs *reasons, cfg *config.Config, ag *agent.Agent, detected bool) string {
	switch {
	case !cfg.Agents[ag.ID].Enabled:
		return rs.word(wordSkipped, fmt.Sprintf("turned off for this vault — run `beadle agents enable %s`", ag.ID))
	case !detected:
		return rs.word(wordSkipped, fmt.Sprintf("this agent's program is not installed on this machine (%s is not on PATH)", ag.ID))
	default:
		return wordDelivered
	}
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

// printBundleDelivery names the resources that read "off" while a
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

		ag := agent.ByID(agents, host.AgentID())
		if ag == nil {
			continue
		}

		for _, k := range host.Kinds() {
			if effectiveMode(cfg, ag, k, config.ModeSync) != config.ModeOff {
				continue
			}

			names := entry.Complement[k]
			if len(names) == 0 {
				fmt.Fprintf(out, "bundles: %s %s=off is written by the bundle, not as a file\n", host.AgentID(), k)

				continue
			}

			fmt.Fprintf(out, "bundles: %s %s=off is written by the bundle; the servers it cannot carry go to the host file: %s\n",
				host.AgentID(), k, strings.Join(sortedNames(names), ", "))
		}
	}
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
