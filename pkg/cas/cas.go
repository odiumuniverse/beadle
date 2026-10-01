package cas

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
)

var ErrInvalidHash = errors.New("invalid content hash")

// ErrCorrupt reports an object whose bytes do not hash to its own name — what a
// crash during a barrier-free write looks like.
var ErrCorrupt = errors.New("corrupt blob")

const hashLen = sha256.Size * 2

type Hash string

type Store struct {
	dir string

	// dirs collects the directories a batch of writes landed in, so one
	// SyncDir at the end of the batch makes them durable instead of a barrier
	// per object. See fsutil.WriteFileAtomicCAS for why the per-object barrier
	// is not worth its price on darwin.
	dirs map[string]struct{}
}

func NewStore(dir string) *Store {
	return &Store{dir: dir, dirs: make(map[string]struct{})}
}

func HashOf(data []byte) Hash {
	sum := sha256.Sum256(data)

	return Hash(hex.EncodeToString(sum[:]))
}

func (s *Store) Put(data []byte) (Hash, error) {
	h := HashOf(data)
	// Presence is not integrity. An object that exists but does not hash to its
	// own name is exactly what a crash during a barrier-free write leaves
	// behind, and Has alone would skip it forever. So the bytes are checked
	// once here and a corrupt object is rewritten. This is the invariant that
	// pays for dropping the per-object barrier: the store repairs itself from
	// the canon, and a torn object can never be served as intact.
	if s.Has(h) {
		if _, err := s.Get(h); err == nil {
			return h, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(s.path(h)), 0o700); err != nil {
		return "", fmt.Errorf("create blob directory: %w", err)
	}

	if err := fsutil.WriteFileAtomicCAS(s.path(h), data, 0o600); err != nil {
		return "", fmt.Errorf("write blob: %w", err)
	}

	s.dirs[filepath.Dir(s.path(h))] = struct{}{}

	return h, nil
}

// Sync flushes the directories this batch of writes touched, once. It is the
// barrier that replaced the per-object ones: the object itself is recoverable
// by hash, but the rename that made it reachable is only durable after this.
func (s *Store) Sync() error {
	if err := fsutil.SyncDirs(slices.Collect(maps.Keys(s.dirs))...); err != nil {
		return fmt.Errorf("sync blob directories: %w", err)
	}

	clear(s.dirs)

	return nil
}

func (s *Store) Get(h Hash) ([]byte, error) {
	if _, err := ParseHash(string(h)); err != nil {
		return nil, err
	}

	data, err := os.ReadFile(s.path(h))
	if err != nil {
		return nil, fmt.Errorf("read blob %s: %w", h, err)
	}

	// A blob is named by the hash of its contents, so the name is a promise the
	// store can check for free. An object written without a per-file barrier
	// and cut short by a crash can therefore never be served as if it were
	// intact: it is reported, and Put rewrites it from the canon.
	if got := HashOf(data); got != h {
		return nil, fmt.Errorf("%w: blob %s holds %s", ErrCorrupt, h, got)
	}

	return data, nil
}

func (s *Store) Has(h Hash) bool {
	if _, err := ParseHash(string(h)); err != nil {
		return false
	}

	_, err := os.Stat(s.path(h))

	return err == nil
}

func ParseHash(s string) (Hash, error) {
	if len(s) != hashLen {
		return "", fmt.Errorf("%w: %q", ErrInvalidHash, s)
	}

	if _, err := hex.DecodeString(s); err != nil {
		return "", fmt.Errorf("%w: %q", ErrInvalidHash, s)
	}

	return Hash(s), nil
}

func (s *Store) path(h Hash) string {
	name := string(h)

	return filepath.Join(s.dir, name[:2], name[2:])
}
