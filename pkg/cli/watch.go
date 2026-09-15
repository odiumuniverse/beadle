package cli

import (
	"context"
	"time"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/agents-sync/pkg/engine"
	"github.com/odiumuniverse/agents-sync/pkg/watch"
)

func (a *app) newWatchCmd() *cobra.Command {
	var (
		debounce time.Duration
		interval time.Duration
	)

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Watch agent configs and the vault, and synchronize on every change",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := a.engine()
			if err != nil {
				return err
			}

			paths, err := e.WatchPaths(cmd.Context())
			if err != nil {
				return err
			}

			return watch.Run(cmd.Context(), watch.Options{
				Paths:    paths,
				Debounce: debounce,
				Interval: interval,
				Log:      a.logger,
				Sync:     a.watchSync,
			})
		},
	}

	cmd.Flags().DurationVar(&debounce, "debounce", watch.DefaultDebounce, "quiet period after the last change")
	cmd.Flags().DurationVar(&interval, "interval", watch.DefaultInterval, "periodic rescan interval (zero = default, negative = off)")

	return cmd
}

func (a *app) watchSync(ctx context.Context) error {
	e, err := a.engine()
	if err != nil {
		return err
	}

	report, err := e.Sync(ctx, engine.SyncOptions{})
	if err != nil {
		return err
	}

	for _, message := range report.Errors() {
		a.logger.Error(ctx, "sync error", "error", message)
	}

	if n := len(report.Conflicts); n > 0 {
		a.logger.Error(ctx, "open conflicts wait for agent-sync resolve", "conflicts", n)
	}

	if report.VaultChanged() || report.Pushed() {
		a.logger.Print(ctx, "synced", "vault_changed", report.VaultChanged(), "pushed", report.Pushed())
	}

	return nil
}
