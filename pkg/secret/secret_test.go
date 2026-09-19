package secret_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/secret"
)

func TestNormalizeName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		key  string
		want string
	}{
		{"lowercase", "api_key", "API_KEY"},
		{"dashes and dots become underscores", "context7.api-key", "CONTEXT7_API_KEY"},
		{"collapses repeated separators", "a--b__c", "A_B_C"},
		{"trims leading and trailing separators", "_TOKEN_", "TOKEN"},
		{"leading digit gets prefixed", "1password", "S_1PASSWORD"},
		{"empty key falls back to placeholder", "", "SECRET"},
		{"only separators falls back to placeholder", "---", "SECRET"},
		{"mixed unicode is stripped to underscores", "töken", "T_KEN"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, secret.NormalizeName(tc.key))
		})
	}
}

func TestFingerprint(t *testing.T) {
	t.Parallel()

	fp1 := secret.Fingerprint("value-one")
	fp2 := secret.Fingerprint("value-two")

	require.Len(t, fp1, 8)
	require.NotEqual(t, fp1, fp2)
	require.Equal(t, fp1, secret.Fingerprint("value-one"), "must be deterministic")

	for _, r := range fp1 {
		require.True(t, (r >= '0' && r <= '9') || (r >= 'A' && r <= 'F'), "expected uppercase hex, got %q", fp1)
	}
}

func TestValidName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		valid bool
	}{
		{"TOKEN", true},
		{"API_KEY_1", true},
		{"_LEADING_UNDERSCORE", true},
		{"", false},
		{"1LEADING_DIGIT", false},
		{"has-dash", false},
		{"has space", false},
		{"has.dot", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.valid, secret.ValidName(tc.name))
		})
	}
}

func TestRefRoundTrip(t *testing.T) {
	t.Parallel()

	ref := secret.Ref("TOKEN")
	require.Equal(t, "{secret:TOKEN}", ref)

	name, ok := secret.ParseRef(ref)
	require.True(t, ok)
	require.Equal(t, "TOKEN", name)
	require.True(t, secret.IsRef(ref))

	_, ok = secret.ParseRef("not a ref")
	require.False(t, ok)
	require.False(t, secret.IsRef("not a ref"))
}

func TestEnvRefRoundTrip(t *testing.T) {
	t.Parallel()

	ref := secret.EnvRef("VAR")
	require.Equal(t, "{env:VAR}", ref)

	name, ok := secret.ParseEnvRef(ref)
	require.True(t, ok)
	require.Equal(t, "VAR", name)

	_, ok = secret.ParseEnvRef("{secret:VAR}")
	require.False(t, ok, "a secret ref must not parse as an env ref")
}

func TestIsSecret(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		key   string
		value string
		want  bool
	}{
		{"api key suffix", "CONTEXT7_API_KEY", "ctx7sk-abc123", true},
		{"bare key field", "key", "sk-abc123", true},
		{"authorization header", "Authorization", "Bearer abc.def.ghi", true},
		{"token key", "access_token", "abc123", true},
		{"password key", "db_password", "hunter2", true},
		{"harmless header", "Accept", "application/json", false},
		{"harmless key", "region", "us-east-1", false},
		{"bearer value without hinted key", "value", "Bearer abc123", true},
		{"github token prefix", "note", "ghp_abcdef123456", true},
		{"empty value is never a secret", "token", "", false},
		{"secret ref is never a secret", "token", "{secret:TOKEN}", false},
		{"env ref is never a secret", "token", "{env:TOKEN}", false},
		{"plain url", "url", "https://example.com/path", false},
		{"embedded env ref under a secret-hinted key", "Authorization", "Bearer {env:TOKEN}", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, secret.IsSecret(tc.key, tc.value))
		})
	}
}

func TestStoreSetGetDeleteNames(t *testing.T) {
	t.Parallel()

	store, err := secret.Load(filepath.Join(t.TempDir(), "secrets.json"))
	require.NoError(t, err)
	require.False(t, store.Changed())
	require.Equal(t, 0, store.Len())

	store.Set("TOKEN", "v1")
	require.True(t, store.Changed())

	value, ok := store.Get("TOKEN")
	require.True(t, ok)
	require.Equal(t, "v1", value)
	require.True(t, store.Has("TOKEN"))

	_, ok = store.Get("MISSING")
	require.False(t, ok)

	store.Set("OTHER", "v2")
	require.Equal(t, []string{"OTHER", "TOKEN"}, store.Names())
	require.Equal(t, 2, store.Len())

	require.True(t, store.Delete("OTHER"))
	require.False(t, store.Delete("OTHER"), "deleting twice reports absence")
	require.Equal(t, []string{"TOKEN"}, store.Names())
}

func TestStoreSetSameValueIsNoop(t *testing.T) {
	t.Parallel()

	store, err := secret.Load(filepath.Join(t.TempDir(), "secrets.json"))
	require.NoError(t, err)

	store.Set("TOKEN", "v1")
	require.NoError(t, store.Save())
	require.False(t, store.Changed())

	store.Set("TOKEN", "v1")
	require.False(t, store.Changed(), "storing an identical value must not mark the store dirty")
}

func TestStoreLoadSaveRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "mcp", "secrets.json")

	store, err := secret.Load(path)
	require.NoError(t, err)
	require.NoError(t, store.Save(), "an unchanged store must not create a file")
	require.NoFileExists(t, path)

	store.Set("TOKEN", "s3cr3t")
	require.NoError(t, store.Save())
	require.FileExists(t, path)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	reloaded, err := secret.Load(path)
	require.NoError(t, err)

	value, ok := reloaded.Get("TOKEN")
	require.True(t, ok)
	require.Equal(t, "s3cr3t", value)
}

func TestStoreLoadMissingFileIsEmpty(t *testing.T) {
	t.Parallel()

	store, err := secret.Load(filepath.Join(t.TempDir(), "does-not-exist.json"))
	require.NoError(t, err)
	require.Equal(t, 0, store.Len())
	require.Empty(t, store.Names())
}

func TestStoreNameFor(t *testing.T) {
	t.Parallel()

	store, err := secret.Load(filepath.Join(t.TempDir(), "secrets.json"))
	require.NoError(t, err)

	name := store.NameFor("token", "v1")
	require.Equal(t, "TOKEN", name)
	store.Set(name, "v1")

	require.Equal(t, "TOKEN", store.NameFor("TOKEN", "v1"))

	name2 := store.NameFor("token", "v2")
	require.NotEqual(t, "TOKEN", name2)
	require.Equal(t, "TOKEN_"+secret.Fingerprint("v2"), name2)

	require.Equal(t, name2, store.NameFor("token", "v2"))
}
