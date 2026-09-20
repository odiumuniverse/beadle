package secret_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/secret"
)

func TestNormalizeName(t *testing.T) {
	Convey("Given a table of raw keys", t, func() {
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
			Convey("When normalizing "+tc.name, func() {
				Convey("Then the name matches", func() {
					So(secret.NormalizeName(tc.key), ShouldEqual, tc.want)
				})
			})
		}
	})
}

func TestFingerprint(t *testing.T) {
	Convey("Given two values", t, func() {
		fp1 := secret.Fingerprint("value-one")
		fp2 := secret.Fingerprint("value-two")

		Convey("When fingerprints are computed", func() {
			Convey("Then they are stable, distinct and uppercase hex", func() {
				So(fp1, ShouldHaveLength, 8)
				So(fp1, ShouldNotEqual, fp2)
				So(secret.Fingerprint("value-one"), ShouldEqual, fp1)

				for _, r := range fp1 {
					So((r >= '0' && r <= '9') || (r >= 'A' && r <= 'F'), ShouldBeTrue)
				}
			})
		})
	})
}

func TestValidName(t *testing.T) {
	Convey("Given a table of candidate names", t, func() {
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
			Convey("When validating "+tc.name, func() {
				Convey("Then validity matches", func() {
					So(secret.ValidName(tc.name), ShouldEqual, tc.valid)
				})
			})
		}
	})
}

func TestRefRoundTrip(t *testing.T) {
	Convey("Given a secret ref", t, func() {
		ref := secret.Ref("TOKEN")

		Convey("When it is rendered and parsed", func() {
			name, ok := secret.ParseRef(ref)

			_, bad := secret.ParseRef("not a ref")

			Convey("Then it round-trips and rejects non-refs", func() {
				So(ref, ShouldEqual, "{secret:TOKEN}")
				So(ok, ShouldBeTrue)
				So(name, ShouldEqual, "TOKEN")
				So(secret.IsRef(ref), ShouldBeTrue)

				So(bad, ShouldBeFalse)
				So(secret.IsRef("not a ref"), ShouldBeFalse)
			})
		})
	})
}

func TestEnvRefRoundTrip(t *testing.T) {
	Convey("Given an env ref", t, func() {
		ref := secret.EnvRef("VAR")

		Convey("When it is rendered and parsed", func() {
			name, ok := secret.ParseEnvRef(ref)

			_, secretOK := secret.ParseEnvRef("{secret:VAR}")

			Convey("Then it round-trips and a secret ref is not an env ref", func() {
				So(ref, ShouldEqual, "{env:VAR}")
				So(ok, ShouldBeTrue)
				So(name, ShouldEqual, "VAR")
				So(secretOK, ShouldBeFalse)
			})
		})
	})
}

func TestIsSecret(t *testing.T) {
	Convey("Given a table of key/value pairs", t, func() {
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
			{"audit timestamp", "authorizedAt", "2026-01-01", false},
			{"audit user", "authorizedBy", "user-42", false},
			{"commit trailer", "Co-Authored-By", "Someone <x@example.com>", false},
			{"session id", "originSessionId", "9f1c2d3e-aaaa-bbbb-cccc-ddddeeeeffff", false},
			{"plural credentials", "credentials", "abcdefgh12345678", true},
			{"django secret key", "DJANGO_SECRET_KEY", "abcdefgh12345678", true},
		}

		for _, tc := range cases {
			Convey("When checking "+tc.name, func() {
				Convey("Then the classification matches", func() {
					So(secret.IsSecret(tc.key, tc.value), ShouldEqual, tc.want)
				})
			})
		}
	})
}

func TestStoreSetGetDeleteNames(t *testing.T) {
	Convey("Given a fresh store", t, func() {
		store, err := secret.Load(filepath.Join(t.TempDir(), "secrets.json"))
		So(err, ShouldBeNil)

		Convey("When values are set, read and deleted", func() {
			So(store.Changed(), ShouldBeFalse)
			So(store.Len(), ShouldEqual, 0)

			store.Set("TOKEN", "v1")
			So(store.Changed(), ShouldBeTrue)

			value, ok := store.Get("TOKEN")
			So(ok, ShouldBeTrue)
			So(value, ShouldEqual, "v1")
			So(store.Has("TOKEN"), ShouldBeTrue)

			_, ok = store.Get("MISSING")
			So(ok, ShouldBeFalse)

			store.Set("OTHER", "v2")

			Convey("Then names are sorted and deletion reports presence", func() {
				So(store.Names(), ShouldResemble, []string{"OTHER", "TOKEN"})
				So(store.Len(), ShouldEqual, 2)

				So(store.Delete("OTHER"), ShouldBeTrue)
				So(store.Delete("OTHER"), ShouldBeFalse)
				So(store.Names(), ShouldResemble, []string{"TOKEN"})
			})
		})
	})
}

func TestStoreSetSameValueIsNoop(t *testing.T) {
	Convey("Given a saved store", t, func() {
		store, err := secret.Load(filepath.Join(t.TempDir(), "secrets.json"))
		So(err, ShouldBeNil)

		store.Set("TOKEN", "v1")
		So(store.Save(), ShouldBeNil)
		So(store.Changed(), ShouldBeFalse)

		Convey("When the same value is set again", func() {
			store.Set("TOKEN", "v1")

			Convey("Then the store stays clean", func() {
				So(store.Changed(), ShouldBeFalse)
			})
		})
	})
}

func TestStoreLoadSaveRoundTrip(t *testing.T) {
	Convey("Given a missing secrets file", t, func() {
		path := filepath.Join(t.TempDir(), "mcp", "secrets.json")

		store, err := secret.Load(path)
		So(err, ShouldBeNil)

		Convey("When saving an unchanged store", func() {
			So(store.Save(), ShouldBeNil)

			Convey("Then no file is created until a value is set", func() {
				_, statErr := os.Stat(path)
				So(statErr, ShouldNotBeNil)

				store.Set("TOKEN", "s3cr3t")
				So(store.Save(), ShouldBeNil)

				info, statErr := os.Stat(path)
				So(statErr, ShouldBeNil)
				So(info.Mode().Perm(), ShouldEqual, os.FileMode(0o600))

				reloaded, err := secret.Load(path)
				So(err, ShouldBeNil)

				value, ok := reloaded.Get("TOKEN")

				Convey("Then the value round-trips", func() {
					So(ok, ShouldBeTrue)
					So(value, ShouldEqual, "s3cr3t")
				})
			})
		})
	})
}

func TestStoreLoadMissingFileIsEmpty(t *testing.T) {
	Convey("Given a missing secrets file", t, func() {
		Convey("When it is loaded", func() {
			store, err := secret.Load(filepath.Join(t.TempDir(), "does-not-exist.json"))

			Convey("Then the store is empty", func() {
				So(err, ShouldBeNil)
				So(store.Len(), ShouldEqual, 0)
				So(store.Names(), ShouldBeEmpty)
			})
		})
	})
}

func TestStoreNameFor(t *testing.T) {
	Convey("Given a store with a named value", t, func() {
		store, err := secret.Load(filepath.Join(t.TempDir(), "secrets.json"))
		So(err, ShouldBeNil)

		name := store.NameFor("token", "v1")
		So(name, ShouldEqual, "TOKEN")
		store.Set(name, "v1")

		Convey("When the same key/value and a colliding key are named", func() {
			So(store.NameFor("TOKEN", "v1"), ShouldEqual, "TOKEN")

			name2 := store.NameFor("token", "v2")

			Convey("Then the collision gets a fingerprint suffix and is stable", func() {
				So(name2, ShouldNotEqual, "TOKEN")
				So(name2, ShouldEqual, "TOKEN_"+secret.Fingerprint("v2"))
				So(store.NameFor("token", "v2"), ShouldEqual, name2)
			})
		})

		Convey("When another key holds the same value under a known name", func() {
			Convey("Then the known name is reused", func() {
				So(store.NameFor("GITHUB_TOKEN", "v1"), ShouldEqual, "TOKEN")
			})
		})
	})
}
