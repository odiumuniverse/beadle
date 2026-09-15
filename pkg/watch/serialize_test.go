package watch

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/require"
	"github.com/vmkteam/embedlog"
)

func TestSerializeCoalescesConcurrentCalls(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})

		var calls atomic.Int32

		run := serialize(t.Context(), func(context.Context) error {
			if calls.Add(1) == 1 {
				<-release
			}

			return nil
		}, embedlog.Logger{})

		go run()

		synctest.Wait()

		run()
		run()

		require.Equal(t, int32(1), calls.Load())

		close(release)
		synctest.Wait()

		require.Equal(t, int32(2), calls.Load())
	})
}
