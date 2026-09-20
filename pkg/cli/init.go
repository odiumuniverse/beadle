package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	daemonpkg "github.com/odiumuniverse/beadle/pkg/daemon"
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
			"It writes nothing into any agent: run `beadle sync --dry-run` to preview the\n" +
			"first synchronization, then `beadle sync`.\n" +
			"With --daemon it also installs the background watcher; --no-daemon opts out\n" +
			"explicitly, and without either flag init only prints the hint.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if daemon && noDaemon {
				return errors.New("choose either --daemon or --no-daemon")
			}

			return a.runInit(cmd, agentIDs, daemon, !daemon && !noDaemon)
		},
	}

	cmd.Flags().StringSliceVar(&agentIDs, "agents", nil, "enable exactly these agents instead of the detected ones")
	cmd.Flags().BoolVar(&daemon, "daemon", false, "install and start the background watcher")
	cmd.Flags().BoolVar(&noDaemon, "no-daemon", false, "do not install the background watcher, print no hint")

	return cmd
}

func (a *app) runInit(cmd *cobra.Command, agentIDs []string, withDaemon, daemonHint bool) error {
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

	agents, err := allAgents()
	if err != nil {
		return err
	}

	if err := validateAgentIDs(agents, agentIDs); err != nil {
		return err
	}

	out := cmd.OutOrStdout()

	fmt.Fprintf(out, "vault: %s\n\nagents:\n", v.Root())

	for _, ag := range agents {
		if err := enableOnInit(out, cfg, ag, agentIDs); err != nil {
			return err
		}
	}

	if err := cfg.Save(v.ConfigPath()); err != nil {
		return err
	}

	if _, err := skills.Seed(v.SkillsDir(), false); err != nil {
		return err
	}

	fmt.Fprintln(out)
	printModes(out, cfg, agents)

	if withDaemon {
		if err := a.installDaemonOnInit(cmd, out); err != nil {
			return err
		}
	}

	printInitNext(out, daemonHint)

	return nil
}

func (a *app) installDaemonOnInit(cmd *cobra.Command, out io.Writer) error {
	spec, err := a.daemonSpec()
	if err != nil {
		return err
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
