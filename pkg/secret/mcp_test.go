package secret_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/mcp"
	"github.com/odiumuniverse/agents-sync/pkg/secret"
)

func newStore(t *testing.T) *secret.Store {
	t.Helper()

	store, err := secret.Load(filepath.Join(t.TempDir(), "secrets.json"))
	require.NoError(t, err)

	return store
}

func TestExtractEmptyServers(t *testing.T) {
	t.Parallel()

	out, replaced, err := secret.Extract(mcp.Servers{}, newStore(t))
	require.NoError(t, err)
	require.False(t, replaced)
	require.Empty(t, out)
}

func TestExtractReplacesLiteralsAndLeavesTheRest(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	servers := mcp.Servers{
		"context7": {
			Transport: "http",
			URL:       "https://mcp.context7.com",
			Headers:   map[string]string{"CONTEXT7_API_KEY": "ctx7sk-abc123", "Accept": "application/json"},
		},
	}

	out, replaced, err := secret.Extract(servers, store)
	require.NoError(t, err)
	require.True(t, replaced)

	require.Equal(t, "application/json", out["context7"].Headers["Accept"], "non-secret values pass through untouched")
	require.NotEqual(t, "ctx7sk-abc123", out["context7"].Headers["CONTEXT7_API_KEY"])
	require.True(t, secret.IsRef(out["context7"].Headers["CONTEXT7_API_KEY"]))
	require.Equal(t, "https://mcp.context7.com", out["context7"].URL, "non-secret fields are untouched")

	name, ok := secret.ParseRef(out["context7"].Headers["CONTEXT7_API_KEY"])
	require.True(t, ok)

	value, ok := store.Get(name)
	require.True(t, ok)
	require.Equal(t, "ctx7sk-abc123", value)
}

func TestExtractIsIdempotent(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	servers := mcp.Servers{
		"web": {Headers: map[string]string{"Authorization": "Bearer c11a1secret"}},
	}

	once, replaced, err := secret.Extract(servers, store)
	require.NoError(t, err)
	require.True(t, replaced)

	twice, replacedAgain, err := secret.Extract(once, store)
	require.NoError(t, err)
	require.False(t, replacedAgain, "re-extracting an already-ref-ized tree must be a no-op")
	require.Equal(t, once, twice)
}

func TestExtractValueIdentityAcrossServers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		valueB    string
		wantEqual bool
		wantLen   int
	}{
		{"identical values share one stored name", "Bearer shared-token", true, 1},
		{"different values never collapse into one name", "Bearer other-token", false, 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := newStore(t)
			servers := mcp.Servers{
				"a": {Headers: map[string]string{"Authorization": "Bearer shared-token"}},
				"b": {Headers: map[string]string{"Authorization": tc.valueB}},
			}

			out, _, err := secret.Extract(servers, store)
			require.NoError(t, err)

			if tc.wantEqual {
				require.Equal(t, out["a"].Headers["Authorization"], out["b"].Headers["Authorization"])
			} else {
				require.NotEqual(t, out["a"].Headers["Authorization"], out["b"].Headers["Authorization"])
			}

			require.Equal(t, tc.wantLen, store.Len())
		})
	}
}

func TestExtractLeavesEmbeddedEnvRefUntouched(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	servers := mcp.Servers{
		"a": {Headers: map[string]string{"Authorization": "Bearer {env:TOKEN}"}},
	}

	out, replaced, err := secret.Extract(servers, store)
	require.NoError(t, err)
	require.False(t, replaced)
	require.Equal(t, "Bearer {env:TOKEN}", out["a"].Headers["Authorization"])
	require.Zero(t, store.Len(), "an embedded reference must never be stored as a credential")
}

func TestExtractPromotesKnownEnvRef(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	store.Set("KNOWN", "value")

	servers := mcp.Servers{
		"a": {Env: map[string]string{"KNOWN": "{env:KNOWN}", "UNKNOWN": "{env:UNKNOWN}"}},
	}

	out, replaced, err := secret.Extract(servers, store)
	require.NoError(t, err)
	require.True(t, replaced)

	require.Equal(t, "{secret:KNOWN}", out["a"].Env["KNOWN"], "an env ref matching a stored secret is canonicalized")
	require.Equal(t, "{env:UNKNOWN}", out["a"].Env["UNKNOWN"], "an env ref with no stored value is left untouched")
}

func TestResolveEmptyServers(t *testing.T) {
	t.Parallel()

	out, missing, err := secret.Resolve(mcp.Servers{}, newStore(t), secret.ModeLiteral)
	require.NoError(t, err)
	require.Empty(t, missing)
	require.Empty(t, out)
}

func TestResolveLiteralMode(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	store.Set("TOKEN", "the-value")

	servers := mcp.Servers{"a": {Headers: map[string]string{"Authorization": secret.Ref("TOKEN")}}}

	out, missing, err := secret.Resolve(servers, store, secret.ModeLiteral)
	require.NoError(t, err)
	require.Empty(t, missing)
	require.Equal(t, "the-value", out["a"].Headers["Authorization"])
}

func TestResolveEnvMode(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	store.Set("TOKEN", "the-value")

	servers := mcp.Servers{"a": {Headers: map[string]string{"Authorization": secret.Ref("TOKEN")}}}

	out, missing, err := secret.Resolve(servers, store, secret.ModeEnv)
	require.NoError(t, err)
	require.Empty(t, missing)
	require.Equal(t, "{env:TOKEN}", out["a"].Headers["Authorization"], "env mode never renders the literal value")
}

func TestResolveReportsMissingAndLeavesRefIntact(t *testing.T) {
	t.Parallel()

	store := newStore(t)

	servers := mcp.Servers{"a": {Headers: map[string]string{"Authorization": secret.Ref("TOKEN")}}}

	out, missing, err := secret.Resolve(servers, store, secret.ModeLiteral)
	require.NoError(t, err)
	require.Equal(t, []string{"TOKEN"}, missing)
	require.Equal(t, secret.Ref("TOKEN"), out["a"].Headers["Authorization"],
		"the caller must not push this value, so it must not silently become a literal")
}

func TestExtractResolveExtractRoundTripIsStable(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	original := mcp.Servers{"web": {Headers: map[string]string{"Authorization": "Bearer c11a1secret"}}}

	extracted, _, err := secret.Extract(original, store)
	require.NoError(t, err)
	require.NoError(t, store.Save())

	pushed, missing, err := secret.Resolve(extracted, store, secret.ModeLiteral)
	require.NoError(t, err)
	require.Empty(t, missing)
	require.Equal(t, original, pushed)

	reExtracted, replaced, err := secret.Extract(pushed, store)
	require.NoError(t, err)
	require.True(t, replaced)
	require.Equal(t, extracted, reExtracted, "the canon must not drift across a push/pull cycle")
}

func TestRefs(t *testing.T) {
	t.Parallel()

	servers := mcp.Servers{
		"a": {Headers: map[string]string{"Authorization": secret.Ref("TOKEN")}},
		"b": {Env: map[string]string{"KEY": secret.Ref("TOKEN"), "OTHER": secret.Ref("OTHER_NAME")}},
	}

	refs, err := secret.Refs(servers)
	require.NoError(t, err)
	require.Equal(t, []string{"OTHER_NAME", "TOKEN"}, refs)
}

func TestRefsEmptyServers(t *testing.T) {
	t.Parallel()

	refs, err := secret.Refs(mcp.Servers{})
	require.NoError(t, err)
	require.Empty(t, refs)
}
