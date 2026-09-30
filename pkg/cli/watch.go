package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/vergerx"
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
			ctx := cmd.Context()

			// The plugin half is settled before the sync loop starts, so a
			// competitor arriving a moment later is told who holds the lease
			// rather than both writing at once.
			release, err := a.startPluginWatch(ctx)
			if err != nil {
				return err
			}

			if release != nil {
				defer release()
			}

			e, err := a.engine(engine.WithUnattended())
			if err != nil {
				return err
			}

			paths, err := e.WatchPaths(ctx)
			if err != nil {
				return err
			}

			// The sync loop is beadle's own and is never given up: a standalone
			// `verger watch` reconciles plugins, not the vault, so losing the
			// lease must not stop beadle syncing.
			return watch.Run(ctx, watch.Options{
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
		fmt.Fprintf(a.errOut, "synced vault_changed=%t pushed=%t\n", report.VaultChanged(), report.Pushed())
	}

	return nil
}

// logSyncReport writes the lines a background sync must not lose: the
// interactive report is not printed there, so warnings and bundle results
// (including the explicit retry command of a failed probe) would otherwise
// vanish from the daemon log.
func (a *app) logSyncReport(ctx context.Context, report *engine.Report) {
	for _, message := range report.Errors() {
		fmt.Fprintf(a.errOut, "sync error: %s\n", message)
	}

	for _, warning := range report.Warnings {
		fmt.Fprintf(a.errOut, "sync warning: %s\n", warning)
	}

	for _, note := range report.Notes {
		fmt.Fprintf(a.errOut, "sync note: %s\n", note)
	}

	for _, line := range bundleLines(report.Bundles) {
		fmt.Fprintf(a.errOut, "bundle: %s\n", strings.TrimSpace(line))
	}

	if n := len(report.Conflicts); n > 0 {
		a.logger.Error(ctx, "open conflicts wait for beadle resolve", "conflicts", n)
	}
}

// startPluginWatch takes the verger watch lease for beadle and, when it gets
// it, starts verger's plugin watcher. The returned func hands the lease back
// and is nil when another live process already holds it.
//
// A held lease is not a failure of this command. The holder is named with its
// pid and the sync loop carries on, because the other process is watching
// plugins and syncing the vault is this command's own job. Releasing on stop
// is what lets a standalone `verger watch` take over after beadle exits rather
// than wait out a stale file.
func (a *app) startPluginWatch(ctx context.Context) (func(), error) {
	v, _, err := a.loadConfig()
	if err != nil {
		return nil, err
	}

	client, err := a.pluginClient(v, nil)
	if err != nil {
		return nil, err
	}

	outcome, err := client.AcquirePluginWatch(ctx, vergerx.WatchOwner)
	if err != nil {
		return nil, err
	}

	if outcome.HeldBy != "" {
		fmt.Fprintf(a.errOut, "plugins are watched by %s (pid %d)\n", outcome.HeldBy, outcome.PID)

		return nil, nil
	}

	return outcome.Release, nil
}
