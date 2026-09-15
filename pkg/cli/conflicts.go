package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/agents-sync/pkg/engine"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
	"github.com/odiumuniverse/agents-sync/pkg/state"
)

func (a *app) newConflictsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "conflicts [id]",
		Short: "List open conflicts, or show one in detail",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := a.engine()
			if err != nil {
				return err
			}

			conflicts, err := e.Conflicts()
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			if len(args) == 0 {
				listConflicts(out, conflicts)

				return nil
			}

			c, err := findConflict(conflicts, args[0])
			if err != nil {
				return err
			}

			return showConflict(out, e, c)
		},
	}
}

func listConflicts(w io.Writer, conflicts []state.Conflict) {
	if len(conflicts) == 0 {
		fmt.Fprintln(w, "no open conflicts")

		return
	}

	fmt.Fprintf(w, "%-8s  %-11s  %-12s  %-11s  %s\n", "ID", "KIND", "AGENT", "REASON", "ITEM")

	for _, c := range conflicts {
		fmt.Fprintf(w, "%-8s  %-11s  %-12s  %-11s  %s\n", c.ID(), c.Kind, c.Agent, c.Reason, c.TargetKey())
	}

	fmt.Fprintln(w, "\ndetails: agent-sync conflicts <id>    settle: agent-sync resolve <id> --take vault|agent|file")
}

func findConflict(conflicts []state.Conflict, id string) (state.Conflict, error) {
	var found []state.Conflict

	for _, c := range conflicts {
		if strings.HasPrefix(c.ID(), id) {
			found = append(found, c)
		}
	}

	switch len(found) {
	case 0:
		return state.Conflict{}, fmt.Errorf("no open conflict %q", id)
	case 1:
		return found[0], nil
	default:
		return state.Conflict{}, fmt.Errorf("conflict id %q is ambiguous", id)
	}
}

func showConflict(w io.Writer, e *engine.Engine, c state.Conflict) error {
	base, vaultValue, local, err := e.ConflictValues(c)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "conflict %s: %s %s\n", c.ID(), c.Kind, c.TargetKey())
	fmt.Fprintf(w, "  agent:  %s\n  reason: %s\n  since:  %s\n", c.Agent, c.Reason, c.Since.Local().Format(time.DateTime))

	file, err := e.ConflictFile(c)
	if err == nil {
		fmt.Fprintf(w, "  file:   %s\n", file)
	}

	fmt.Fprintln(w)
	printSide(w, "base (last synchronized)", c.Kind, base)
	printSide(w, "vault", c.Kind, vaultValue)
	printSide(w, "agent "+c.Agent, c.Kind, local)

	fmt.Fprintf(w, "settle:\n  agent-sync resolve %s --take vault   # keep the vault value; %s receives it\n", c.ID(), c.Agent)
	fmt.Fprintf(w, "  agent-sync resolve %s --take agent   # take the value of %s everywhere\n", c.ID(), c.Agent)

	if err == nil && filepath.Ext(file) != ".json" {
		fmt.Fprintf(w, "  or edit %s, remove the conflict markers, then: agent-sync resolve %s --take file\n", file, c.ID())
	}

	return nil
}

func printSide(w io.Writer, title string, k kind.ID, data []byte) {
	fmt.Fprintf(w, "── %s ──\n", title)

	switch {
	case data == nil:
		fmt.Fprintln(w, "(absent)")
	case !kind.IsText(data):
		fmt.Fprintf(w, "(%d bytes of binary data)\n", len(data))
	default:
		text := readable(k, data)
		fmt.Fprint(w, text)

		if !strings.HasSuffix(text, "\n") {
			fmt.Fprintln(w)
		}
	}
}
