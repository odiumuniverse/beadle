package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

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
		mergetool, mergetoolAbort            bool
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
			"A full sync follows, so unrelated changes made meanwhile are merged, never overwritten.\n" +
			"  --mergetool    materialize the conflict as a real git merge state in the vault (audit mode);\n" +
			"                 settle it with git mergetool, then apply the file with --from <file>\n" +
			"                 (binding from beadle conflicts --json) or edit the conflict file and use --take file\n" +
			"  --mergetool-abort  remove that merge state; the refs/beadle/mergetool/* audit refs stay",
		RunE: func(cmd *cobra.Command, args []string) error {
			if mergetool || mergetoolAbort {
				opts := mergetoolOptions{
					start: mergetool, abort: mergetoolAbort, json: jsonOut,
					all: all, take: take, from: from, stdin: stdin,
					kind: kindName, agent: agentID, risky: allowRisky,
					expect: cmd.Flags().Changed("expect-base") || cmd.Flags().Changed("expect-vault") || cmd.Flags().Changed("expect-agent"),
				}

				return a.runMergetool(cmd, opts, args)
			}

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
				if err := printResolveJSON(out, report); err != nil {
					return err
				}

				return refusalError(report)
			}

			for _, refused := range report.Refusals {
				fmt.Fprintf(out, "refused %s: %s: %s\n", refused.ID, refused.Code, refused.Message)
			}

			fmt.Fprintf(out, "resolved %d conflict(s)\n\n", len(report.Resolved))
			printReport(out, report)

			return refusalError(report)
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
	cmd.Flags().BoolVar(&mergetool, "mergetool", false, "materialize the conflict as a git merge state in the vault (audit mode)")
	cmd.Flags().BoolVar(&mergetoolAbort, "mergetool-abort", false, "remove the mergetool merge state, keeping the audit refs")

	return cmd
}

type mergetoolOptions struct {
	start, abort, json, all, stdin bool
	risky, expect                  bool
	take, from, kind, agent        string
}

func (a *app) runMergetool(cmd *cobra.Command, opts mergetoolOptions, args []string) error {
	if err := validateMergetool(opts, args); err != nil {
		return err
	}

	e, err := a.engine()
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()

	if opts.abort {
		result, err := e.MergetoolAbort(cmd.Context(), args[0])
		if err != nil {
			return err
		}

		if opts.json {
			return printMergetoolJSON(out, result)
		}

		cmd.Printf("mergetool aborted: %s\n", result.Path)

		return nil
	}

	result, warnings, err := e.Mergetool(cmd.Context(), args[0])
	if err != nil {
		return err
	}

	for _, warning := range warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", warning)
	}

	if opts.json {
		return printMergetoolJSON(out, result)
	}

	cmd.Printf("mergetool active: %s\n", result.Path)
	cmd.Printf("  refs: %s\n", strings.Join(result.Refs, " "))
	cmd.Printf("  resolve it with git mergetool, then apply the file with\n")
	cmd.Printf("  beadle resolve %s --from <file> --expect-base <hash> --expect-vault <hash> --expect-agent <hash>\n", result.ID)
	cmd.Printf("  or edit the conflict file and run beadle resolve %s --take file\n", result.ID)
	cmd.Printf("  abort: beadle resolve %s --mergetool-abort\n", result.ID)

	return nil
}

func validateMergetool(opts mergetoolOptions, args []string) error {
	switch {
	case opts.start && opts.abort:
		return errors.New("choose either --mergetool or --mergetool-abort")
	case opts.all:
		return errors.New("--all cannot be combined with --mergetool or --mergetool-abort")
	case opts.take != "" || opts.from != "" || opts.stdin || opts.risky || opts.expect || opts.kind != "" || opts.agent != "":
		return errors.New("--mergetool takes no resolution flags (--take/--from/--stdin/--kind/--agent/--expect-*/--allow-risky)")
	case len(args) != 1 || args[0] == "":
		return errors.New("mergetool takes exactly one conflict id (see beadle conflicts --json)")
	}

	return nil
}

func printMergetoolJSON(w io.Writer, result engine.MergetoolResult) error {
	payload := struct {
		Mergetool engine.MergetoolResult `json:"mergetool"`
	}{Mergetool: result}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}

	_, err = fmt.Fprintln(w, string(data))

	return err
}

type resolveJSON struct {
	Resolved []string        `json:"resolved"`
	Refusals []state.Refusal `json:"refusals"`
}

func refusalError(report *engine.Report) error {
	if len(report.Refusals) == 0 {
		return nil
	}

	return fmt.Errorf("%d conflict resolution(s) were refused", len(report.Refusals))
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
