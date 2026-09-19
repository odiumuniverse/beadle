package cas_test

import (
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/cas"
)

func TestHashOf(t *testing.T) {
	t.Parallel()

	h := cas.HashOf([]byte("hello"))
	require.Len(t, string(h), 64)
	require.Equal(t, h, cas.HashOf([]byte("hello")))
	require.NotEqual(t, h, cas.HashOf([]byte("world")))

	_, err := cas.ParseHash(string(h))
	require.NoError(t, err)
}

func TestStorePutGet(t *testing.T) {
	t.Parallel()

	store := cas.NewStore(filepath.Join(t.TempDir(), "objects"))

	h, err := store.Put([]byte("content"))
	require.NoError(t, err)
	require.True(t, store.Has(h))

	got, err := store.Get(h)
	require.NoError(t, err)
	require.Equal(t, "content", string(got))
}

func TestStorePutIdempotent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := cas.NewStore(dir)

	h1, err := store.Put([]byte("same"))
	require.NoError(t, err)

	h2, err := store.Put([]byte("same"))
	require.NoError(t, err)
	require.Equal(t, h1, h2)

	count := 0

	walkErr := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !d.IsDir() {
			count++
		}

		return nil
	})
	require.NoError(t, walkErr)
	require.Equal(t, 1, count)
}

func TestStoreGetMissing(t *testing.T) {
	t.Parallel()

	store := cas.NewStore(t.TempDir())
	h := cas.HashOf([]byte("absent"))

	_, err := store.Get(h)
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func TestStoreRejectsInvalidHash(t *testing.T) {
	t.Parallel()

	store := cas.NewStore(t.TempDir())

	shortHash := cas.Hash(string(cas.HashOf([]byte("x")))[:63])

	for _, bad := range []cas.Hash{"", "zz", "../../etc/passwd", shortHash} {
		_, err := store.Get(bad)
		require.ErrorIs(t, err, cas.ErrInvalidHash, "hash %q", bad)

		require.False(t, store.Has(bad))
	}

	require.False(t, store.Has(cas.Hash("")))
}
