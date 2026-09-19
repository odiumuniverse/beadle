package secret_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/secret"
)

type fakeKeyring struct {
	values     map[string]string
	gets       int
	sets       int
	deletes    int
	failGet    error
	failSet    error
	failDelete error
}

func newFakeKeyring(values map[string]string) *fakeKeyring {
	if values == nil {
		values = map[string]string{}
	}

	return &fakeKeyring{values: values}
}

func (f *fakeKeyring) Get(account string) (string, bool, error) {
	f.gets++

	if f.failGet != nil {
		return "", false, f.failGet
	}

	value, ok := f.values[account]

	return value, ok, nil
}

func (f *fakeKeyring) Set(account, value string) error {
	f.sets++

	if f.failSet != nil {
		return f.failSet
	}

	f.values[account] = value

	return nil
}

func (f *fakeKeyring) Delete(account string) (bool, error) {
	f.deletes++

	if f.failDelete != nil {
		return false, f.failDelete
	}

	if _, ok := f.values[account]; !ok {
		return false, nil
	}

	delete(f.values, account)

	return true, nil
}

func write(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func read(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: tests read their own temp files
	require.NoError(t, err)

	return string(data)
}

func keyringIndex(t *testing.T, path string, names ...string) {
	t.Helper()

	secrets := map[string]string{}

	for _, name := range names {
		secrets[name] = ""
	}

	encoded, err := json.Marshal(map[string]any{"version": 2, "backend": "keyring", "secrets": secrets})
	require.NoError(t, err)

	write(t, path, string(encoded)+"\n")
}

func keyringStore(t *testing.T, fake secret.Keyring, names ...string) (string, *secret.Store) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "mcp", "secrets.json")
	keyringIndex(t, path, names...)

	store, err := secret.Load(path, secret.WithKeyring(fake))
	require.NoError(t, err)

	return path, store
}

func TestKeyringStorePrefetch(t *testing.T) {
	t.Parallel()

	fake := newFakeKeyring(map[string]string{"ALPHA": "s3cr3t"})
	_, store := keyringStore(t, fake, "ALPHA")

	require.Equal(t, secret.BackendKeyring, store.Backend())
	require.Equal(t, 1, fake.gets)
	require.NoError(t, store.KeyringErr())
	require.NoError(t, store.Probe())

	value, ok := store.Get("ALPHA")
	require.True(t, ok)
	require.Equal(t, "s3cr3t", value)
}

func TestKeyringStorePrefetchFailure(t *testing.T) {
	t.Parallel()

	fake := newFakeKeyring(nil)
	fake.failGet = errors.New("keyring is locked")

	_, store := keyringStore(t, fake, "ALPHA")

	require.ErrorIs(t, store.KeyringErr(), fake.failGet)
	require.ErrorIs(t, store.Probe(), fake.failGet)

	_, ok := store.Get("ALPHA")
	require.False(t, ok)
	require.Empty(t, store.Names())
}

func TestKeyringStoreV1BackCompat(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "mcp", "secrets.json")
	write(t, path, `{"version": 1, "secrets": {"ALPHA": "s3cr3t"}}`)

	fake := newFakeKeyring(nil)

	store, err := secret.Load(path, secret.WithKeyring(fake))
	require.NoError(t, err)
	require.Equal(t, secret.BackendFile, store.Backend())
	require.Zero(t, fake.gets, "the file backend never touches the keyring")
	require.NoError(t, store.Probe())

	store.Set("BETA", "fresh")
	require.NoError(t, store.Save())

	raw := read(t, path)
	require.Contains(t, raw, `"version": 1`, "the file backend keeps writing the v1 document")
	require.Contains(t, raw, "s3cr3t")
	require.NotContains(t, raw, `"backend"`)
}

func TestKeyringStoreUnknownBackend(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "mcp", "secrets.json")
	write(t, path, `{"version": 2, "backend": "gpg", "secrets": {}}`)

	_, err := secret.Load(path)
	require.ErrorContains(t, err, "unknown backend")
}

func TestKeyringStoreBuffersUntilSave(t *testing.T) {
	t.Parallel()

	fake := newFakeKeyring(map[string]string{"ALPHA": "s3cr3t"})
	path, store := keyringStore(t, fake, "ALPHA")

	store.Set("BETA", "fresh")

	require.Zero(t, fake.sets, "values are buffered in memory until Save")
	require.True(t, store.Changed())

	require.NoError(t, store.Save())
	require.Equal(t, 1, fake.sets)
	require.Equal(t, "fresh", fake.values["BETA"])

	raw := read(t, path)
	require.NotContains(t, raw, "fresh")
	require.NotContains(t, raw, "s3cr3t")
	require.Contains(t, raw, `"backend": "keyring"`)
	require.Contains(t, raw, `"ALPHA": ""`)
	require.Contains(t, raw, `"BETA": ""`)
	require.False(t, store.Changed())

	reloaded, err := secret.Load(path, secret.WithKeyring(fake))
	require.NoError(t, err)

	value, ok := reloaded.Get("BETA")
	require.True(t, ok)
	require.Equal(t, "fresh", value)
}

func TestKeyringStoreDeleteOnSave(t *testing.T) {
	t.Parallel()

	fake := newFakeKeyring(map[string]string{"ALPHA": "s3cr3t", "BETA": "other"})
	path, store := keyringStore(t, fake, "ALPHA", "BETA")

	require.True(t, store.Delete("ALPHA"))
	require.Zero(t, fake.deletes, "deleting is buffered in memory until Save")

	require.NoError(t, store.Save())
	require.Equal(t, 1, fake.deletes)

	_, ok := fake.values["ALPHA"]
	require.False(t, ok)

	raw := read(t, path)
	require.NotContains(t, raw, `"ALPHA"`)
	require.Contains(t, raw, `"BETA"`)
}

func TestKeyringStoreDeleteFailureKeepsIndex(t *testing.T) {
	t.Parallel()

	fake := newFakeKeyring(map[string]string{"ALPHA": "s3cr3t"})
	path, store := keyringStore(t, fake, "ALPHA")

	require.True(t, store.Delete("ALPHA"))

	fake.failDelete = errors.New("delete denied")

	before := read(t, path)

	require.ErrorContains(t, store.Save(), "remove secrets from the keyring")
	require.Equal(t, before, read(t, path), "the index is not rewritten")

	fake.failDelete = nil

	require.NoError(t, store.Save(), "retrying after the keyring heals is idempotent")
	require.NotContains(t, read(t, path), `"ALPHA"`)
}

func TestKeyringStoreSaveFailureKeepsIndex(t *testing.T) {
	t.Parallel()

	fake := newFakeKeyring(map[string]string{"ALPHA": "s3cr3t"})
	path, store := keyringStore(t, fake, "ALPHA")

	store.Set("BETA", "fresh")

	fake.failSet = errors.New("write denied")

	before := read(t, path)

	require.ErrorContains(t, store.Save(), "save secrets to the keyring")
	require.Equal(t, before, read(t, path), "the index is not rewritten")
	require.True(t, store.Changed())

	fake.failSet = nil

	require.NoError(t, store.Save())
	require.Equal(t, "fresh", fake.values["BETA"])
}

func TestKeyringStoreSaveRefusedAfterPrefetchFailure(t *testing.T) {
	t.Parallel()

	fake := newFakeKeyring(nil)
	fake.failGet = errors.New("keyring is locked")

	path, store := keyringStore(t, fake, "ALPHA", "BETA")

	before := read(t, path)

	store.Set("GAMMA", "fresh")

	require.ErrorContains(t, store.Save(), "save secrets to the keyring")
	require.Equal(t, before, read(t, path), "the index is never rewritten from a truncated map")
	require.Contains(t, read(t, path), `"ALPHA"`)
}

func TestKeyringStoreMigratesBothWays(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "mcp", "secrets.json")
	write(t, path, `{"version": 1, "secrets": {"ALPHA": "s3cr3t"}}`)

	fake := newFakeKeyring(nil)

	store, err := secret.Load(path, secret.WithKeyring(fake))
	require.NoError(t, err)

	require.NoError(t, store.SetBackend(secret.BackendKeyring))
	require.NoError(t, store.Probe())
	require.NoError(t, store.Save())

	require.Equal(t, "s3cr3t", fake.values["ALPHA"])

	raw := read(t, path)
	require.Contains(t, raw, `"backend": "keyring"`)
	require.NotContains(t, raw, "s3cr3t", "the keyring index holds names only")

	require.NoError(t, store.SetBackend(secret.BackendFile))
	require.NoError(t, store.Save())

	raw = read(t, path)
	require.Contains(t, raw, "s3cr3t", "the plaintext moves back into the file")
	require.NotContains(t, raw, `"backend"`)

	require.NoError(t, store.SetBackend(secret.BackendFile), "migrating to the current backend is a no-op")
	require.ErrorContains(t, store.SetBackend("gpg"), "unknown secrets backend")
}

func TestKeyringStoreNameForStaysStable(t *testing.T) {
	t.Parallel()

	fake := newFakeKeyring(map[string]string{"TOKEN": "v1"})
	_, store := keyringStore(t, fake, "TOKEN")

	name := store.NameFor("token", "v1")
	require.Equal(t, "TOKEN", name)

	store.Set(name, "v1")
	require.NoError(t, store.Save())

	require.Equal(t, []string{"TOKEN"}, store.Names())
	require.Equal(t, "TOKEN", store.NameFor("token", "v1"), "no duplicate entry appears after a save")

	second := store.NameFor("token", "v2")
	require.Equal(t, "TOKEN_"+secret.Fingerprint("v2"), second)
}

func TestKeyringStoreExtractionRoundTrip(t *testing.T) {
	t.Parallel()

	fake := newFakeKeyring(nil)
	path, store := keyringStore(t, fake)

	source := []byte("token=s3cr3tvalue123\n")

	extracted, names, changed, err := secret.ExtractText(source, store)
	require.NoError(t, err)
	require.True(t, changed)
	require.Len(t, names, 1)
	require.Contains(t, string(extracted), secret.Ref(names[0]))
	require.Zero(t, fake.sets, "extraction is buffered")

	require.NoError(t, store.Save())
	require.Equal(t, 1, fake.sets)
	require.Equal(t, "s3cr3tvalue123", fake.values[names[0]])

	reloaded, err := secret.Load(path, secret.WithKeyring(fake))
	require.NoError(t, err)

	resolved, missing := secret.ResolveText(extracted, reloaded)
	require.Empty(t, missing)
	require.Equal(t, source, resolved)
}
