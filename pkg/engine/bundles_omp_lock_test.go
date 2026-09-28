package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/plugin"
)

// TestOmpPluginLockSerializesWriters holds the shared lock the way verger does
// and asserts beadle refuses to mutate omp's state instead of interleaving
// with the other writer, then takes it once the other writer released it.
func TestOmpPluginLockSerializesWriters(t *testing.T) {
	Convey("Given the shared omp plugin lock is free", t, func() {
		home := t.TempDir()
		path := filepath.Join(plugin.OmpRoot(home), ompPluginLockFile)

		previous := ompPluginLockWait
		ompPluginLockWait = 100 * time.Millisecond

		defer func() { ompPluginLockWait = previous }()

		e := &Engine{home: home}

		Convey("Then a writer takes it", func() {
			report := &Report{}

			release, locked := e.lockOmpPlugin(report)
			So(locked, ShouldBeTrue)
			So(report.Warnings, ShouldBeEmpty)
			release()

			So(os.MkdirAll(filepath.Dir(path), 0o700), ShouldBeNil)

			So(flock.New(path).Path(), ShouldEqual, path)
		})

		Convey("When another writer (verger) holds it", func() {
			So(os.MkdirAll(filepath.Dir(path), 0o700), ShouldBeNil)

			held := flock.New(path)

			locked, err := held.TryLock()
			So(err, ShouldBeNil)
			So(locked, ShouldBeTrue)

			report := &Report{}

			release, took := e.lockOmpPlugin(report)

			Convey("Then the lock is not taken and the caller is told", func() {
				So(took, ShouldBeFalse)
				So(report.Warnings, ShouldNotBeEmpty)
				So(report.Warnings[0], ShouldContainSubstring, "omp bundle stays on the manual path")

				release()

				Convey("And once the other writer releases it, beadle takes it", func() {
					So(held.Unlock(), ShouldBeNil)

					release, took := e.lockOmpPlugin(&Report{})
					So(took, ShouldBeTrue)
					release()
				})
			})
		})
	})
}
