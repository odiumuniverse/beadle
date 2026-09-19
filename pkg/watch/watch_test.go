package watch_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/watch"
)

func TestWatchTriggersSync(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	agentFile := filepath.Join(dir, "CLAUDE.md")
	require.NoError(t, os.WriteFile(agentFile, []byte("v1\n"), 0o600))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	runs := make(chan struct{}, 16)

	opts := watch.Options{
		Paths:    []string{dir},
		Debounce: 50 * time.Millisecond,
		Interval: -1,
		Sync: func(context.Context) error {
			runs <- struct{}{}

			return nil
		},
	}

	done := make(chan error, 1)

	go func() { done <- watch.Run(ctx, opts) }()

	waitForRun(t, runs, "initial sync")

	require.NoError(t, os.WriteFile(agentFile, []byte("v2\n"), 0o600))

	waitForRun(t, runs, "sync after change")

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not stop")
	}
}

func TestWatchRequiresSync(t *testing.T) {
	t.Parallel()

	err := watch.Run(t.Context(), watch.Options{})
	require.Error(t, err)
}

func waitForRun(t *testing.T, runs <-chan struct{}, what string) {
	t.Helper()

	select {
	case <-runs:
	case <-time.After(5 * time.Second):
		t.Fatalf("no %s", what)
	}
}
