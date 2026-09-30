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
				return writeJSON(out, newAgentsDocument(jsonSchemaName(cmd), cfg, agents))
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

	return jsonForm(cmd, "beadle.agents")
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

// kindsDocument is what `beadle kinds --json` prints: the kind-centric view,
// which is the one thing `beadle agents --json` does not carry. An agent document
// answers "what mode does this agent resolve for that kind"; this one answers
// "is this kind synchronized at all, and if not, what do I run" — and the answer
// to the second is the footnote the human table prints under it, which a script
// cannot get anywhere else.
type kindsDocument struct {
	withSchema
	Kinds  []kindRow  `json:"kinds"`
	Agents []agentRow `json:"agents"`
}

// kindRow is one resource kind in the document: whether beadle keeps it in step,
// in the reader's word, and the reason and the command when it does not. The
// word is the same one the table prints, because a consumer and a person reading
// the same vault should not have to learn two vocabularies.
type kindRow struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
	Runnable string `json:"command,omitempty"`
}

// newKindsDocument projects the config into the kind view, plus the per-agent
// modes the same run resolves — one row type with `beadle agents --json`, so a
// consumer that wants both does not have to reconcile two shapes.
// newKindsDocument projects the config into the kind view, plus the per-agent
// modes the same run resolves — the same row type `beadle agents --json` uses, so
// a consumer that wants both does not have to reconcile two shapes. The name
// comes from the command that asked, which is where it is declared: the envelope
// on stdout and the annotation the root reads cannot drift apart.
func newKindsDocument(name string, cfg *config.Config, agents []*agent.Agent) kindsDocument {
	doc := kindsDocument{
		withSchema: newEnvelope(name),
		Kinds:      []kindRow{},
		Agents:     agentRows(cfg, agents),
	}

	for _, spec := range kind.All() {
		row := kindRow{ID: string(spec.ID), Status: wordDelivered}

		if !cfg.KindEnabled(spec.ID) {
			row.Status = wordSkipped
			row.Reason = "not synchronized"
			row.Runnable = "beadle kinds enable " + string(spec.ID)
		}

		doc.Kinds = append(doc.Kinds, row)
	}

	return doc
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

			if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
				agents, agentsErr := allAgents()
				if agentsErr != nil {
					return agentsErr
				}

				return writeJSON(cmd.OutOrStdout(), newKindsDocument(jsonSchemaName(cmd), cfg, agents))
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

	return jsonForm(cmd, "beadle.kinds")
}

// kindWord is one resource's state in the reader's words. A resource that is
// switched on is what beadle keeps in step with every agent; one that is off is
// skipped, with the command that changes it. It is the human half of the same
// fact newKindsDocument states as a status and a runnable command.
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
