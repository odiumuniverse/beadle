package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	daemonpkg "github.com/odiumuniverse/beadle/pkg/daemon"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/skills"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

func (a *app) newInitCmd() *cobra.Command {
	var (
		agentIDs         []string
		daemon, noDaemon bool
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

			return a.runInit(cmd, agentIDs, daemon, !noDaemon)
		},
	}

	cmd.Flags().StringSliceVar(&agentIDs, "agents", nil, "enable exactly these agents instead of the detected ones")
	cmd.Flags().BoolVar(&daemon, "daemon", false, "install the watcher even if a service file already exists")
	cmd.Flags().BoolVar(&noDaemon, "no-daemon", false, "do not install the background watcher")

	return cmd
}

func (a *app) runInit(cmd *cobra.Command, agentIDs []string, forceDaemon, installDaemon bool) error {
	root, err := vault.ResolveRoot(a.vaultPath, os.Getenv(vault.EnvHome))
	if err != nil {
		return err
	}

	v := vault.New(root)
	if err := v.Init(); err != nil {
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

	if err := validateAgentIDs(agents, agentIDs); err != nil {
		return err
	}

	out := cmd.OutOrStdout()

	if err := a.enableAgentsOnInit(out, v, cfg, agents, agentIDs); err != nil {
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
// `beadle daemon install` hint belongs in the next steps.
func (a *app) installDaemonOnInitStep(cmd *cobra.Command, out io.Writer, forceDaemon, installDaemon bool) (bool, error) {
	switch {
	case !installDaemon:
		fmt.Fprintln(out, "\ndaemon: skipped (--no-daemon)")

		return false, nil
	case forceDaemon:
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
	if !bundleAutoEnable {
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
	if !projectAutoEnable {
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
	spec, err := a.daemonSpec()
	if err != nil {
		return err
	}

	if !force {
		status, err := daemonpkg.Check(spec.Home, spec.Label, daemonCheckRunner)
		if err != nil {
			return err
		}

		if status.Installed {
			fmt.Fprintf(out, "\ndaemon: already installed (%s)\n", status.Path)

			return nil
		}
	}

	path, err := daemonpkg.Install(cmd.Context(), spec, daemonInstallRunner)
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

func validateAgentIDs(agents []*agent.Agent, ids []string) error {
	for _, id := range ids {
		if agent.ByID(agents, id) == nil {
			return fmt.Errorf("unknown agent %q (see beadle agents)", id)
		}
	}

	return nil
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
