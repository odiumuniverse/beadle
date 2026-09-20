package watch_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/watch"
)

func waitForRun(t *testing.T, runs <-chan struct{}, what string) {
	t.Helper()

	select {
	case <-runs:
	case <-time.After(5 * time.Second):
		t.Fatalf("no %s", what)
	}
}

func TestWatchTriggersSync(t *testing.T) {
	Convey("Given a watcher over a directory with a file", t, func() {
		dir := t.TempDir()
		agentFile := filepath.Join(dir, "CLAUDE.md")
		So(os.WriteFile(agentFile, []byte("v1\n"), 0o600), ShouldBeNil)

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

		Convey("When the file changes", func() {
			So(os.WriteFile(agentFile, []byte("v2\n"), 0o600), ShouldBeNil)

			waitForRun(t, runs, "sync after change")

			Convey("Then canceling stops the watcher cleanly", func() {
				cancel()

				select {
				case err := <-done:
					So(err, ShouldBeNil)
				case <-time.After(2 * time.Second):
					t.Fatal("watcher did not stop")
				}
			})
		})
	})
}

func TestWatchRequiresSync(t *testing.T) {
	Convey("Given watcher options without a sync function", t, func() {
		Convey("When the watcher runs", func() {
			err := watch.Run(t.Context(), watch.Options{})

			Convey("Then it fails", func() {
				So(err, ShouldBeError)
			})
		})
	})
}
