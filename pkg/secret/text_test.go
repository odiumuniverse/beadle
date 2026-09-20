package secret_test

import (
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

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
	Convey("Given a table of texts with value hints", t, func() {
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
			Convey("When scanning "+tt.name, func() {
				Convey("Then the hits match", func() {
					So(valuesOf(secret.ScanText([]byte(tt.text))), ShouldResemble, tt.want)
				})
			})
		}
	})
}

func TestScanTextKeyHints(t *testing.T) {
	Convey("Given a table of key-hint texts", t, func() {
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
			Convey("When scanning "+tt.name, func() {
				hits := secret.ScanText([]byte(tt.text))

				Convey("Then one named hit is found", func() {
					So(hits, ShouldHaveLength, 1)
					So(hits[0].Value, ShouldEqual, tt.value)
					So(hits[0].Name, ShouldNotBeEmpty)
				})
			})
		}
	})
}

func TestScanTextOrdinaryText(t *testing.T) {
	Convey("Given a note with dates, a commit trailer and a session id", t, func() {
		text := []byte(`---
name: db-notes
description: schema notes
---
Audit columns: authorizedBy = "user-42", authorizedAt = "2026-01-01".
Commit trailer: Co-Authored-By: Someone <x@example.com>
Session field: originSessionId = "9f1c2d3e-aaaa-bbbb-cccc-ddddeeeeffff"
`)

		Convey("When it is scanned", func() {
			Convey("Then nothing is treated as a secret", func() {
				So(secret.ScanText(text), ShouldBeEmpty)
			})
		})
	})
}

func TestScanTextNegatives(t *testing.T) {
	Convey("Given a table of texts that must not yield hits", t, func() {
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
			Convey("When scanning "+tt.name, func() {
				Convey("Then nothing is found", func() {
					So(secret.ScanText([]byte(tt.text)), ShouldBeEmpty)
				})
			})
		}
	})
}

func TestScanTextPEM(t *testing.T) {
	Convey("Given a text holding a PEM block", t, func() {
		pem := "-----BEGIN OPENSSH PRIVATE KEY-----\n" + strings.Repeat("b3BlbnNzaC1rZXkK\n", 3) + "-----END OPENSSH PRIVATE KEY-----"

		hits := secret.ScanText([]byte("note:\n" + pem + "\nrest"))

		Convey("When scanned", func() {
			Convey("Then the whole PEM block is one hit", func() {
				So(hits, ShouldHaveLength, 1)
				So(hits[0].Value, ShouldEqual, pem)
			})
		})
	})
}

func TestScanTextUnicodeOffsets(t *testing.T) {
	Convey("Given a table of texts with non-ASCII characters before the secret", t, func() {
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
			Convey("When scanning "+tt.name, func() {
				Convey("Then offsets resolve to the right value", func() {
					So(valuesOf(secret.ScanText([]byte(tt.text))), ShouldResemble, tt.want)
				})
			})
		}
	})
}

func TestScanTextLaterKeyPair(t *testing.T) {
	Convey("Given a JSON body whose first key has an empty value", t, func() {
		text := []byte(`{"token":"","password":"hunter2secret"}`)

		hits := secret.ScanText(text)

		Convey("When scanned", func() {
			Convey("Then the later key pair is found", func() {
				So(hits, ShouldHaveLength, 1)
				So(hits[0].Value, ShouldEqual, "hunter2secret")
				So(hits[0].Name, ShouldEqual, "PASSWORD")
			})
		})
	})
}

func TestExtractText(t *testing.T) {
	Convey("Given a store and a text with a secret", t, func() {
		store, err := secret.Load(t.TempDir() + "/secrets.json")
		So(err, ShouldBeNil)

		text := []byte("line one\napi_key: " + genericKey + "\nline three\n")

		out, names, changed, err := secret.ExtractText(text, store)
		So(err, ShouldBeNil)

		Convey("When it is extracted and then extracted again", func() {
			again, namesAgain, changedAgain, err := secret.ExtractText(out, store)
			So(err, ShouldBeNil)

			Convey("Then the value is replaced once and re-extraction is a noop", func() {
				So(changed, ShouldBeTrue)
				So(names, ShouldHaveLength, 1)
				So(string(out), ShouldContainSubstring, "{secret:"+names[0]+"}")
				So(string(out), ShouldNotContainSubstring, genericKey)
				So(store.Changed(), ShouldBeTrue)
				So(store.Has(names[0]), ShouldBeTrue)

				So(changedAgain, ShouldBeFalse)
				So(namesAgain, ShouldBeEmpty)
				So(string(again), ShouldEqual, string(out))
			})
		})

		Convey("When the same value is extracted with the same key elsewhere", func() {
			store2, err := secret.Load(t.TempDir() + "/secrets.json")
			So(err, ShouldBeNil)

			_, sameName, _, err := secret.ExtractText([]byte("API_KEY="+genericKey), store2)

			Convey("Then the name is reused", func() {
				So(err, ShouldBeNil)
				So(sameName[0], ShouldEqual, names[0])
			})
		})
	})
}

func TestExtractTextNames(t *testing.T) {
	Convey("Given a store", t, func() {
		store, err := secret.Load(t.TempDir() + "/secrets.json")
		So(err, ShouldBeNil)

		Convey("When a keyed value is extracted", func() {
			_, names, _, err := secret.ExtractText([]byte("GITHUB_TOKEN="+genericKey), store)

			Convey("Then the key names the secret", func() {
				So(err, ShouldBeNil)
				So(names, ShouldResemble, []string{"GITHUB_TOKEN"})
			})
		})

		Convey("When a bearer value is extracted", func() {
			_, names, _, err := secret.ExtractText([]byte("Bearer "+ghpToken), store)

			Convey("Then it is named SECRET", func() {
				So(err, ShouldBeNil)
				So(names, ShouldResemble, []string{"SECRET"})
			})
		})

		Convey("When two different values collide on the same key", func() {
			store2, err := secret.Load(t.TempDir() + "/secrets.json")
			So(err, ShouldBeNil)

			_, names, _, err := secret.ExtractText([]byte("api_key: "+genericKey), store2)
			So(err, ShouldBeNil)
			So(names, ShouldHaveLength, 1)

			_, names, _, err = secret.ExtractText([]byte("api_key: "+strings.Repeat("z", 20)), store2)

			Convey("Then the second gets a fingerprint suffix", func() {
				So(err, ShouldBeNil)
				So(names, ShouldHaveLength, 1)
				So(names[0], ShouldNotEqual, "API_KEY")
			})
		})
	})
}

func TestResolveText(t *testing.T) {
	Convey("Given a store with a value", t, func() {
		store, err := secret.Load(t.TempDir() + "/secrets.json")
		So(err, ShouldBeNil)

		store.Set("API_KEY", genericKey)

		Convey("When a secret ref is resolved", func() {
			out, missing := secret.ResolveText([]byte("key: {secret:API_KEY}"), store)

			Convey("Then it expands", func() {
				So(missing, ShouldBeEmpty)
				So(string(out), ShouldEqual, "key: "+genericKey)
			})
		})

		Convey("When an unknown ref is resolved", func() {
			out, missing := secret.ResolveText([]byte("key: {secret:NOPE}"), store)

			Convey("Then it reports missing and redacts", func() {
				So(missing, ShouldResemble, []string{"NOPE"})
				So(string(out), ShouldEqual, "key: [redacted]")
			})
		})

		Convey("When an env ref and a bare ref are resolved", func() {
			env := []byte("key: {env:API_KEY}")
			out, missing := secret.ResolveText(env, store)
			So(missing, ShouldBeEmpty)
			So(string(out), ShouldEqual, string(env))

			bare := []byte("key: {secret:no closing")
			out, missing = secret.ResolveText(bare, store)
			So(missing, ShouldBeEmpty)
			So(string(out), ShouldEqual, string(bare))
		})
	})
}

func TestTextRoundTrip(t *testing.T) {
	Convey("Given a text with a token", t, func() {
		store, err := secret.Load(t.TempDir() + "/secrets.json")
		So(err, ShouldBeNil)

		original := []byte("token: " + ghpToken + "\nnote without secrets\n")

		extracted, _, _, err := secret.ExtractText(original, store)
		So(err, ShouldBeNil)

		Convey("When extracted and resolved back", func() {
			resolved, missing := secret.ResolveText(extracted, store)

			Convey("Then the original text is restored", func() {
				So(missing, ShouldBeEmpty)
				So(string(resolved), ShouldEqual, string(original))
			})
		})
	})
}

func TestRefsText(t *testing.T) {
	Convey("Given a text with several ref-like fragments", t, func() {
		text := []byte("a {secret:API_KEY} b {secret:B_TOKEN} c {env:C} d {secret:lowercase} e {secret:1BAD} f {secret:} g {secret:UNCLOSED")

		Convey("When refs are listed", func() {
			Convey("Then only valid secret names are returned", func() {
				So(secret.RefsText(text), ShouldResemble, []string{"API_KEY", "B_TOKEN", "lowercase"})
			})
		})
	})
}
