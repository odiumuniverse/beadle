package secret_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/secret"
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

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: tests read their own temp files
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(data)
}

func keyringIndex(t *testing.T, path string, names ...string) {
	t.Helper()

	secrets := map[string]string{}

	for _, name := range names {
		secrets[name] = ""
	}

	encoded, err := json.Marshal(map[string]any{"version": 2, "backend": "keyring", "secrets": secrets})
	if err != nil {
		t.Fatalf("marshal index: %v", err)
	}

	write(t, path, string(encoded)+"\n")
}

func keyringStore(t *testing.T, fake secret.Keyring, names ...string) (string, *secret.Store) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "mcp", "secrets.json")
	keyringIndex(t, path, names...)

	store, err := secret.Load(path, secret.WithKeyring(fake))
	if err != nil {
		t.Fatalf("load store: %v", err)
	}

	return path, store
}

func TestKeyringStorePrefetch(t *testing.T) {
	Convey("Given a keyring index with one prefetched secret", t, func() {
		fake := newFakeKeyring(map[string]string{"ALPHA": "s3cr3t"})
		_, store := keyringStore(t, fake, "ALPHA")

		Convey("When the store is used", func() {
			value, ok := store.Get("ALPHA")

			Convey("Then the keyring backend is active and the value is available", func() {
				So(store.Backend(), ShouldEqual, secret.BackendKeyring)
				So(fake.gets, ShouldEqual, 1)
				So(store.KeyringErr(), ShouldBeNil)
				So(store.Probe(), ShouldBeNil)

				So(ok, ShouldBeTrue)
				So(value, ShouldEqual, "s3cr3t")
			})
		})
	})
}

func TestKeyringStorePrefetchFailure(t *testing.T) {
	Convey("Given a keyring that fails to preload", t, func() {
		fake := newFakeKeyring(nil)
		fake.failGet = errors.New("keyring is locked")

		_, store := keyringStore(t, fake, "ALPHA")

		Convey("When the store is used", func() {
			value, ok := store.Get("ALPHA")

			Convey("Then the failure surfaces and the store stays empty", func() {
				So(errors.Is(store.KeyringErr(), fake.failGet), ShouldBeTrue)
				So(errors.Is(store.Probe(), fake.failGet), ShouldBeTrue)

				So(ok, ShouldBeFalse)
				So(value, ShouldBeEmpty)
				So(store.Names(), ShouldBeEmpty)
			})
		})
	})
}

func TestKeyringStoreV1BackCompat(t *testing.T) {
	Convey("Given a v1 secrets document", t, func() {
		path := filepath.Join(t.TempDir(), "mcp", "secrets.json")
		write(t, path, `{"version": 1, "secrets": {"ALPHA": "s3cr3t"}}`)

		fake := newFakeKeyring(nil)

		store, err := secret.Load(path, secret.WithKeyring(fake))
		So(err, ShouldBeNil)

		Convey("When a value is added and saved", func() {
			store.Set("BETA", "fresh")
			So(store.Save(), ShouldBeNil)

			raw := read(t, path)

			Convey("Then the file backend stays active and keeps writing v1", func() {
				So(store.Backend(), ShouldEqual, secret.BackendFile)
				So(fake.gets, ShouldEqual, 0)
				So(store.Probe(), ShouldBeNil)

				So(raw, ShouldContainSubstring, `"version": 1`)
				So(raw, ShouldContainSubstring, "s3cr3t")
				So(raw, ShouldNotContainSubstring, `"backend"`)
			})
		})
	})
}

func TestKeyringStoreUnknownBackend(t *testing.T) {
	Convey("Given a secrets document with an unknown backend", t, func() {
		path := filepath.Join(t.TempDir(), "mcp", "secrets.json")
		write(t, path, `{"version": 2, "backend": "gpg", "secrets": {}}`)

		Convey("When it is loaded", func() {
			_, err := secret.Load(path)

			Convey("Then it is rejected", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "unknown backend")
			})
		})
	})
}

func TestKeyringStoreBuffersUntilSave(t *testing.T) {
	Convey("Given a keyring-backed store", t, func() {
		fake := newFakeKeyring(map[string]string{"ALPHA": "s3cr3t"})
		path, store := keyringStore(t, fake, "ALPHA")

		store.Set("BETA", "fresh")

		Convey("When the store is saved", func() {
			So(fake.sets, ShouldEqual, 0)
			So(store.Changed(), ShouldBeTrue)

			So(store.Save(), ShouldBeNil)

			raw := read(t, path)
			So(raw, ShouldNotContainSubstring, "fresh")
			So(raw, ShouldNotContainSubstring, "s3cr3t")

			reloaded, err := secret.Load(path, secret.WithKeyring(fake))
			So(err, ShouldBeNil)

			value, ok := reloaded.Get("BETA")

			Convey("Then values reach the keyring, the index holds names only and reload sees the value", func() {
				So(fake.sets, ShouldEqual, 1)
				So(fake.values["BETA"], ShouldEqual, "fresh")
				So(raw, ShouldContainSubstring, `"backend": "keyring"`)
				So(raw, ShouldContainSubstring, `"ALPHA": ""`)
				So(raw, ShouldContainSubstring, `"BETA": ""`)
				So(store.Changed(), ShouldBeFalse)

				So(ok, ShouldBeTrue)
				So(value, ShouldEqual, "fresh")
			})
		})
	})
}

func TestKeyringStoreDeleteOnSave(t *testing.T) {
	Convey("Given a keyring-backed store with two secrets", t, func() {
		fake := newFakeKeyring(map[string]string{"ALPHA": "s3cr3t", "BETA": "other"})
		path, store := keyringStore(t, fake, "ALPHA", "BETA")

		So(store.Delete("ALPHA"), ShouldBeTrue)

		Convey("When the store is saved", func() {
			So(fake.deletes, ShouldEqual, 0)

			So(store.Save(), ShouldBeNil)

			_, ok := fake.values["ALPHA"]

			raw := read(t, path)

			Convey("Then the keyring entry is removed and the index drops the name", func() {
				So(fake.deletes, ShouldEqual, 1)
				So(ok, ShouldBeFalse)
				So(raw, ShouldNotContainSubstring, `"ALPHA"`)
				So(raw, ShouldContainSubstring, `"BETA"`)
			})
		})
	})
}

func TestKeyringStoreDeleteFailureKeepsIndex(t *testing.T) {
	Convey("Given a keyring whose delete fails", t, func() {
		fake := newFakeKeyring(map[string]string{"ALPHA": "s3cr3t"})
		path, store := keyringStore(t, fake, "ALPHA")

		So(store.Delete("ALPHA"), ShouldBeTrue)

		fake.failDelete = errors.New("delete denied")

		before := read(t, path)

		Convey("When save fails and then the keyring heals", func() {
			err := store.Save()
			So(err, ShouldBeError)
			So(err.Error(), ShouldContainSubstring, "remove secrets from the keyring")
			So(read(t, path), ShouldEqual, before)

			fake.failDelete = nil

			Convey("Then retrying is idempotent and drops the name", func() {
				So(store.Save(), ShouldBeNil)
				So(read(t, path), ShouldNotContainSubstring, `"ALPHA"`)
			})
		})
	})
}

func TestKeyringStoreSaveFailureKeepsIndex(t *testing.T) {
	Convey("Given a keyring whose write fails", t, func() {
		fake := newFakeKeyring(map[string]string{"ALPHA": "s3cr3t"})
		path, store := keyringStore(t, fake, "ALPHA")

		store.Set("BETA", "fresh")

		fake.failSet = errors.New("write denied")

		before := read(t, path)

		Convey("When save fails and then the keyring heals", func() {
			err := store.Save()
			So(err, ShouldBeError)
			So(err.Error(), ShouldContainSubstring, "save secrets to the keyring")
			So(read(t, path), ShouldEqual, before)
			So(store.Changed(), ShouldBeTrue)

			fake.failSet = nil

			Convey("Then retrying stores the value", func() {
				So(store.Save(), ShouldBeNil)
				So(fake.values["BETA"], ShouldEqual, "fresh")
			})
		})
	})
}

func TestKeyringStoreSaveRefusedAfterPrefetchFailure(t *testing.T) {
	Convey("Given a store whose prefetch failed", t, func() {
		fake := newFakeKeyring(nil)
		fake.failGet = errors.New("keyring is locked")

		path, store := keyringStore(t, fake, "ALPHA", "BETA")

		before := read(t, path)

		store.Set("GAMMA", "fresh")

		Convey("When it is saved", func() {
			err := store.Save()

			Convey("Then the save is refused and the index is never truncated", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "save secrets to the keyring")
				So(read(t, path), ShouldEqual, before)
				So(read(t, path), ShouldContainSubstring, `"ALPHA"`)
			})
		})
	})
}

func TestKeyringStoreMigratesBothWays(t *testing.T) {
	Convey("Given a v1 file-backed secrets document", t, func() {
		path := filepath.Join(t.TempDir(), "mcp", "secrets.json")
		write(t, path, `{"version": 1, "secrets": {"ALPHA": "s3cr3t"}}`)

		fake := newFakeKeyring(nil)

		store, err := secret.Load(path, secret.WithKeyring(fake))
		So(err, ShouldBeNil)

		Convey("When it migrates to keyring and back to file", func() {
			So(store.SetBackend(secret.BackendKeyring), ShouldBeNil)
			So(store.Probe(), ShouldBeNil)
			So(store.Save(), ShouldBeNil)

			So(fake.values["ALPHA"], ShouldEqual, "s3cr3t")

			raw := read(t, path)
			So(raw, ShouldContainSubstring, `"backend": "keyring"`)
			So(raw, ShouldNotContainSubstring, "s3cr3t")

			So(store.SetBackend(secret.BackendFile), ShouldBeNil)
			So(store.Save(), ShouldBeNil)

			raw = read(t, path)

			Convey("Then values move back and unknown backends are rejected", func() {
				So(raw, ShouldContainSubstring, "s3cr3t")
				So(raw, ShouldNotContainSubstring, `"backend"`)

				So(store.SetBackend(secret.BackendFile), ShouldBeNil)

				err := store.SetBackend("gpg")
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "unknown secrets backend")
			})
		})
	})
}

func TestKeyringStoreNameForStaysStable(t *testing.T) {
	Convey("Given a keyring-backed store with a value", t, func() {
		fake := newFakeKeyring(map[string]string{"TOKEN": "v1"})
		_, store := keyringStore(t, fake, "TOKEN")

		name := store.NameFor("token", "v1")
		So(name, ShouldEqual, "TOKEN")

		store.Set(name, "v1")
		So(store.Save(), ShouldBeNil)

		Convey("When names are queried again", func() {
			second := store.NameFor("token", "v2")

			Convey("Then the first name is stable and the collision is fingerprinted", func() {
				So(store.Names(), ShouldResemble, []string{"TOKEN"})
				So(store.NameFor("token", "v1"), ShouldEqual, "TOKEN")
				So(second, ShouldEqual, "TOKEN_"+secret.Fingerprint("v2"))
			})
		})
	})
}

func TestKeyringStoreExtractionRoundTrip(t *testing.T) {
	Convey("Given an empty keyring-backed store", t, func() {
		fake := newFakeKeyring(nil)
		path, store := keyringStore(t, fake)

		source := []byte("token=s3cr3tvalue123\n")

		extracted, names, changed, err := secret.ExtractText(source, store)
		So(err, ShouldBeNil)

		Convey("When the extraction is saved and resolved back", func() {
			So(changed, ShouldBeTrue)
			So(names, ShouldHaveLength, 1)
			So(string(extracted), ShouldContainSubstring, secret.Ref(names[0]))
			So(fake.sets, ShouldEqual, 0)

			So(store.Save(), ShouldBeNil)
			So(fake.sets, ShouldEqual, 1)
			So(fake.values[names[0]], ShouldEqual, "s3cr3tvalue123")

			reloaded, err := secret.Load(path, secret.WithKeyring(fake))
			So(err, ShouldBeNil)

			resolved, missing := secret.ResolveText(extracted, reloaded)

			Convey("Then the source round-trips", func() {
				So(missing, ShouldBeEmpty)
				So(string(resolved), ShouldEqual, string(source))
			})
		})
	})
}
