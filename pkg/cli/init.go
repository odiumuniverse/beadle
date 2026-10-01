package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	daemonpkg "github.com/odiumuniverse/beadle/pkg/daemon"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/skills"
	"github.com/odiumuniverse/beadle/pkg/vault"
	"github.com/odiumuniverse/beadle/pkg/vergerx"
)

// initOptions are the two flags that change what init does rather than what it
// creates: -y skips the one question, --dry-run writes nothing.
type initOptions struct {
	assume bool
	dryRun bool
}

func (a *app) newInitCmd() *cobra.Command {
	var (
		agentIDs         []string
		daemon, noDaemon bool
		assume, dryRun   bool
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create the vault and enable the agents installed on this machine",
		Long: "init creates the vault (default ~/.beadle) and enables every detected agent.\n" +
			"Detected hosts with their CLI installed get their native bundle rendered and\n" +
			"registered; without the CLI, init prints the registration command and leaves\n" +
			"file sync on. In a git checkout the project files present on disk are enabled\n" +
			"with secrets kept out, and the background watcher is installed unless it is\n" +
			"already there. Run `beadle sync --dry-run` to preview the first\n" +
			"synchronization, then `beadle sync`.\n" +
			"With --daemon the watcher is (re)installed even if a service file exists;\n" +
			"--no-daemon skips the watcher and prints the hint.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if daemon && noDaemon {
				return errors.New("choose either --daemon or --no-daemon")
			}

			// The flags are read here, not where they were registered: registration
			// runs before parsing, so copying the values there captures the
			// zero value and -y/--dry-run silently do nothing.
			return a.runInit(cmd, agentIDs, daemon, !noDaemon, initOptions{assume: assume, dryRun: dryRun})
		},
	}

	cmd.Flags().StringSliceVar(&agentIDs, "agents", nil, "enable exactly these agents instead of the detected ones")
	cmd.Flags().BoolVar(&daemon, "daemon", false, "install the watcher even if a service file already exists")
	cmd.Flags().BoolVar(&noDaemon, "no-daemon", false, "do not install the background watcher")
	cmd.Flags().BoolVarP(&assume, "yes", "y", false, "enable the detected agents without asking")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan and write nothing")

	return cmd
}

func (a *app) runInit(cmd *cobra.Command, agentIDs []string, forceDaemon, installDaemon bool, opts initOptions) error {
	root, err := vault.ResolveRoot(a.vaultPath, fsutil.RootEnv(vault.EnvHome))
	if err != nil {
		return err
	}

	// The plan comes first: the user reads what was found before anything is
	// written, and a --dry-run run stops here having written nothing at all.
	agents, err := allAgents()
	if err != nil {
		return err
	}

	home, herr := os.UserHomeDir()
	if herr != nil {
		home = ""
	}

	thePlan := buildPlan(root, home, agents, nil)
	annotatePlan(&thePlan, enabledAgents(root), agents)
	thePlan.Already = alreadyInitialised(root, thePlan)
	// The wording and the question both key off this, not off len(New): a
	// newly found agent is a change to apply to a vault that is already
	// there, and announcing a creation for it was the reported bug.
	thePlan.Exists = existsDir(root)

	out := cmd.OutOrStdout()

	thePlan.render(out)

	if thePlan.Already {
		return a.reconcileOnReRun(cmd, out, forceDaemon, installDaemon)
	}

	// Only a vault that does not exist is ever created, so only that run asks.
	// A re-run that found a new agent has work to do, and asking "create the
	// vault?" about a vault already on disk was the second half of the same
	// defect.
	if !opts.assume && !thePlan.Exists {
		askCreate(out, thePlan.countFound())

		// An empty or closed stdin is the default, not a failure: a script
		// running `beadle init` with no terminal is the most likely reader of
		// this prompt, and it should get the documented [Y/n] default rather
		// than an error it cannot answer.
		var answer string

		if _, err := fmt.Fscan(cmd.InOrStdin(), &answer); err != nil {
			answer = "" // EOF: take the default
		}

		if !answerIsYes(answer) {
			fmt.Fprintln(out, "\n  nothing was written")
			renderHowToStop(out)

			return nil
		}
	}

	if opts.dryRun {
		dryRunNotice(out)
		renderHowToStop(out)

		return nil
	}

	return a.applyInit(cmd, out, root, agentIDs, agents, forceDaemon, installDaemon)
}

// reconcileOnReRun is a second `init`: it is a summary, but it is not inert.
// The desired state of the watcher is still reconciled, because a unit that is
// missing or points at another vault is exactly what a re-run should fix.
// The agent work is skipped, because a re-run that enabled agents would be
// doing the thing §5.2 says it must not.
func (a *app) reconcileOnReRun(cmd *cobra.Command, out io.Writer, forceDaemon, installDaemon bool) error {
	fmt.Fprintln(out, "\n  reconciling the watcher")

	daemonHint, err := a.installDaemonOnInitStep(cmd, out, forceDaemon, installDaemon)
	if err != nil {
		return err
	}

	if daemonHint {
		fmt.Fprintln(out, "  beadle daemon install   (re)install the watcher")
	}

	// The same next steps a first run prints. A summary that drops them loses
	// the guide pointer, and `TestGuidePointerAfterInit` — which pins that an
	// AI agent is told where the guide is — fails on any second run.
	printInitNext(out, daemonHint)

	renderHowToStop(out)

	return nil
}

// enabledAgents reads which agents the existing vault has switched on, so a
// second run can tell one that is merely present from one that has appeared
// since. A vault that does not exist yet yields an empty map, which is the
// honest answer for a first run rather than a special case.
func enabledAgents(root string) map[string]bool {
	enabled := map[string]bool{}

	if !existsDir(root) {
		return enabled
	}

	cfg, err := config.Load(vault.New(root).ConfigPath())
	if err != nil {
		return enabled
	}

	for id, entry := range cfg.Agents {
		if entry.Enabled {
			enabled[id] = true
		}
	}

	return enabled
}

// annotatePlan fills in what the screen shows about agents: the ones that are
// new since the last run, and the ones that are enabled but no longer on this
// machine. Names, not ids — the screen is for a person.
func annotatePlan(thePlan *plan, enabled map[string]bool, agents []*agent.Agent) {
	for _, h := range thePlan.Hosts {
		if !h.Found {
			continue
		}

		if !enabled[h.ID] {
			thePlan.New = append(thePlan.New, h.Name)
		}
	}

	// An agent the vault has enabled that is no longer found is reported by
	// name, with the command that stops it. The screen only *asks* about new
	// ones; this one is information, because sync is already pointing at a
	// host that is not there.
	//
	// The loop runs on a first run too, and there it finds nothing: `enabled`
	// is an empty map then, so every lookup is false. It used to be wrapped in
	// `if enabled != nil`, which was always true — a map literal is never nil —
	// and said nothing about the run it was trying to exclude.
	for _, h := range thePlan.Hosts {
		if !h.Found && enabled[h.ID] {
			thePlan.Gone = append(thePlan.Gone, h.Name)
		}
	}
}

// applyInit is the half of init that writes: the vault, the config, the
// agents, the bundles and the watcher. Everything before it is the screen and
// the question, and everything after is nothing. Split here because a function
// carrying both is a function nobody can read either half of.
func (a *app) applyInit(
	cmd *cobra.Command,
	out io.Writer,
	root string,
	agentIDs []string,
	agents []*agent.Agent,
	forceDaemon, installDaemon bool,
) error {
	v := vault.New(root)
	if err := v.Init(); err != nil {
		return err
	}

	// A standalone `verger watch` run before init created ~/.verger and is
	// watching plugins there. The move happens here, before anything in this
	// window has opened - let alone created - the vault's plugin home: verger
	// renames a home whose target is absent and merges into one that is not,
	// and the merge would keep a freshly laid out default spec over the user's
	// standalone one (NIGHT-pR-2, NIGHT-pC-6).
	if err := a.absorbStandaloneVergerHome(cmd.Context(), v, out); err != nil {
		return err
	}

	cfg, err := config.Load(v.ConfigPath())
	if err != nil {
		return err
	}

	a.reportConfigMigration(cfg)

	requested, err := resolveAgentIDs(agents, agentIDs)
	if err != nil {
		return err
	}

	if err := a.enableAgentsOnInit(out, v, cfg, agents, requested); err != nil {
		return err
	}

	e, err := a.engineWith(v, cfg, agents)
	if err != nil {
		return err
	}

	a.applyInitMigrationDefaults(e, out)

	if err := cfg.Save(v.ConfigPath()); err != nil {
		return err
	}

	if _, err := skills.Seed(v.SkillsDir(), false); err != nil {
		return err
	}

	// The bundle attempt runs before the modes table: a verified bundle turns
	// skills/mcp off, and the table must show what was just saved.
	if err := a.autoEnableBundlesOnInit(cmd, e, out); err != nil {
		return err
	}

	if err := a.enableProjectDefaultsOnInit(cmd, e, out); err != nil {
		return err
	}

	fmt.Fprintln(out)
	printModes(out, cfg, agents)

	daemonHint, err := a.installDaemonOnInitStep(cmd, out, forceDaemon, installDaemon)
	if err != nil {
		return err
	}

	printInitNext(out, daemonHint)

	return nil
}

// enableAgentsOnInit prints the agent table and enables the selected ones.
func (a *app) enableAgentsOnInit(out io.Writer, v *vault.Vault, cfg *config.Config, agents []*agent.Agent, requested []string) error {
	fmt.Fprintf(out, "vault: %s\n\nagents:\n", v.Root())

	for _, ag := range agents {
		if err := enableOnInit(out, cfg, ag, requested); err != nil {
			return err
		}
	}

	return nil
}

// applyInitMigrationDefaults prints the one-time v3 migration lines; the trust
// lift for canon hooks is persisted by the config save that follows.
func (a *app) applyInitMigrationDefaults(e *engine.Engine, out io.Writer) {
	migration := engine.Report{}
	e.ApproveCanonHooks(&migration)

	for _, note := range migration.Notes {
		fmt.Fprintln(out, "note: "+note)
	}

	for _, warning := range migration.Warnings {
		fmt.Fprintln(out, "warning: "+warning)
	}
}

// installDaemonOnInitStep runs the daemon part of init and reports whether the
// `beadle daemon install` hint belongs in the next steps. A temporary home or
// vault skips the service with a note — an explicit --daemon is refused — so
// init never leaves a unit that outlives its temporary tree.
func (a *app) installDaemonOnInitStep(cmd *cobra.Command, out io.Writer, forceDaemon, installDaemon bool) (bool, error) {
	if !installDaemon {
		fmt.Fprintln(out, "\ndaemon: skipped (--no-daemon)")

		return false, nil
	}

	if spec, vaultRoot, err := a.daemonSpec(); err == nil {
		if refusal := a.temporaryDaemonRefusal(spec, vaultRoot, "use a permanent HOME/BEADLE_HOME or pass --no-daemon"); refusal != nil {
			if forceDaemon {
				return false, refusal
			}

			fmt.Fprintln(out, "\ndaemon: skipped (temporary home)")

			// No install hint: the explicit command would refuse the same
			// temporary path.
			return false, nil
		}
	}

	if forceDaemon {
		return false, a.installDaemonOnInit(cmd, out, true)
	}

	if err := a.installDaemonOnInit(cmd, out, false); err != nil {
		fmt.Fprintf(out, "\ndaemon: not installed (%v); run `beadle daemon install` when ready\n", err)

		return true, nil
	}

	return false, nil
}

// autoEnableBundlesOnInit makes the unattended bundle attempt at init time:
// a host CLI registers the rendered bundle right away, while a host without
// its CLI gets the instruction instead and keeps file sync on.
func (a *app) autoEnableBundlesOnInit(cmd *cobra.Command, e *engine.Engine, out io.Writer) error {
	if !a.bundleAutoEnable {
		return nil
	}

	report, err := e.AutoEnableBundles(cmd.Context())
	if err != nil {
		return err
	}

	printBundleSection(out, report.Bundles)

	for _, warning := range report.Warnings {
		fmt.Fprintln(out, "  ! "+warning)
	}

	return nil
}

// enableProjectDefaultsOnInit enables the present project files of a git
// checkout, keeping secrets out: the per-file opt-in stays with
// `beadle project enable <file> --allow-secrets`.
func (a *app) enableProjectDefaultsOnInit(cmd *cobra.Command, e *engine.Engine, out io.Writer) error {
	if !a.projectAutoEnable {
		return nil
	}

	enabled, err := e.ProjectEnableDetected(cmd.Context())
	if err != nil {
		return err
	}

	if len(enabled) == 0 {
		return nil
	}

	fmt.Fprintf(out, "\nproject: enabled %s (secrets stay out; `beadle project enable <file> --allow-secrets` to materialize them)\n",
		strings.Join(enabled, ", "))

	return nil
}

func (a *app) installDaemonOnInit(cmd *cobra.Command, out io.Writer, force bool) error {
	spec, _, err := a.daemonSpec()
	if err != nil {
		return err
	}

	if !force {
		status, err := daemonpkg.Check(spec.Home, spec.Label, a.checkRunner())
		if err != nil {
			return err
		}

		if status.Installed {
			fmt.Fprintf(out, "\ndaemon: already installed (%s)\n", status.Path)

			return nil
		}
	}

	path, err := daemonpkg.Install(cmd.Context(), spec, a.installRunner())
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "\ndaemon: installed and started (%s)\n", path)

	if runtime.GOOS == "linux" {
		fmt.Fprintln(out, "hint: run `loginctl enable-linger $USER` so the service survives logout")
	}

	return nil
}

func printInitNext(out io.Writer, daemonHint bool) {
	next := "\nnext:\n" +
		"  beadle sync --dry-run   # preview the first synchronization, nothing is written\n" +
		"  beadle sync             # synchronize; conflicts wait for `beadle resolve`\n"

	if daemonHint {
		next += "  beadle daemon install   # keep everything in sync in the background\n"
	}

	next += "\nAre you an AI agent? Run `beadle guide`.\n"

	fmt.Fprint(out, next)
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

// resolveAgentIDs validates the agent ids a user typed and returns their
// canonical form, so a historical id selects the same agent as the id the
// vault is keyed by and is stored under that id.
func resolveAgentIDs(agents []*agent.Agent, ids []string) ([]string, error) {
	canonical := make([]string, 0, len(ids))

	for _, id := range ids {
		ag := agent.ByID(agents, id)
		if ag == nil {
			return nil, fmt.Errorf("unknown agent %q (see beadle agents)", id)
		}

		canonical = append(canonical, ag.ID)
	}

	return canonical, nil
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

// absorbStandaloneVergerHome moves a `~/.verger` created before the vault
// existed into the vault, and says so.
//
// It prints nothing when there was no standalone home, because "moved" with
// nothing to move is worse than silence: init's output is a plan a user reads
// before approving it. A move that fails is not fatal either - the vault is
// still a working vault, and refusing to finish init over a leftover
// directory would be a worse outcome than saying what could not be done.
func (a *app) absorbStandaloneVergerHome(ctx context.Context, v *vault.Vault, out io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil //nolint:nilerr // no home to look in: nothing to absorb
	}

	source := filepath.Join(home, ".verger")

	// Deliberately not `a.pluginClient`: opening the vault's plugin client is
	// what creates the target directory, and once it exists verger merges
	// instead of renaming - and the merge keeps the default spec it just wrote
	// over the user's standalone one.
	merge, err := vergerx.AbsorbStandaloneHome(ctx, v.Root(), source, a.logger)
	if err != nil {
		fmt.Fprintf(out, "  standalone %s was left alone: %v\n", source, err)

		return nil
	}

	if merge == nil {
		return nil
	}

	if merge.Report.Renamed {
		fmt.Fprintf(out, "  moved the standalone plugin home %s into %s\n", merge.Report.HomeFrom, merge.Report.HomeTo)
	} else {
		fmt.Fprintf(out, "  merged %d standalone plugin path(s) from %s into %s\n",
			len(merge.Report.Moved), merge.Report.HomeFrom, merge.Report.HomeTo)
	}

	if n := len(merge.Report.Skipped); n > 0 {
		fmt.Fprintf(out, "  %d path(s) were already in the vault and were kept there\n", n)
	}

	for _, id := range merge.Conflicts {
		fmt.Fprintf(out, "  kept the vault's %q; the standalone one is in %s\n", id, merge.Backup)
	}

	return nil
}
