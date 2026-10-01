package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The price of the write-temp-fsync-rename-fsync-dir discipline, isolated from
// everything else a sync does. On darwin a File.Sync is F_FULLFSYNC — it asks
// the drive to flush its own cache — so each call costs tens of milliseconds. On
// linux the same call is a cheap journal barrier.
//
// The two disciplines are spelled out here rather than reached through a
// production flag: a benchmark that calls the shipped code cannot compare two
// versions of it, and a `sync bool` parameter added only for measurement would
// be a parameter nobody ever passes differently in production.
func benchWrite(b *testing.B, durable bool) {
	b.Helper()

	dir := b.TempDir()
	data := make([]byte, 4096)

	for i := range data {
		data[i] = byte(i)
	}

	b.ResetTimer()

	for i := range b.N {
		name := filepath.Join(dir, fmt.Sprintf("obj-%d", i))

		if err := writeOne(name, data, durable); err != nil {
			b.Fatal(err)
		}
	}
}

func writeOne(path string, data []byte, durable bool) error {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}

	tmpName := tmp.Name()

	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()

		return err
	}

	if durable {
		if err := tmp.Sync(); err != nil {
			_ = tmp.Close()

			return err
		}
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}

	if err := os.Rename(tmpName, path); err != nil {
		return err
	}

	if !durable {
		return nil
	}

	d, err := os.Open(dir) //nolint:gosec // G304: the directory this benchmark just wrote into
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	return d.Sync()
}

// BenchmarkWriteFileAtomic measures the discipline as shipped.
func BenchmarkWriteFileAtomic(b *testing.B) {
	benchWrite(b, true)
}

// BenchmarkWriteTempRenameNoSync measures the floor a content-addressed write
// would sit at: same atomicity, no per-file fsync and no directory fsync.
func BenchmarkWriteTempRenameNoSync(b *testing.B) {
	benchWrite(b, false)
}
