package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/vmkteam/embedlog"
)

type Options struct {
	Version string
}

func Execute(opts Options) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := newRootCmd(opts)
	root.SilenceUsage = true
	root.SilenceErrors = true

	return root.ExecuteContext(ctx)
}

func newRootCmd(opts Options) *cobra.Command {
	a := &app{logger: embedlog.NewDevLogger()}

	var verbose, logJSON bool

	root := &cobra.Command{
		Use:   "beadle",
		Short: "One configuration for every AI coding agent",
		Long: "beadle keeps the rules, MCP servers, skills and permissions of your AI coding\n" +
			"agents (Claude Code, OpenCode, Gemini CLI, Cursor, ...) in sync through a vault\n" +
			"you own. Change them in any agent: the others follow. Real files, 3-way merges,\n" +
			"no symlinks; conflicts wait for you instead of being guessed.",
		Version: opts.Version,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			if verbose || logJSON {
				a.logger = embedlog.NewLogger(verbose, logJSON)
			}
		},
	}

	root.PersistentFlags().StringVar(&a.vaultPath, "vault", "", "vault root (default: $BEADLE_HOME or ~/.beadle)")
	root.PersistentFlags().BoolVar(&verbose, "verbose", false, "enable info-level logs")
	root.PersistentFlags().BoolVar(&logJSON, "log-json", false, "log in JSON format")

	root.AddCommand(
		a.newInitCmd(),
		a.newStatusCmd(),
		a.newSyncCmd(),
		a.newPullCmd(),
		a.newPushCmd(),
		a.newDiffCmd(),
		a.newConflictsCmd(),
		a.newResolveCmd(),
		a.newRulingsCmd(),
		a.newDoctorCmd(),
		a.newHistoryCmd(),
		a.newRestoreCmd(),
		a.newHealCmd(),
		a.newAgentsCmd(),
		a.newKindsCmd(),
		a.newPluginsCmd(),
		a.newHooksCmd(),
		a.newBundlesCmd(),
		a.newSkillsCmd(),
		a.newWatchCmd(),
		a.newDaemonCmd(),
		a.newSecretsCmd(),
		a.newProjectCmd(),
	)

	return root
}
