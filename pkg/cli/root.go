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
		Use:     "agent-sync",
		Short:   "Synchronize AI agent configs via a single-source-of-truth vault",
		Version: opts.Version,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			if verbose || logJSON {
				a.logger = embedlog.NewLogger(verbose, logJSON)
			}
		},
	}

	root.PersistentFlags().StringVar(&a.vaultPath, "vault", "", "vault root (default: $AGENTSYNC_HOME or ~/.agent-sync)")
	root.PersistentFlags().BoolVar(&verbose, "verbose", false, "enable info-level logs")
	root.PersistentFlags().BoolVar(&logJSON, "log-json", false, "log in JSON format")

	root.AddCommand(
		a.newInitCmd(),
		a.newStatusCmd(),
		a.newSyncCmd(),
		a.newPullCmd(),
		a.newPushCmd(),
		a.newDiffCmd(),
		a.newResolveCmd(),
		a.newDoctorCmd(),
		a.newRestoreCmd(),
		a.newWatchCmd(),
		a.newDaemonCmd(),
	)

	return root
}
