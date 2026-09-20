package secret_test

import (
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/mcp"
	"github.com/odiumuniverse/beadle/pkg/secret"
)

func newStore(t *testing.T) *secret.Store {
	t.Helper()

	store, err := secret.Load(filepath.Join(t.TempDir(), "secrets.json"))
	if err != nil {
		t.Fatalf("load store: %v", err)
	}

	return store
}

func TestExtractEmptyServers(t *testing.T) {
	Convey("Given no servers", t, func() {
		Convey("When extracting secrets", func() {
			out, replaced, err := secret.Extract(mcp.Servers{}, newStore(t))

			Convey("Then nothing is replaced", func() {
				So(err, ShouldBeNil)
				So(replaced, ShouldBeFalse)
				So(out, ShouldBeEmpty)
			})
		})
	})
}

func TestExtractReplacesLiteralsAndLeavesTheRest(t *testing.T) {
	Convey("Given a server with a secret header and a harmless one", t, func() {
		store := newStore(t)
		servers := mcp.Servers{
			"context7": {
				Transport: "http",
				URL:       "https://mcp.context7.com",
				Headers:   map[string]string{"CONTEXT7_API_KEY": "ctx7sk-abc123", "Accept": "application/json"},
			},
		}

		out, replaced, err := secret.Extract(servers, store)

		Convey("When extracting secrets", func() {
			name, ok := secret.ParseRef(out["context7"].Headers["CONTEXT7_API_KEY"])

			Convey("Then only the secret becomes a ref and the value is stored", func() {
				So(err, ShouldBeNil)
				So(replaced, ShouldBeTrue)

				So(out["context7"].Headers["Accept"], ShouldEqual, "application/json")
				So(out["context7"].Headers["CONTEXT7_API_KEY"], ShouldNotEqual, "ctx7sk-abc123")
				So(secret.IsRef(out["context7"].Headers["CONTEXT7_API_KEY"]), ShouldBeTrue)
				So(out["context7"].URL, ShouldEqual, "https://mcp.context7.com")

				So(ok, ShouldBeTrue)

				value, ok := store.Get(name)
				So(ok, ShouldBeTrue)
				So(value, ShouldEqual, "ctx7sk-abc123")
			})
		})
	})
}

func TestExtractIsIdempotent(t *testing.T) {
	Convey("Given a server with a bearer header", t, func() {
		store := newStore(t)
		servers := mcp.Servers{
			"web": {Headers: map[string]string{"Authorization": "Bearer c11a1secret"}},
		}

		once, replaced, err := secret.Extract(servers, store)
		So(err, ShouldBeNil)

		Convey("When extracting again", func() {
			twice, replacedAgain, err := secret.Extract(once, store)

			Convey("Then the second pass is a no-op", func() {
				So(err, ShouldBeNil)
				So(replaced, ShouldBeTrue)
				So(replacedAgain, ShouldBeFalse)
				So(twice, ShouldResemble, once)
			})
		})
	})
}

func TestExtractValueIdentityAcrossServers(t *testing.T) {
	Convey("Given a table of two-server value pairs", t, func() {
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
			Convey("When "+tc.name, func() {
				store := newStore(t)
				servers := mcp.Servers{
					"a": {Headers: map[string]string{"Authorization": "Bearer shared-token"}},
					"b": {Headers: map[string]string{"Authorization": tc.valueB}},
				}

				out, _, err := secret.Extract(servers, store)

				Convey("Then refs and the store length match", func() {
					So(err, ShouldBeNil)

					if tc.wantEqual {
						So(out["a"].Headers["Authorization"], ShouldEqual, out["b"].Headers["Authorization"])
					} else {
						So(out["a"].Headers["Authorization"], ShouldNotEqual, out["b"].Headers["Authorization"])
					}

					So(store.Len(), ShouldEqual, tc.wantLen)
				})
			})
		}
	})
}

func TestExtractLeavesEmbeddedEnvRefUntouched(t *testing.T) {
	Convey("Given a header with an embedded env ref", t, func() {
		store := newStore(t)
		servers := mcp.Servers{
			"a": {Headers: map[string]string{"Authorization": "Bearer {env:TOKEN}"}},
		}

		out, replaced, err := secret.Extract(servers, store)

		Convey("When extracting secrets", func() {
			Convey("Then the ref is untouched and nothing is stored", func() {
				So(err, ShouldBeNil)
				So(replaced, ShouldBeFalse)
				So(out["a"].Headers["Authorization"], ShouldEqual, "Bearer {env:TOKEN}")
				So(store.Len(), ShouldEqual, 0)
			})
		})
	})
}

func TestExtractPromotesKnownEnvRef(t *testing.T) {
	Convey("Given a server with a known and an unknown env ref", t, func() {
		store := newStore(t)
		store.Set("KNOWN", "value")

		servers := mcp.Servers{
			"a": {Env: map[string]string{"KNOWN": "{env:KNOWN}", "UNKNOWN": "{env:UNKNOWN}"}},
		}

		out, replaced, err := secret.Extract(servers, store)

		Convey("When extracting secrets", func() {
			Convey("Then only the known ref is canonicalized", func() {
				So(err, ShouldBeNil)
				So(replaced, ShouldBeTrue)
				So(out["a"].Env["KNOWN"], ShouldEqual, "{secret:KNOWN}")
				So(out["a"].Env["UNKNOWN"], ShouldEqual, "{env:UNKNOWN}")
			})
		})
	})
}

func TestResolveEmptyServers(t *testing.T) {
	Convey("Given no servers", t, func() {
		Convey("When resolving secrets", func() {
			out, missing, err := secret.Resolve(mcp.Servers{}, newStore(t), secret.ModeLiteral)

			Convey("Then nothing is resolved", func() {
				So(err, ShouldBeNil)
				So(missing, ShouldBeEmpty)
				So(out, ShouldBeEmpty)
			})
		})
	})
}

//nolint:dupl // the literal and env modes intentionally mirror each other
func TestResolveLiteralMode(t *testing.T) {
	Convey("Given a server holding a secret ref", t, func() {
		store := newStore(t)
		store.Set("TOKEN", "the-value")

		servers := mcp.Servers{"a": {Headers: map[string]string{"Authorization": secret.Ref("TOKEN")}}}

		Convey("When resolving in literal mode", func() {
			out, missing, err := secret.Resolve(servers, store, secret.ModeLiteral)

			Convey("Then the literal value is rendered", func() {
				So(err, ShouldBeNil)
				So(missing, ShouldBeEmpty)
				So(out["a"].Headers["Authorization"], ShouldEqual, "the-value")
			})
		})
	})
}

//nolint:dupl // mirrors the literal-mode case above
func TestResolveEnvMode(t *testing.T) {
	Convey("Given a server holding a secret ref", t, func() {
		store := newStore(t)
		store.Set("TOKEN", "the-value")

		servers := mcp.Servers{"a": {Headers: map[string]string{"Authorization": secret.Ref("TOKEN")}}}

		Convey("When resolving in env mode", func() {
			out, missing, err := secret.Resolve(servers, store, secret.ModeEnv)

			Convey("Then the env ref is rendered, never the literal", func() {
				So(err, ShouldBeNil)
				So(missing, ShouldBeEmpty)
				So(out["a"].Headers["Authorization"], ShouldEqual, "{env:TOKEN}")
			})
		})
	})
}

func TestResolveReportsMissingAndLeavesRefIntact(t *testing.T) {
	Convey("Given a server holding an unknown secret ref", t, func() {
		store := newStore(t)
		servers := mcp.Servers{"a": {Headers: map[string]string{"Authorization": secret.Ref("TOKEN")}}}

		Convey("When resolving in literal mode", func() {
			out, missing, err := secret.Resolve(servers, store, secret.ModeLiteral)

			Convey("Then the ref is reported missing and left intact", func() {
				So(err, ShouldBeNil)
				So(missing, ShouldResemble, []string{"TOKEN"})
				So(out["a"].Headers["Authorization"], ShouldEqual, secret.Ref("TOKEN"))
			})
		})
	})
}

func TestExtractResolveExtractRoundTripIsStable(t *testing.T) {
	Convey("Given a server with a bearer header", t, func() {
		store := newStore(t)
		original := mcp.Servers{"web": {Headers: map[string]string{"Authorization": "Bearer c11a1secret"}}}

		extracted, _, err := secret.Extract(original, store)
		So(err, ShouldBeNil)
		So(store.Save(), ShouldBeNil)

		Convey("When resolved back and re-extracted", func() {
			pushed, missing, err := secret.Resolve(extracted, store, secret.ModeLiteral)
			So(err, ShouldBeNil)

			reExtracted, replaced, err := secret.Extract(pushed, store)
			So(err, ShouldBeNil)

			Convey("Then the canon does not drift across a push/pull cycle", func() {
				So(missing, ShouldBeEmpty)
				So(pushed, ShouldResemble, original)
				So(replaced, ShouldBeTrue)
				So(reExtracted, ShouldResemble, extracted)
			})
		})
	})
}

func TestRefs(t *testing.T) {
	Convey("Given servers with several refs", t, func() {
		servers := mcp.Servers{
			"a": {Headers: map[string]string{"Authorization": secret.Ref("TOKEN")}},
			"b": {Env: map[string]string{"KEY": secret.Ref("TOKEN"), "OTHER": secret.Ref("OTHER_NAME")}},
		}

		Convey("When refs are listed", func() {
			refs, err := secret.Refs(servers)

			Convey("Then they are sorted and deduplicated", func() {
				So(err, ShouldBeNil)
				So(refs, ShouldResemble, []string{"OTHER_NAME", "TOKEN"})
			})
		})
	})
}

func TestRefsEmptyServers(t *testing.T) {
	Convey("Given no servers", t, func() {
		Convey("When refs are listed", func() {
			refs, err := secret.Refs(mcp.Servers{})

			Convey("Then the list is empty", func() {
				So(err, ShouldBeNil)
				So(refs, ShouldBeEmpty)
			})
		})
	})
}
