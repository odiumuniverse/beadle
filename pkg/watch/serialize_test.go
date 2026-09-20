package watch

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"

	. "github.com/smartystreets/goconvey/convey"
	"github.com/vmkteam/embedlog"
)

func TestSerializeCoalescesConcurrentCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		Convey("Given a serialized runner with one call in flight", t, func() {
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

			Convey("When two more calls arrive while it is busy", func() {
				run()
				run()

				So(calls.Load(), ShouldEqual, int32(1))

				Convey("Then they coalesce into one follow-up call", func() {
					close(release)
					synctest.Wait()

					So(calls.Load(), ShouldEqual, int32(2))
				})
			})
		})
	})
}
