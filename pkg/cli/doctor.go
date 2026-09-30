package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/vergerx"
	"github.com/odiumuniverse/verger/pkg/verger"
)

func (a *app) newDoctorCmd() *cobra.Command {
	var (
		asJSON bool
		fix    bool
		assume bool
	)

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose vault, sync state and agent configuration problems",
		Long: "Doctor reports what needs your attention and, for each finding, the command " +
			"that fixes it. With --fix it applies the fixes it owns outright — creating a " +
			"directory, re-registering a service — after one confirmation. Anything that " +
			"would touch something you wrote, or that needs your consent, is printed " +
			"rather than applied.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := a.engine()
			if err != nil {
				return err
			}

			issues, err := e.Doctor(cmd.Context())
			if err != nil {
				return err
			}

			if asJSON {
				return printDoctorJSON(cmd, issues)
			}

			out := cmd.OutOrStdout()

			// One line, not a section: the plugin manager has its own
			// document, and beadle points at it instead of restating it.
			if line, ok := a.pluginsSummary(cmd); ok {
				fmt.Fprintln(out, line)
			}

			if fix {
				return a.applyDoctorFixes(cmd, issues, assume)
			}

			printDoctorFindings(out, issues)

			return doctorError(issues)
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "print the findings as JSON")
	cmd.Flags().BoolVar(&fix, "fix", false, "apply the fixes doctor owns, after one confirmation")
	cmd.Flags().BoolVarP(&assume, "yes", "y", false, "apply the fixes without asking")

	return cmd
}

// printDoctorFindings writes the human form: one line per finding, the words
// from W7-UX §1, and a fix line only when there is a command to run. A finding
// with no fix says so rather than leaving the reader wondering.
func printDoctorFindings(out interface{ Write([]byte) (int, error) }, issues []engine.Finding) {
	if len(issues) == 0 {
		fmt.Fprintln(out, "no issues found")

		return
	}

	for _, issue := range issues {
		fmt.Fprintf(out, "%-8s %-28s %s\n", issue.Severity, issue.Scope(), issue.Message)

		if len(issue.Fix) == 0 {
			continue
		}

		fmt.Fprintf(out, "         fix: %s\n", renderFix(issue.Fix))
	}
}

// renderFix renders the argv steps as a copyable string. Steps are joined with
// "; " so a user can see the order and run the first one on its own.
func renderFix(fix [][]string) string {
	steps := make([]string, 0, len(fix))
	for _, step := range fix {
		steps = append(steps, strings.Join(step, " "))
	}

	return strings.Join(steps, "; ")
}

// doctorDocument is the `beadle doctor --json` envelope. W7-UX §2.1 requires every
// document to carry its schema as the FIRST field, and the doctor document was
// a bare array: a consumer could not tell it from any other array, and could
// not version it. It now uses the same writeJSON envelope as status, sync and
// plugins, with the findings under a named key.
type doctorDocument struct {
	withSchema

	Findings []engine.Finding `json:"findings"`
}

// printDoctorJSON writes the shared schema: one object, schema first, then
// the findings, each carrying the fields W7-UX §3.1 fixes, so a script reads
// the same shape from either tool.
func printDoctorJSON(cmd *cobra.Command, issues []engine.Finding) error {
	// Populate Subject from the derived scope before encoding: a script
	// matches on it, so it must never be the empty string a check left behind.
	resolved := make([]engine.Finding, 0, len(issues))
	for _, issue := range issues {
		issue.Subject = issue.Scope()
		resolved = append(resolved, issue)
	}

	issues = resolved

	doc := doctorDocument{withSchema: newEnvelope("beadle.doctor"), Findings: issues}

	if err := writeJSON(cmd.OutOrStdout(), doc); err != nil {
		return fmt.Errorf("doctor: %w", err)
	}

	return doctorError(issues)
}

// doctorError is the exit: at least one error-level finding, or nil.
func doctorError(issues []engine.Finding) error {
	for _, issue := range issues {
		if issue.Severity == engine.SeverityError {
			return fmt.Errorf("doctor found %d error(s)", countErrors(issues))
		}
	}

	return nil
}

func countErrors(issues []engine.Finding) int {
	count := 0

	for _, issue := range issues {
		if issue.Severity == engine.SeverityError {
			count++
		}
	}

	return count
}

// applyDoctorFixes applies the safe findings and prints the rest. The order is
// by subject, so a run is reproducible: two findings whose fixes touch the same
// thing run in the same sequence every time.
func (a *app) applyDoctorFixes(cmd *cobra.Command, issues []engine.Finding, assume bool) error {
	out := cmd.OutOrStdout()

	safe := make([]engine.Finding, 0, len(issues))
	manual := make([]engine.Finding, 0, len(issues))

	for _, issue := range issues {
		if issue.CanAutoFix() {
			safe = append(safe, issue)
		} else {
			manual = append(manual, issue)
		}
	}

	slices.SortFunc(safe, func(a, b engine.Finding) int { return strings.Compare(a.Scope(), b.Scope()) })

	if len(safe) == 0 {
		printDoctorFindings(out, issues)

		if len(issues) > 0 {
			fmt.Fprintln(out, "\nnothing doctor can fix on its own; the lines above say what to run")
		}

		return doctorError(issues)
	}

	// The whole plan, once. Asking per finding is how an autofix trains people
	// to press enter without reading.
	fmt.Fprintf(out, "doctor will apply %d fix(es):\n", len(safe))

	for _, issue := range safe {
		fmt.Fprintf(out, "  %-28s %s\n", issue.Scope(), renderFix(issue.Fix))
	}

	if len(manual) > 0 {
		fmt.Fprintf(out, "\nand print %d finding(s) it will not touch:\n", len(manual))
		printDoctorFindings(out, manual)
	}

	if !assume {
		fmt.Fprintf(out, "\napply? [y/N] ")

		var answer string
		if _, err := fmt.Fscan(cmd.InOrStdin(), &answer); err != nil {
			return fmt.Errorf("doctor: read the confirmation: %w", err)
		}

		if !strings.EqualFold(strings.TrimSpace(answer), "y") {
			fmt.Fprintln(out, "no")

			return doctorError(issues)
		}
	}

	applied, failed := 0, 0

	for _, issue := range safe {
		if err := a.runFixStep(cmd, issue.Fix[0]); err != nil {
			// Stop at the first failure: the fixes are ordered, and the ones
			// after this may assume it happened.
			fmt.Fprintf(out, "\nfailed at %s: %v\n", issue.Scope(), err)

			failed++

			break
		}

		fmt.Fprintf(out, "fixed %s\n", issue.Scope())

		applied++
	}

	fmt.Fprintf(out, "\napplied %d of %d", applied, len(safe))

	if failed > 0 {
		fmt.Fprintf(out, ", stopped at the first failure")
	}

	fmt.Fprintln(out)

	return doctorError(issues)
}

// fixTimeout bounds one fix step. A watcher install that hangs must not hang
// the doctor that asked for it: the user gets an error they can act on, not a
// terminal that stopped responding.
const fixTimeout = 2 * time.Minute

// runFixStep runs one argv step. It is a field on app so a test drives it with
// a recorder: a test that shells out to the real binary runs the real
// installer, and the first version of this test did exactly that and hung the
// whole package for ten minutes.
func (a *app) runFixStep(cmd *cobra.Command, step []string) error {
	if len(step) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), fixTimeout)
	defer cancel()

	if a.execFix != nil {
		return a.execFix(ctx, cmd, step)
	}

	return a.runFixReal(ctx, cmd, step)
}

// runFixReal is the real executor: it runs the same binary the user was shown, so
// the step performed is the command printed rather than a second
// implementation of it.
func (a *app) runFixReal(ctx context.Context, cmd *cobra.Command, step []string) error {
	// The self path: `beadle daemon install` inside `beadle doctor --fix` must
	// be this binary, not whatever is first on PATH.
	binary, err := os.Executable()
	if err != nil || binary == "" {
		binary = step[0]
	}

	// The binary is this process (`os.Executable`), or the first token of a
	// step beadle itself wrote into its own fix table and showed the user;
	// the arguments are the rest of that same printed command. Nothing here
	// comes from input.
	command := exec.CommandContext(ctx, binary, step[1:]...) //nolint:gosec // G204: see above — the argv is beadle's own fix table
	command.Stdout = cmd.OutOrStdout()
	command.Stderr = cmd.ErrOrStderr()

	return command.Run()
}

// pluginsSummary is the single plugins line doctor prints: how many packages
// are installed, and a pointer to the command that explains the matrix. A
// machine whose plugin home cannot be opened gets no line rather than a broken
// one.
//
// The contract is docs/TASK-W3B1-EMBED.md §4: "one plugins summary line +
// pointer to `verger status`". An earlier reconstruction of this function
// pointed at `beadle plugins list`, which is not what that section asks for;
// the pointer is `verger status` because the plugin library owns the matrix.
func (a *app) pluginsSummary(cmd *cobra.Command) (string, bool) {
	var doc *verger.StatusDocument

	// withPlugins is the one way the CLI opens a plugin client; going around it
	// would be a second path to the same subsystem.
	if err := a.withPlugins(cmd, func(c *vergerx.Client, _ *bytes.Buffer) error {
		status, err := c.Status(cmd.Context())
		if err == nil {
			doc = &status
		}

		return err
	}); err != nil || doc == nil {
		return "", false
	}

	// The contract (docs/TASK-W3B1-EMBED.md §4) is "one plugins summary line
	// + pointer to `verger status`", not to beadle's own list.
	if len(doc.Cells) == 0 {
		return "plugins: none installed — `verger status` explains the matrix", true
	}

	// Count the packages, not the cells: one package on four hosts is one
	// package, and "4 installed" would be a different claim.
	packages := make(map[string]struct{}, len(doc.Cells))
	for _, cell := range doc.Cells {
		packages[cell.Package] = struct{}{}
	}

	return fmt.Sprintf("plugins: %d installed across %d cell(s) — `verger status` shows the matrix", len(packages), len(doc.Cells)), true
}
