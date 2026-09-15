package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/agents-sync/pkg/engine"
)

func (a *app) newResolveCmd() *cobra.Command {
	var (
		take, from, kindName, agentID string
		all                           bool
	)

	cmd := &cobra.Command{
		Use:   "resolve [id...]",
		Short: "Settle conflicts and propagate the decision to every agent",
		Long: "resolve settles open conflicts (see `agent-sync conflicts`):\n" +
			"  --take vault   keep the vault value; the agent receives it\n" +
			"  --take agent   take the agent value into the vault and every other agent\n" +
			"  --take file    take the edited conflict file (markers removed) from the vault conflicts directory\n" +
			"  --from <file>  take the content of any file\n" +
			"A full sync follows, so unrelated changes made meanwhile are merged, never overwritten.",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := a.engine()
			if err != nil {
				return err
			}

			ids, err := selectConflicts(e, args, all, kindName, agentID)
			if err != nil {
				return err
			}

			if len(ids) == 0 {
				return errors.New("nothing to resolve: pass conflict ids or --all (see agent-sync conflicts)")
			}

			res, err := resolution(take, from)
			if err != nil {
				return err
			}

			report, err := e.Resolve(cmd.Context(), ids, res)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			fmt.Fprintf(out, "resolved %d conflict(s)\n\n", len(ids))
			printReport(out, report)

			return nil
		},
	}

	cmd.Flags().StringVar(&take, "take", "", "winning side: vault, agent or file")
	cmd.Flags().StringVar(&from, "from", "", "file holding the resolved content")
	cmd.Flags().BoolVar(&all, "all", false, "resolve every open conflict (filter with --kind and --agent)")
	cmd.Flags().StringVar(&kindName, "kind", "", "with --all: only conflicts of this kind")
	cmd.Flags().StringVar(&agentID, "agent", "", "with --all: only conflicts of this agent")

	return cmd
}

func resolution(take, from string) (engine.Resolution, error) {
	if from != "" {
		data, err := os.ReadFile(from) //nolint:gosec // G304: the user names the file holding the resolution
		if err != nil {
			return engine.Resolution{}, fmt.Errorf("read %s: %w", from, err)
		}

		return engine.Resolution{Take: engine.TakeContent, Content: data}, nil
	}

	if take == "" {
		return engine.Resolution{}, errors.New("choose --take vault, --take agent or --take file (or --from <file>)")
	}

	parsed, err := engine.ParseTake(take)
	if err != nil {
		return engine.Resolution{}, err
	}

	if parsed == engine.TakeContent {
		return engine.Resolution{}, errors.New("use --from <file> to provide content")
	}

	return engine.Resolution{Take: parsed}, nil
}

func selectConflicts(e *engine.Engine, args []string, all bool, kindName, agentID string) ([]string, error) {
	if !all {
		return args, nil
	}

	conflicts, err := e.Conflicts()
	if err != nil {
		return nil, err
	}

	var ids []string

	for _, c := range conflicts {
		if kindName != "" && string(c.Kind) != kindName {
			continue
		}

		if agentID != "" && c.Agent != agentID {
			continue
		}

		ids = append(ids, c.ID())
	}

	return ids, nil
}
