package cli

import (
	"context"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/watch"
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
			e, err := a.engine(engine.WithUnattended())
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

// watchSync runs one unattended sync: a service manager may start the watcher
// without the user's shell PATH, so the engine reaches host CLIs through the
// locations attended runs recorded and records none itself.
func (a *app) watchSync(ctx context.Context) error {
	e, err := a.engine(engine.WithUnattended())
	if err != nil {
		return err
	}

	report, err := e.Sync(ctx, engine.SyncOptions{})
	if err != nil {
		return err
	}

	a.logSyncReport(ctx, report)

	if report.VaultChanged() || report.Pushed() {
		a.logger.Print(ctx, "synced", "vault_changed", report.VaultChanged(), "pushed", report.Pushed())
	}

	return nil
}

// logSyncReport writes the lines a background sync must not lose: the
// interactive report is not printed there, so warnings and bundle results
// (including the explicit retry command of a failed probe) would otherwise
// vanish from the daemon log.
func (a *app) logSyncReport(ctx context.Context, report *engine.Report) {
	for _, message := range report.Errors() {
		a.logger.Error(ctx, "sync error", "error", message)
	}

	for _, warning := range report.Warnings {
		a.logger.Print(ctx, "sync warning", "warning", warning)
	}

	for _, note := range report.Notes {
		a.logger.Print(ctx, "sync note", "note", note)
	}

	for _, line := range bundleLines(report.Bundles) {
		a.logger.Print(ctx, "bundle: "+strings.TrimSpace(line))
	}

	if n := len(report.Conflicts); n > 0 {
		a.logger.Error(ctx, "open conflicts wait for beadle resolve", "conflicts", n)
	}
}
