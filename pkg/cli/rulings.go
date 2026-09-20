package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/rulings"
)

func (a *app) newRulingsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rulings",
		Short: "Inspect and curate remembered conflict rulings",
		Long: "rulings remembers conflict decisions by shape (kind, target, divergence, scope),\n" +
			"never by content. An observed signature is only suggested; a trusted one may be\n" +
			"applied automatically on sync when the match is exact and not blast-radius\n" +
			"(permissions and MCP command/url always ask).",
	}

	cmd.AddCommand(
		a.newRulingsListCmd(),
		a.newRulingsShowCmd(),
		a.newRulingsTrustCmd(),
		a.newRulingsForgetCmd(),
	)

	return cmd
}

func (a *app) newRulingsListCmd() *cobra.Command {
	var explain, jsonOut bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List remembered rulings",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ledger, err := a.loadLedger()
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			if jsonOut {
				return printRulingsJSON(out, ledger.All())
			}

			listRulings(out, ledger.All(), explain)

			return nil
		},
	}

	cmd.Flags().BoolVar(&explain, "explain", false, "show how each signature was derived")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")

	return cmd
}

func (a *app) newRulingsShowCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "show <sig>",
		Short: "Show one ruling",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ledger, err := a.loadLedger()
			if err != nil {
				return err
			}

			ruling, err := findRuling(ledger, args[0])
			if err != nil {
				return err
			}

			if jsonOut {
				return printRulingsJSON(cmd.OutOrStdout(), []rulings.Ruling{ruling})
			}

			printRuling(cmd.OutOrStdout(), ruling)

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")

	return cmd
}

func (a *app) newRulingsTrustCmd() *cobra.Command {
	var scope, ruling string

	cmd := &cobra.Command{
		Use:   "trust <sig> [--scope host:<agent>|global]",
		Short: "Promote a signature so it can be applied automatically",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := rulings.ValidateScope(scope); err != nil {
				return err
			}

			ledger, err := a.loadLedger()
			if err != nil {
				return err
			}

			sig, err := findRulingSignature(ledger, args[0])
			if err != nil {
				return err
			}

			sig.Scope = scope
			if sig.Scope == "" {
				sig.Scope = rulings.ScopeGlobal
			}

			chosen := ruling
			if chosen == "" {
				if existing, ok := ledger.Get(sig); ok {
					chosen = existing.Ruling
				}
			}

			if chosen != rulings.RulingTakeVault && chosen != rulings.RulingTakeAgent {
				return fmt.Errorf("ruling %q must be %s or %s", chosen, rulings.RulingTakeVault, rulings.RulingTakeAgent)
			}

			got := ledger.Trust(sig, sig.Scope, chosen, time.Now().UTC())
			if err := a.saveLedger(ledger); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "trusted %s\n", got.Signature.Hash())

			return nil
		},
	}

	cmd.Flags().StringVar(&scope, "scope", "", "scope override: host:<agent> or global")
	cmd.Flags().StringVar(&ruling, "ruling", "", "decision to remember: take-vault or take-agent")

	return cmd
}

func (a *app) newRulingsForgetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "forget <sig>",
		Short: "Delete a ruling so it is no longer suggested or applied",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ledger, err := a.loadLedger()
			if err != nil {
				return err
			}

			hash, err := resolveRulingHash(ledger, args[0])
			if err != nil {
				return err
			}

			if !ledger.Forget(hash) {
				return fmt.Errorf("no ruling %q", args[0])
			}

			if err := a.saveLedger(ledger); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "forgot %s\n", hash)

			return nil
		},
	}

	return cmd
}

func (a *app) loadLedger() (*rulings.Ledger, error) {
	v, err := a.resolveVault()
	if err != nil {
		return nil, err
	}

	return rulings.Load(v.RulingsPath())
}

func (a *app) saveLedger(ledger *rulings.Ledger) error {
	v, err := a.resolveVault()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(v.RulingsDir(), 0o700); err != nil {
		return err
	}

	return ledger.Save(v.RulingsPath())
}

func findRuling(ledger *rulings.Ledger, id string) (rulings.Ruling, error) {
	hash, err := resolveRulingHash(ledger, id)
	if err != nil {
		return rulings.Ruling{}, err
	}

	ruling, ok := ledger.Rulings[hash]
	if !ok {
		return rulings.Ruling{}, fmt.Errorf("no ruling %q", id)
	}

	return ruling, nil
}

func findRulingSignature(ledger *rulings.Ledger, id string) (rulings.Signature, error) {
	if ruling, ok := ledger.Rulings[id]; ok {
		return ruling.Signature, nil
	}

	var matches []rulings.Ruling

	for _, ruling := range ledger.All() {
		if strings.HasPrefix(ruling.Signature.Hash(), id) {
			matches = append(matches, ruling)
		}
	}

	switch len(matches) {
	case 0:
		return rulings.Signature{}, fmt.Errorf("no ruling %q (see beadle rulings list)", id)
	case 1:
		return matches[0].Signature, nil
	default:
		return rulings.Signature{}, fmt.Errorf("ruling %q is ambiguous (%d matches)", id, len(matches))
	}
}

func resolveRulingHash(ledger *rulings.Ledger, id string) (string, error) {
	if _, ok := ledger.Rulings[id]; ok {
		return id, nil
	}

	var matches []string

	for hash := range ledger.Rulings {
		if strings.HasPrefix(hash, id) {
			matches = append(matches, hash)
		}
	}

	slices.Sort(matches)

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no ruling %q (see beadle rulings list)", id)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ruling %q is ambiguous (%d matches)", id, len(matches))
	}
}

func listRulings(w io.Writer, all []rulings.Ruling, explain bool) {
	if len(all) == 0 {
		fmt.Fprintln(w, "no rulings")

		return
	}

	fmt.Fprintf(w, "%-12s  %-11s  %-20s  %-30s  %-14s  %-11s  %-9s  %s\n",
		"SIG", "KIND", "TARGET", "DIVERGENCE", "SCOPE", "RULING", "STATE", "HITS")

	for _, ruling := range all {
		sig := ruling.Signature

		fmt.Fprintf(w, "%-12s  %-11s  %-20s  %-30s  %-14s  %-11s  %-9s  %d\n",
			sig.Hash(), sig.Kind, sig.Target, sig.Divergence, sig.Scope, ruling.Ruling, ruling.State, ruling.Hits)

		if explain {
			fmt.Fprintf(w, "  kind=%s target=%s divergence=%s scope=%s\n", sig.Kind, sig.Target, sig.Divergence, sig.Scope)
		}
	}
}

func printRuling(w io.Writer, ruling rulings.Ruling) {
	sig := ruling.Signature

	fmt.Fprintf(w, "sig         %s\n", sig.Hash())
	fmt.Fprintf(w, "kind        %s\n", sig.Kind)
	fmt.Fprintf(w, "target      %s\n", sig.Target)
	fmt.Fprintf(w, "divergence  %s\n", sig.Divergence)
	fmt.Fprintf(w, "scope       %s\n", sig.Scope)
	fmt.Fprintf(w, "ruling      %s\n", ruling.Ruling)
	fmt.Fprintf(w, "state       %s\n", ruling.State)
	fmt.Fprintf(w, "source      %s\n", ruling.Source)
	fmt.Fprintf(w, "confirmed   %d\n", ruling.Confirmations)
	fmt.Fprintf(w, "hits        %d\n", ruling.Hits)
	fmt.Fprintf(w, "created     %s\n", ruling.CreatedAt.Format("2006-01-02 15:04:05"))

	if !ruling.LastAppliedAt.IsZero() {
		fmt.Fprintf(w, "last applied %s\n", ruling.LastAppliedAt.Format("2006-01-02 15:04:05"))
	}
}

func printRulingsJSON(w io.Writer, all []rulings.Ruling) error {
	if all == nil {
		all = []rulings.Ruling{}
	}

	data, err := json.MarshalIndent(map[string]any{"rulings": all}, "", "  ")
	if err != nil {
		return err
	}

	_, err = fmt.Fprintln(w, string(data))

	return err
}
