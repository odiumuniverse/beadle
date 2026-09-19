package cas

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
)

var ErrInvalidHash = errors.New("invalid content hash")

const hashLen = sha256.Size * 2

type Hash string

type Store struct {
	dir string
}

func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func HashOf(data []byte) Hash {
	sum := sha256.Sum256(data)

	return Hash(hex.EncodeToString(sum[:]))
}

func (s *Store) Put(data []byte) (Hash, error) {
	h := HashOf(data)
	if s.Has(h) {
		return h, nil
	}

	if err := os.MkdirAll(filepath.Dir(s.path(h)), 0o700); err != nil {
		return "", fmt.Errorf("create blob directory: %w", err)
	}

	if err := fsutil.WriteFileAtomic(s.path(h), data, 0o600); err != nil {
		return "", fmt.Errorf("write blob: %w", err)
	}

	return h, nil
}

func (s *Store) Get(h Hash) ([]byte, error) {
	if _, err := ParseHash(string(h)); err != nil {
		return nil, err
	}

	data, err := os.ReadFile(s.path(h))
	if err != nil {
		return nil, fmt.Errorf("read blob %s: %w", h, err)
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
