package secret_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/secret"
)

const (
	ghpToken   = "ghp_abcdefghijklmnopqrstuvwxyz012345"
	slackToken = "xoxb-123456789012-abcdefghijklmnop"
	awsKey     = "AKIAIOSFODNN7EXAMPLE"            //nolint:gosec // G101: synthetic token for scanner tests
	googleTok  = "ya29.a0AfH6SMBx1234567890abcdef" //nolint:gosec // G101: synthetic token for scanner tests
	skToken    = "sk-proj-abcdefghijklmnopqrstuvwxyz"
	glpatToken = "glpat-abcdefghijklmnop1234"
	genericKey = "abcdefgh12345678"
)

func valuesOf(hits []secret.TextHit) []string {
	if len(hits) == 0 {
		return nil
	}

	out := make([]string, 0, len(hits))

	for _, hit := range hits {
		out = append(out, hit.Value)
	}

	return out
}

func TestScanTextValueHints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want []string
	}{
		{name: "bearer", text: "Authorization: Bearer " + ghpToken, want: []string{ghpToken}},
		{name: "token prefix", text: "token " + genericKey, want: []string{genericKey}},
		{name: "basic", text: "basic dXNlcjpwYXNzd29yZA==", want: []string{"dXNlcjpwYXNzd29yZA=="}},
		{name: "github", text: "use " + ghpToken + " now", want: []string{ghpToken}},
		{name: "github oauth", text: "gho_abcdefghijklmnopqrstuvwxyz012345", want: []string{"gho_abcdefghijklmnopqrstuvwxyz012345"}},
		{name: "github pat", text: "github_pat_11ABCDEFG0123456789_abcdefgh", want: []string{"github_pat_11ABCDEFG0123456789_abcdefgh"}},
		{name: "gitlab", text: glpatToken, want: []string{glpatToken}},
		{name: "slack bot", text: slackToken, want: []string{slackToken}},
		{name: "slack user", text: "xoxp-123456789012-abcdefghijklmnop", want: []string{"xoxp-123456789012-abcdefghijklmnop"}},
		{name: "aws", text: awsKey, want: []string{awsKey}},
		{name: "google", text: "token " + googleTok, want: []string{googleTok}},
		{name: "openai", text: skToken, want: []string{skToken}},
		{name: "short token ignored", text: "ghp_short"},
		{name: "mid-word prefix ignored", text: "task-" + strings.Repeat("a", 20)},
		{name: "bare hex ignored", text: "commit deadbeefcafebabe1234567890abcdef"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, valuesOf(secret.ScanText([]byte(tt.text))))
		})
	}
}

func TestScanTextKeyHints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		text  string
		value string
	}{
		{name: "equals", text: "api_key=" + genericKey, value: genericKey},
		{name: "colon", text: "api_key: " + genericKey, value: genericKey},
		{name: "dashed key", text: "api-key: " + genericKey, value: genericKey},
		{name: "uppercase", text: "PASSWORD: " + genericKey, value: genericKey},
		{name: "export", text: "export GITHUB_TOKEN=" + genericKey, value: genericKey},
		{name: "quoted", text: `secret = "` + genericKey + `"`, value: genericKey},
		{name: "query string", text: "curl 'https://x.test/?access_token=" + genericKey + "'", value: genericKey},
		{name: "comment", text: "# api_key: " + genericKey, value: genericKey},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hits := secret.ScanText([]byte(tt.text))
			require.Len(t, hits, 1)
			require.Equal(t, tt.value, hits[0].Value)
			require.NotEmpty(t, hits[0].Name)
		})
	}
}

func TestScanTextNegatives(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
	}{
		{name: "secret ref", text: "api_key: {secret:API_KEY}"},
		{name: "env ref", text: "api_key: {env:API_KEY}"},
		{name: "embedded env ref", text: "token: before {env:TOKEN} after"},
		{name: "redacted", text: "api_key: [redacted]"},
		{name: "shell variable", text: "token: ${GITHUB_TOKEN}"},
		{name: "short value", text: "api_key: short"},
		{name: "pure number", text: "api_key: 123456789"},
		{name: "url host", text: "token: api.example.com"},
		{name: "url", text: "token: https://example.com/path"},
		{name: "unterminated ref", text: "api_key: {secret:API_KEY"},
		{name: "plain text", text: "just a sentence about tokens and keys"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Empty(t, secret.ScanText([]byte(tt.text)), "text: %q", tt.text)
		})
	}
}

func TestScanTextPEM(t *testing.T) {
	t.Parallel()

	pem := "-----BEGIN OPENSSH PRIVATE KEY-----\n" + strings.Repeat("b3BlbnNzaC1rZXkK\n", 3) + "-----END OPENSSH PRIVATE KEY-----"

	hits := secret.ScanText([]byte("note:\n" + pem + "\nrest"))

	require.Len(t, hits, 1)
	require.Equal(t, pem, hits[0].Value)
}

func TestScanTextUnicodeOffsets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want []string
	}{
		{name: "dotted capital I before bearer", text: "İ Bearer " + ghpToken, want: []string{ghpToken}},
		{name: "sharp s before token", text: "ẞ token " + genericKey, want: []string{genericKey}},
		{name: "unicode body", text: "İstanbul: api_key: " + genericKey, want: []string{genericKey}},
		{name: "non ascii before pem", text: "İ\n-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----", want: []string{"-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, valuesOf(secret.ScanText([]byte(tt.text))))
		})
	}
}

func TestScanTextLaterKeyPair(t *testing.T) {
	t.Parallel()

	text := []byte(`{"token":"","password":"hunter2secret"}`)

	hits := secret.ScanText(text)
	require.Len(t, hits, 1)
	require.Equal(t, "hunter2secret", hits[0].Value)
	require.Equal(t, "PASSWORD", hits[0].Name)
}

func TestExtractText(t *testing.T) {
	t.Parallel()

	store, err := secret.Load(t.TempDir() + "/secrets.json")
	require.NoError(t, err)

	text := []byte("line one\napi_key: " + genericKey + "\nline three\n")

	out, names, changed, err := secret.ExtractText(text, store)
	require.NoError(t, err)
	require.True(t, changed)
	require.Len(t, names, 1)
	require.Contains(t, string(out), "{secret:"+names[0]+"}")
	require.NotContains(t, string(out), genericKey)
	require.True(t, store.Changed())
	require.True(t, store.Has(names[0]))

	again, namesAgain, changed, err := secret.ExtractText(out, store)
	require.NoError(t, err)
	require.False(t, changed)
	require.Empty(t, namesAgain)
	require.Equal(t, string(out), string(again), "repeated extraction is a noop")

	store2, err := secret.Load(t.TempDir() + "/secrets.json")
	require.NoError(t, err)

	_, sameName, _, err := secret.ExtractText([]byte("API_KEY="+genericKey), store2)
	require.NoError(t, err)
	require.Equal(t, names[0], sameName[0], "the same value and key reuse the same name")
}

func TestExtractTextNames(t *testing.T) {
	t.Parallel()

	store, err := secret.Load(t.TempDir() + "/secrets.json")
	require.NoError(t, err)

	_, names, _, err := secret.ExtractText([]byte("GITHUB_TOKEN="+genericKey), store)
	require.NoError(t, err)
	require.Equal(t, []string{"GITHUB_TOKEN"}, names)

	_, names, _, err = secret.ExtractText([]byte("Bearer "+ghpToken), store)
	require.NoError(t, err)
	require.Equal(t, []string{"SECRET"}, names)

	store2, err := secret.Load(t.TempDir() + "/secrets.json")
	require.NoError(t, err)

	_, names, _, err = secret.ExtractText([]byte("api_key: "+genericKey), store2)
	require.NoError(t, err)
	require.Len(t, names, 1)

	_, names, _, err = secret.ExtractText([]byte("api_key: "+strings.Repeat("z", 20)), store2)
	require.NoError(t, err)
	require.Len(t, names, 1)
	require.NotEqual(t, "API_KEY", names[0], "a colliding name gets a fingerprint suffix")
}

func TestResolveText(t *testing.T) {
	t.Parallel()

	store, err := secret.Load(t.TempDir() + "/secrets.json")
	require.NoError(t, err)

	store.Set("API_KEY", genericKey)

	out, missing := secret.ResolveText([]byte("key: {secret:API_KEY}"), store)
	require.Empty(t, missing)
	require.Equal(t, "key: "+genericKey, string(out))

	out, missing = secret.ResolveText([]byte("key: {secret:NOPE}"), store)
	require.Equal(t, []string{"NOPE"}, missing)
	require.Equal(t, "key: [redacted]", string(out))

	env := []byte("key: {env:API_KEY}")
	out, missing = secret.ResolveText(env, store)
	require.Empty(t, missing)
	require.Equal(t, string(env), string(out), "env refs are not expanded")

	bare := []byte("key: {secret:no closing")
	out, missing = secret.ResolveText(bare, store)
	require.Empty(t, missing)
	require.Equal(t, string(bare), string(out))
}

func TestTextRoundTrip(t *testing.T) {
	t.Parallel()

	store, err := secret.Load(t.TempDir() + "/secrets.json")
	require.NoError(t, err)

	original := []byte("token: " + ghpToken + "\nnote without secrets\n")

	extracted, _, _, err := secret.ExtractText(original, store)
	require.NoError(t, err)

	resolved, missing := secret.ResolveText(extracted, store)
	require.Empty(t, missing)
	require.Equal(t, string(original), string(resolved))
}

func TestRefsText(t *testing.T) {
	t.Parallel()

	text := []byte("a {secret:API_KEY} b {secret:B_TOKEN} c {env:C} d {secret:lowercase} e {secret:1BAD} f {secret:} g {secret:UNCLOSED")

	require.Equal(t, []string{"API_KEY", "B_TOKEN", "lowercase"}, secret.RefsText(text))
}
