package cli

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/agents-sync/pkg/adapter"
	"github.com/odiumuniverse/agents-sync/pkg/config"
	syncer "github.com/odiumuniverse/agents-sync/pkg/sync"
	"github.com/odiumuniverse/agents-sync/pkg/watch"
)

func (a *app) newWatchCmd() *cobra.Command {
	var (
		debounce time.Duration
		interval time.Duration
	)

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Watch configuration files and synchronize continuously",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := a.watchPaths()
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

func (a *app) watchPaths() ([]string, error) {
	v, err := a.resolveVault()
	if err != nil {
		return nil, err
	}

	cfg, err := config.Load(v.ConfigPath())
	if err != nil {
		return nil, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	paths := []string{
		filepath.Join(v.Root(), "rules"),
		filepath.Join(v.Root(), "skills"),
		filepath.Join(v.Root(), "mcp", "servers.json"),
		filepath.Join(v.Root(), "permissions", "rules.json"),
		filepath.Join(v.Root(), "permissions", "override"),
	}

	for _, item := range adapter.All(home) {
		if !cfg.Agents[item.ID()].Enabled {
			continue
		}

		detected, err := item.Detect()
		if err != nil {
			return nil, err
		}

		if !detected {
			continue
		}

		paths = append(paths, item.WatchPaths()...)
	}

	return paths, nil
}

func (a *app) watchSync(ctx context.Context) error {
	engine, err := a.engine()
	if err != nil {
		return err
	}

	report, err := engine.Run(ctx, syncer.ModeSync)
	if err != nil {
		return err
	}

	conflicts := report.Rules.ConflictCount() + report.MCP.ConflictCount() +
		report.Skills.ConflictCount() + report.Permissions.ConflictCount()

	if conflicts > 0 {
		a.logger.Error(ctx, "conflicts detected", "conflicts", conflicts)

		return nil
	}

	if report.Rules.Changed || report.MCP.Changed || report.Skills.Changed || report.Permissions.Changed {
		a.logger.Print(ctx, "synced",
			"rules", report.Rules.Changed,
			"mcp", report.MCP.Changed,
			"skills", report.Skills.Changed,
			"permissions", report.Permissions.Changed,
		)
	}

	return nil
}
