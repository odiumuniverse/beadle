package fsutil

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// Which writer pays which barrier.
//
// An fsync buys durability against power loss and kernel panic, not against
// process death: a child killed with SIGKILL leaves its bytes in the page cache
// and they read back fine with or without the barrier. So no runtime test on
// this machine can observe whether the barrier was taken. What is observable is
// which writer a call went through, because that is a choice the code makes
// before any syscall — and that is what these counts pin.
//
// Each mutation below is the one the corresponding row has to kill:
//
//	WriteFileAtomic   → 1 file sync, 1 dir sync   dropping the directory sync from the durable path
//	WriteFileAtomicCAS→ 0 file syncs               deleting the `if durable` in front of the file barrier
//	SyncDirs("a","b") → 2 dir syncs                syncing only its first argument
//
// The counters are declared inside the Convey per case, not outside the loop:
// a counter that carries over would let the CAS row pass for the wrong reason,
// which is the failure this suite exists to prevent.
func TestWhichWriterPaysWhichBarrier(t *testing.T) {
	Convey("Given counters on the two barriers", t, func() {
		realFile, realDir := fileSync, syncDir

		defer func() { fileSync, syncDir = realFile, realDir }()

		cases := []struct {
			name              string
			write             func(path string) error
			then              func()
			wantFile, wantDir int
		}{
			{
				"the durable writer pays both",
				func(p string) error { return WriteFileAtomic(p, []byte("x"), 0o600) }, nil, 1, 1,
			},
			{
				"the CAS writer pays neither",
				func(p string) error { return WriteFileAtomicCAS(p, []byte("x"), 0o600) }, nil, 0, 0,
			},
			{
				"the batch flush pays one directory per directory",
				func(string) error { return nil }, func() { _ = SyncDirs("a", "b") }, 0, 2,
			},
		}

		for _, c := range cases {
			Convey("Then "+c.name, func() {
				fileSyncs, dirSyncs := 0, 0

				fileSync = func(f *os.File) error {
					fileSyncs++

					return f.Sync()
				}
				syncDir = func(string) error {
					dirSyncs++

					return nil
				}

				So(c.write(filepath.Join(t.TempDir(), "object")), ShouldBeNil)

				if c.then != nil {
					c.then()
				}

				So(fileSyncs, ShouldEqual, c.wantFile)
				So(dirSyncs, ShouldEqual, c.wantDir)
			})
		}
	})
}
