package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
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

// TestOmpSharedFileLockGuardsTheMCPWrite pins the shared-file rule for the one
// MCP surface beadle and verger both write: while another writer holds
// <ompRoot>/.omp-plugin.verger.lock the omp MCP write is skipped with a note
// (never an unbounded wait and never a race), and every other surface is
// untouched by the lock.
func TestOmpSharedFileLockGuardsTheMCPWrite(t *testing.T) {
	Convey("Given omp's MCP file and a writer holding the shared lock", t, func() {
		home := t.TempDir()

		previous := ompPluginLockWait
		ompPluginLockWait = 100 * time.Millisecond

		defer func() { ompPluginLockWait = previous }()

		e := &Engine{home: home}
		spec := kind.Spec{ID: kind.MCP}
		v := &view{agent: agent.Omp(home, home)}

		Convey("Then a free lock is taken for the omp MCP file", func() {
			release, note := e.lockOmpMCPWrite(spec, v)
			So(note, ShouldBeEmpty)
			So(release, ShouldNotBeNil)
			release()
		})

		Convey("And no lock is taken for another agent or another kind", func() {
			other := &view{agent: agent.ClaudeCode(home, home)}

			release, note := e.lockOmpMCPWrite(spec, other)
			So(note, ShouldBeEmpty)
			So(release, ShouldBeNil)

			release, note = e.lockOmpMCPWrite(kind.Spec{ID: kind.Skills}, v)
			So(note, ShouldBeEmpty)
			So(release, ShouldBeNil)
		})

		Convey("When another writer holds the lock", func() {
			path := filepath.Join(plugin.OmpRoot(home), ompPluginLockFile)
			So(os.MkdirAll(filepath.Dir(path), 0o700), ShouldBeNil)

			held := flock.New(path)

			locked, err := held.TryLock()
			So(err, ShouldBeNil)
			So(locked, ShouldBeTrue)

			release, note := e.lockOmpMCPWrite(spec, v)

			Convey("Then the write is skipped with a bounded wait and a note", func() {
				So(release, ShouldBeNil)
				So(note, ShouldContainSubstring, "held by another writer")
				So(note, ShouldContainSubstring, "left to the other writer")

				Convey("And once the writer releases it, the lock is taken again", func() {
					So(held.Unlock(), ShouldBeNil)

					release, note := e.lockOmpMCPWrite(spec, v)
					So(note, ShouldBeEmpty)
					So(release, ShouldNotBeNil)
					release()
				})
			})
		})
	})
}
