package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
)

func (a *app) newResolveCmd() *cobra.Command {
	var (
		take, from, kindName, agentID        string
		expectBase, expectVault, expectAgent string
		all, stdin, allowRisky, jsonOut      bool
	)

	cmd := &cobra.Command{
		Use:   "resolve [id...]",
		Short: "Settle conflicts and propagate the decision to every agent",
		Long: "resolve settles open conflicts (see `beadle conflicts`):\n" +
			"  --take vault   keep the vault value; the agent receives it\n" +
			"  --take agent   take the agent value into the vault and every other agent\n" +
			"  --take file    take the edited conflict file (markers removed) from the vault conflicts directory\n" +
			"  --from <file>  take the content of any file (requires the three --expect-* hashes)\n" +
			"  --stdin        take the content from stdin (requires the three --expect-* hashes)\n" +
			"--expect-base/--expect-vault/--expect-agent bind the decision to the conflict as it was\n" +
			"read; any mismatch is refused (stale-conflict), never partially applied.\n" +
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
				return errors.New("nothing to resolve: pass conflict ids or --all (see beadle conflicts)")
			}

			res, err := resolution(cmd.InOrStdin(), take, from, stdin, expectation{
				base: expectBase, vault: expectVault, agent: expectAgent,
				baseSet:    cmd.Flags().Changed("expect-base"),
				vaultSet:   cmd.Flags().Changed("expect-vault"),
				agentSet:   cmd.Flags().Changed("expect-agent"),
				allowRisky: allowRisky,
			})
			if err != nil {
				return err
			}

			report, err := e.Resolve(cmd.Context(), ids, res)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			if jsonOut {
				return printResolveJSON(out, report)
			}

			for _, refused := range report.Refusals {
				fmt.Fprintf(out, "refused %s: %s: %s\n", refused.ID, refused.Code, refused.Message)
			}

			fmt.Fprintf(out, "resolved %d conflict(s)\n\n", len(report.Resolved))
			printReport(out, report)

			return nil
		},
	}

	cmd.Flags().StringVar(&take, "take", "", "winning side: vault, agent or file")
	cmd.Flags().StringVar(&from, "from", "", "file holding the resolved content")
	cmd.Flags().BoolVar(&stdin, "stdin", false, "read the resolved content from stdin")
	cmd.Flags().StringVar(&expectBase, "expect-base", "", "base hash the decision was made against (required with --from and --stdin)")
	cmd.Flags().StringVar(&expectVault, "expect-vault", "", "vault hash the decision was made against (required with --from and --stdin)")
	cmd.Flags().StringVar(&expectAgent, "expect-agent", "", "agent hash the decision was made against (required with --from and --stdin)")
	cmd.Flags().BoolVar(&allowRisky, "allow-risky", false, "allow a change to an MCP command/url or to permission rules")
	cmd.Flags().BoolVar(&all, "all", false, "resolve every open conflict (filter with --kind and --agent)")
	cmd.Flags().StringVar(&kindName, "kind", "", "with --all: only conflicts of this kind")
	cmd.Flags().StringVar(&agentID, "agent", "", "with --all: only conflicts of this agent")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")

	return cmd
}

type resolveJSON struct {
	Resolved []string        `json:"resolved"`
	Refusals []state.Refusal `json:"refusals"`
}

func printResolveJSON(w io.Writer, report *engine.Report) error {
	payload := resolveJSON{Resolved: report.Resolved, Refusals: report.Refusals}

	if payload.Resolved == nil {
		payload.Resolved = []string{}
	}

	if payload.Refusals == nil {
		payload.Refusals = []state.Refusal{}
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}

	_, err = fmt.Fprintln(w, string(data))

	return err
}

type expectation struct {
	base, vault, agent string
	baseSet, vaultSet  bool
	agentSet           bool
	allowRisky         bool
}

func (exp expectation) missing() string {
	switch {
	case !exp.baseSet:
		return "--expect-base"
	case !exp.vaultSet:
		return "--expect-vault"
	case !exp.agentSet:
		return "--expect-agent"
	default:
		return ""
	}
}

func (exp expectation) resolution(content []byte) engine.Resolution {
	return engine.Resolution{
		Take:        engine.TakeContent,
		Content:     content,
		ExpectBase:  exp.base,
		ExpectVault: exp.vault,
		ExpectAgent: exp.agent,
		AllowRisky:  exp.allowRisky,
	}
}

func resolution(stdin io.Reader, take, from string, useStdin bool, exp expectation) (engine.Resolution, error) {
	switch {
	case from != "" && useStdin:
		return engine.Resolution{}, errors.New("choose either --from <file> or --stdin")
	case from != "":
		if missing := exp.missing(); missing != "" {
			return engine.Resolution{}, fmt.Errorf("%s <hash> is required with --from (see beadle conflicts --json)", missing)
		}

		data, err := os.ReadFile(from) //nolint:gosec // G304: the user names the file holding the resolution
		if err != nil {
			return engine.Resolution{}, fmt.Errorf("read %s: %w", from, err)
		}

		return exp.resolution(data), nil
	case useStdin:
		if missing := exp.missing(); missing != "" {
			return engine.Resolution{}, fmt.Errorf("%s <hash> is required with --stdin (see beadle conflicts --json)", missing)
		}

		data, err := io.ReadAll(stdin)
		if err != nil {
			return engine.Resolution{}, fmt.Errorf("read stdin: %w", err)
		}

		return exp.resolution(data), nil
	}

	if take == "" {
		return engine.Resolution{}, errors.New("choose --take vault, --take agent, --take file (or --from/--stdin with --expect-base/--expect-vault/--expect-agent)")
	}

	parsed, err := engine.ParseTake(take)
	if err != nil {
		return engine.Resolution{}, err
	}

	if parsed == engine.TakeContent {
		return engine.Resolution{}, errors.New("use --from <file> or --stdin to provide content")
	}

	return engine.Resolution{Take: parsed, AllowRisky: exp.allowRisky}, nil
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
		if c.Kind == kind.Permissions && kindName == "" {
			continue
		}

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
