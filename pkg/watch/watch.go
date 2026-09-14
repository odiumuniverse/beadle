package watch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/samber/lo"
	"github.com/vmkteam/embedlog"
)

const (
	DefaultDebounce = 500 * time.Millisecond
	DefaultInterval = 10 * time.Minute
)

var skippedDirNames = map[string]struct{}{
	"node_modules": {},
	".git":         {},
	".cache":       {},
	"cache":        {},
}

type Options struct {
	Paths    []string
	Debounce time.Duration
	Interval time.Duration
	Log      embedlog.Logger
	Sync     func(ctx context.Context) error
}

func Run(ctx context.Context, opts Options) error {
	if opts.Sync == nil {
		return errors.New("watch: Sync is required")
	}

	debounce := opts.Debounce
	if debounce == 0 {
		debounce = DefaultDebounce
	}

	interval := opts.Interval
	if interval == 0 {
		interval = DefaultInterval
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create watcher: %w", err)
	}

	defer func() { _ = watcher.Close() }()

	files := make(map[string]struct{})

	for _, path := range opts.Paths {
		watchPath(watcher, path, files, opts.Log)
	}

	debounced, cancelDebounce := lo.NewDebounce(max(debounce, 0), func() {
		if err := opts.Sync(ctx); err != nil {
			opts.Log.Error(ctx, "sync failed", "err", err)
		}
	})

	defer cancelDebounce()

	opts.Log.Print(ctx, "watching", "paths", len(opts.Paths))

	if err := opts.Sync(ctx); err != nil {
		opts.Log.Error(ctx, "initial sync failed", "err", err)
	}

	var ticks <-chan time.Time

	if interval > 0 {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		ticks = ticker.C
	}

	return loop(ctx, opts, watcher, files, debounced, ticks, debounce)
}

func loop(
	ctx context.Context,
	opts Options,
	watcher *fsnotify.Watcher,
	files map[string]struct{},
	debounced func(),
	ticks <-chan time.Time,
	debounce time.Duration,
) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}

			handleEvent(watcher, event, files, opts.Log)

			if debounce > 0 {
				debounced()

				continue
			}

			if err := opts.Sync(ctx); err != nil {
				opts.Log.Error(ctx, "sync failed", "err", err)
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}

			opts.Log.Error(ctx, "watcher error, rescanning", "err", err)
			debounced()
		case <-ticks:
			opts.Log.Print(ctx, "periodic rescan")

			debounced()
		}
	}
}

func watchPath(watcher *fsnotify.Watcher, path string, files map[string]struct{}, log embedlog.Logger) {
	if path == "" {
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		return
	}

	if info.IsDir() {
		watchDirTree(watcher, path, log)

		return
	}

	files[path] = struct{}{}

	if err := watcher.Add(path); err != nil {
		log.Error(context.Background(), "cannot watch file", "path", path, "err", err)
	}
}

func watchDirTree(watcher *fsnotify.Watcher, root string, log embedlog.Logger) {
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // unreadable entries are skipped deliberately
		}

		if !entry.IsDir() {
			return nil
		}

		if _, skip := skippedDirNames[entry.Name()]; path != root && skip {
			return filepath.SkipDir
		}

		if err := watcher.Add(path); err != nil {
			log.Error(context.Background(), "cannot watch directory", "path", path, "err", err)
		}

		return nil
	})
	if walkErr != nil {
		log.Error(context.Background(), "cannot walk directory", "path", root, "err", walkErr)
	}
}

func handleEvent(watcher *fsnotify.Watcher, event fsnotify.Event, files map[string]struct{}, log embedlog.Logger) {
	if _, ok := files[event.Name]; ok && event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		if info, err := os.Stat(event.Name); err == nil && !info.IsDir() {
			if err := watcher.Add(event.Name); err != nil {
				log.Error(context.Background(), "cannot re-watch file", "path", event.Name, "err", err)
			}
		}

		return
	}

	if event.Op&fsnotify.Create == 0 {
		return
	}

	info, err := os.Stat(event.Name)
	if err != nil || !info.IsDir() {
		return
	}

	watchDirTree(watcher, event.Name, log)
}
