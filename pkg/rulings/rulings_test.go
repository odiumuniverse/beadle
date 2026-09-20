package rulings_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/rulings"
)

func sig(kindID kind.ID, target, divergence, scope string) rulings.Signature {
	return rulings.Signature{Kind: kindID, Target: target, Divergence: divergence, Scope: scope}
}

func TestComputeSignatureGolden(t *testing.T) {
	Convey("Given a modified MCP command conflict", t, func() {
		base := []byte(`{"command":["npx","old"]}`)
		vault := []byte(`{"command":["npx","vault"]}`)
		local := []byte(`{"command":["npx","agent"]}`)

		Convey("When the signature is computed", func() {
			got, err := rulings.ComputeSignature(kind.MCP, "modified", "alpha.command", "alpha", "host:opencode", base, vault, local)

			Convey("Then it carries the shape, not the content", func() {
				So(err, ShouldBeNil)
				So(got.Kind, ShouldEqual, kind.MCP)
				So(got.Target, ShouldEqual, "alpha.command")
				So(got.Divergence, ShouldEqual, rulings.DivJSONRiskyValue)
				So(got.Scope, ShouldEqual, "host:opencode")
			})

			Convey("Then changing only the values keeps the same signature", func() {
				other, err := rulings.ComputeSignature(kind.MCP, "modified", "alpha.command", "alpha", "host:opencode",
					[]byte(`{"command":["x"]}`), []byte(`{"command":["y"]}`), []byte(`{"command":["z"]}`))

				So(err, ShouldBeNil)
				So(other.Hash(), ShouldEqual, got.Hash())
			})

			Convey("Then a different kind, target, divergence or scope hashes differently", func() {
				kinds := []rulings.Signature{
					sig(kind.MCP, "alpha.command", rulings.DivJSONRiskyValue, "host:claude"),
					sig(kind.MCP, "alpha.url", rulings.DivJSONRiskyValue, "host:opencode"),
					sig(kind.MCP, "alpha.command", rulings.DivJSONValue, "host:opencode"),
					sig(kind.Rules, "alpha.command", rulings.DivJSONRiskyValue, "host:opencode"),
				}

				for _, variant := range kinds {
					So(variant.Hash(), ShouldNotEqual, got.Hash())
				}
			})
		})
	})
}

func TestComputeSignatureReasons(t *testing.T) {
	Convey("Given the conflict reasons", t, func() {
		Convey("When the vault deleted the value", func() {
			got, err := rulings.ComputeSignature(kind.Rules, "deleted", "main", "", "global", []byte("x"), nil, []byte("y"))

			Convey("Then the divergence is deleted", func() {
				So(err, ShouldBeNil)
				So(got.Divergence, ShouldEqual, rulings.DivDeleted)
				So(got.Target, ShouldEqual, "main")
			})
		})

		Convey("When the agent added a whole server", func() {
			got, err := rulings.ComputeSignature(kind.MCP, "added", "beta", "", "global", nil, nil, []byte(`{"url":"https://example.com"}`))

			Convey("Then it is a json key-added", func() {
				So(err, ShouldBeNil)
				So(got.Divergence, ShouldEqual, rulings.DivJSONKeyAdded)
				So(got.Target, ShouldEqual, "beta")
			})
		})
	})
}

func TestComputeSignatureText(t *testing.T) {
	Convey("Given text conflicts", t, func() {
		Convey("When only whitespace differs", func() {
			got, err := rulings.ComputeSignature(kind.Rules, "modified", "main", "", "global",
				[]byte("# a\n"), []byte("# one\r\n\r\n"), []byte("#  one\n"))

			Convey("Then the divergence is formatting-only", func() {
				So(err, ShouldBeNil)
				So(got.Divergence, ShouldEqual, rulings.DivTextFormatting)
			})
		})

		Convey("When words differ", func() {
			got, err := rulings.ComputeSignature(kind.Rules, "modified", "main", "", "global",
				[]byte("# a\n"), []byte("# one\n"), []byte("# two\n"))

			Convey("Then the divergence is content", func() {
				So(err, ShouldBeNil)
				So(got.Divergence, ShouldEqual, rulings.DivTextContent)
			})
		})

		Convey("When a skill conflict is normalized", func() {
			got, err := rulings.ComputeSignature(kind.Skills, "modified", "foo/SKILL.md", "foo/SKILL.md", "global",
				[]byte("x"), []byte("y"), []byte("z"))

			Convey("Then the target is the skill name", func() {
				So(err, ShouldBeNil)
				So(got.Target, ShouldEqual, "skills/foo")
			})
		})
	})
}

func TestLoadSaveDeterminism(t *testing.T) {
	Convey("Given a ledger with two entries", t, func() {
		at := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)

		ledger := rulings.New()
		ledger.Trust(sig(kind.MCP, "alpha.command", rulings.DivJSONValue, "global"), "global", rulings.RulingTakeAgent, at)
		ledger.Trust(sig(kind.Rules, "main", rulings.DivTextContent, "global"), "global", rulings.RulingTakeVault, at)

		path := filepath.Join(t.TempDir(), "rulings.json")

		Convey("When it is saved twice", func() {
			So(ledger.Save(path), ShouldBeNil)
			first, err := os.ReadFile(path) //nolint:gosec // G304: test-owned temp file
			So(err, ShouldBeNil)

			So(ledger.Save(path), ShouldBeNil)
			second, err := os.ReadFile(path) //nolint:gosec // G304: test-owned temp file
			So(err, ShouldBeNil)

			Convey("Then the bytes are stable", func() {
				So(string(second), ShouldEqual, string(first))
			})

			Convey("When it is reloaded", func() {
				loaded, err := rulings.Load(path)
				So(err, ShouldBeNil)

				Convey("Then the entries survive with a deterministic order", func() {
					So(err, ShouldBeNil)
					So(loaded.All(), ShouldHaveLength, 2)
					So(loaded.All()[0].Signature.Kind, ShouldEqual, kind.MCP)
					So(loaded.All()[1].Signature.Kind, ShouldEqual, kind.Rules)
				})
			})
		})
	})
}

func TestLoadMissingIsEmpty(t *testing.T) {
	Convey("Given no ledger file", t, func() {
		ledger, err := rulings.Load(filepath.Join(t.TempDir(), "nope.json"))

		Convey("Then an empty ledger is returned", func() {
			So(err, ShouldBeNil)
			So(len(ledger.Rulings), ShouldEqual, 0)
		})
	})
}

func TestTrustForget(t *testing.T) {
	Convey("Given an observed ruling", t, func() {
		at := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
		ledger := rulings.New()
		s := sig(kind.MCP, "alpha.command", rulings.DivJSONRiskyValue, "global")

		ledger.Observe(s, rulings.RulingTakeAgent, at)

		Convey("When it is trusted", func() {
			got := ledger.Trust(s, "global", rulings.RulingTakeAgent, at)

			Convey("Then it is trusted", func() {
				So(got.State, ShouldEqual, rulings.StateTrusted)
				So(got.Source, ShouldEqual, rulings.SourceHuman)
				So(got.CreatedAt, ShouldEqual, at)
			})

			Convey("When it is forgotten", func() {
				So(ledger.Forget(s.Hash()), ShouldBeTrue)
				_, ok := ledger.Get(s)

				Convey("Then it is gone", func() {
					So(ok, ShouldBeFalse)
					So(ledger.Forget(s.Hash()), ShouldBeFalse)
				})
			})
		})
	})
}

func TestTrustCreatesMissing(t *testing.T) {
	Convey("Given an empty ledger", t, func() {
		ledger := rulings.New()
		s := sig(kind.Rules, "main", rulings.DivTextContent, "global")

		Convey("When a signature is trusted directly", func() {
			got := ledger.Trust(s, "global", rulings.RulingTakeVault, time.Now().UTC())

			Convey("Then a trusted entry exists", func() {
				So(got.State, ShouldEqual, rulings.StateTrusted)
				So(got.Confirmations, ShouldEqual, 0)
			})
		})
	})
}

func TestDemoteLadder(t *testing.T) {
	Convey("Given a trusted ruling", t, func() {
		at := time.Now().UTC()
		ledger := rulings.New()
		s := sig(kind.MCP, "alpha.command", rulings.DivJSONValue, "global")
		ledger.Trust(s, "global", rulings.RulingTakeAgent, at)

		Convey("When it is confirmed with the same ruling", func() {
			got := ledger.Confirm(s, rulings.RulingTakeAgent, at)

			Convey("Then it stays trusted and counts", func() {
				So(got.State, ShouldEqual, rulings.StateTrusted)
				So(got.Confirmations, ShouldEqual, 1)
			})
		})

		Convey("When it is resolved against", func() {
			got := ledger.Confirm(s, rulings.RulingTakeVault, at)

			Convey("Then it is demoted to suggested with a reset counter", func() {
				So(got.State, ShouldEqual, rulings.StateSuggested)
				So(got.Confirmations, ShouldEqual, 0)
			})

			Convey("And demoting again reaches observed", func() {
				again := ledger.Demote(s, at)
				So(again.State, ShouldEqual, rulings.StateObserved)
			})
		})
	})
}

func TestMatchExactAndPrecedence(t *testing.T) {
	Convey("Given a host-scoped and a global ruling on the same shape", t, func() {
		at := time.Now().UTC()
		ledger := rulings.New()

		host := sig(kind.MCP, "alpha.command", rulings.DivJSONValue, "host:opencode")
		global := sig(kind.MCP, "alpha.command", rulings.DivJSONValue, "global")

		ledger.Trust(host, "host:opencode", rulings.RulingTakeAgent, at)
		ledger.Trust(global, "global", rulings.RulingTakeVault, at)

		Convey("When matching a host-scoped conflict", func() {
			match := ledger.Match(host)

			Convey("Then the host ruling wins over global", func() {
				So(match.Found, ShouldBeTrue)
				So(match.Exact, ShouldBeTrue)
				So(match.Ruling.Ruling, ShouldEqual, rulings.RulingTakeAgent)
			})
		})

		Convey("When matching a project-scoped conflict", func() {
			project := sig(kind.MCP, "alpha.command", rulings.DivJSONValue, "project:acme")
			match := ledger.Match(project)

			Convey("Then it falls back to global", func() {
				So(match.Found, ShouldBeTrue)
				So(match.Exact, ShouldBeTrue)
				So(match.Ruling.Ruling, ShouldEqual, rulings.RulingTakeVault)
				So(match.Ruling.Signature.Scope, ShouldEqual, "global")
			})
		})

		Convey("When no scope matches", func() {
			other := sig(kind.Skills, "skills/foo", rulings.DivTextContent, "global")
			match := ledger.Match(other)

			Convey("Then nothing is found", func() {
				So(match.Found, ShouldBeFalse)
			})
		})
	})
}

func TestMatchRelaxed(t *testing.T) {
	Convey("Given a global ruling on one MCP server field", t, func() {
		at := time.Now().UTC()
		ledger := rulings.New()

		exact := sig(kind.MCP, "alpha.command", rulings.DivJSONValue, "global")
		ledger.Trust(exact, "global", rulings.RulingTakeAgent, at)

		Convey("When matching a sibling field on the same server", func() {
			sibling := sig(kind.MCP, "alpha.url", rulings.DivJSONValue, "global")
			match := ledger.Match(sibling)

			Convey("Then a non-exact match is returned for pre-fill only", func() {
				So(match.Found, ShouldBeTrue)
				So(match.Exact, ShouldBeFalse)
				So(match.Ruling.Ruling, ShouldEqual, rulings.RulingTakeAgent)
			})
		})
	})
}

func TestAppliedCounts(t *testing.T) {
	Convey("Given a trusted ruling", t, func() {
		at := time.Now().UTC()
		ledger := rulings.New()
		s := sig(kind.Rules, "main", rulings.DivTextContent, "global")
		ledger.Trust(s, "global", rulings.RulingTakeVault, at)

		Convey("When it is applied", func() {
			got := ledger.Applied(s, at)

			Convey("Then hits and last-applied are recorded", func() {
				So(got.Hits, ShouldEqual, 1)
				So(got.LastAppliedAt, ShouldEqual, at)
			})
		})
	})
}

func TestValidateScope(t *testing.T) {
	Convey("Given scope values", t, func() {
		Convey("Then the accepted forms pass", func() {
			So(rulings.ValidateScope("global"), ShouldBeNil)
			So(rulings.ValidateScope("host:opencode"), ShouldBeNil)
			So(rulings.ValidateScope("project:acme"), ShouldBeNil)
		})

		Convey("Then malformed scopes are rejected", func() {
			So(rulings.ValidateScope("host:"), ShouldBeError)
			So(rulings.ValidateScope("nonsense"), ShouldBeError)
		})
	})
}
