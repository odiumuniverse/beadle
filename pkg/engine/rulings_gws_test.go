package engine_test

import (
	"os"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/permission"
	"github.com/odiumuniverse/beadle/pkg/rulings"
	"github.com/odiumuniverse/beadle/pkg/state"
)

func trustRuling(t *testing.T, f *fixture, sig rulings.Signature, decision string) {
	t.Helper()

	ledger, err := rulings.Load(f.vault.RulingsPath())
	if err != nil {
		t.Fatalf("load rulings: %v", err)
	}

	ledger.Trust(sig, sig.Scope, decision, time.Now().UTC())

	if err := os.MkdirAll(f.vault.RulingsDir(), 0o700); err != nil {
		t.Fatalf("mkdir rulings: %v", err)
	}

	if err := ledger.Save(f.vault.RulingsPath()); err != nil {
		t.Fatalf("save rulings: %v", err)
	}
}

func loadRulings(t *testing.T, f *fixture) *rulings.Ledger {
	t.Helper()

	ledger, err := rulings.Load(f.vault.RulingsPath())
	if err != nil {
		t.Fatalf("load rulings: %v", err)
	}

	return ledger
}

func engineSignature(t *testing.T, f *fixture, c state.Conflict) rulings.Signature {
	t.Helper()

	base, vault, local, err := f.engine.ConflictValues(c)
	if err != nil {
		t.Fatalf("conflict values: %v", err)
	}

	sig, err := rulings.ComputeSignature(c.Kind, c.Reason, c.Key, c.VaultKey, "host:"+c.Agent, base, vault, local)
	if err != nil {
		t.Fatalf("compute signature: %v", err)
	}

	return sig
}

func rulesDiverge(t *testing.T, f *fixture) {
	t.Helper()

	f.emptyConfigs(t)

	f.sync(t)

	write(t, f.claudeRules(), "# shared changed\n")
	write(t, f.openCodeRules(), "# shared other\n")
}

func TestRulingTrustedExactApplies(t *testing.T) {
	Convey("Given a text rules conflict", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		rulesDiverge(t, f)
		f.sync(t)

		c := f.conflict(t, kind.Rules, agent.OpenCodeID)

		sig := engineSignature(t, f, c)
		So(sig.Kind, ShouldEqual, kind.Rules)
		So(sig.Target, ShouldEqual, "main")
		So(sig.Scope, ShouldEqual, "host:"+agent.OpenCodeID)

		trustRuling(t, f, sig, rulings.RulingTakeVault)

		Convey("When the same shape recurs", func() {
			report := f.sync(t)

			Convey("Then the trusted ruling applies without a conflict", func() {
				So(report.RulingsApplied, ShouldHaveLength, 1)
				So(report.RulingsApplied[0].Ruling, ShouldEqual, rulings.RulingTakeVault)
				So(report.ConflictsOf(kind.Rules), ShouldBeEmpty)
			})

			Convey("Then the ledger records the hit", func() {
				ruling, ok := loadRulings(t, f).Get(sig)

				So(ok, ShouldBeTrue)
				So(ruling.Hits, ShouldEqual, 1)
				So(ruling.LastAppliedAt.IsZero(), ShouldBeFalse)
			})

			Convey("Then the conflict is gone", func() {
				So(f.conflicts(t, kind.Rules, agent.OpenCodeID), ShouldBeEmpty)
			})
		})
	})
}

func TestRulingSuggestedDoesNotApply(t *testing.T) {
	Convey("Given a suggested (not trusted) ruling", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		rulesDiverge(t, f)
		f.sync(t)

		c := f.conflict(t, kind.Rules, agent.OpenCodeID)
		sig := engineSignature(t, f, c)

		ledger, err := rulings.Load(f.vault.RulingsPath())
		So(err, ShouldBeNil)

		ledger.Trust(sig, sig.Scope, rulings.RulingTakeVault, time.Now().UTC())
		ledger.Demote(sig, time.Now().UTC())

		So(os.MkdirAll(f.vault.RulingsDir(), 0o700), ShouldBeNil)
		So(ledger.Save(f.vault.RulingsPath()), ShouldBeNil)

		Convey("When the conflict recurs", func() {
			report := f.sync(t)

			Convey("Then nothing is applied and a suggestion is shown", func() {
				So(report.RulingsApplied, ShouldBeEmpty)
				So(report.RulingSuggestions, ShouldHaveLength, 1)
				So(report.ConflictsOf(kind.Rules), ShouldNotBeEmpty)
			})
		})
	})
}

func TestRulingBlastRadiusNeverAuto(t *testing.T) {
	Convey("Given a trusted permissions ruling", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.config.Permissions = permission.ModeSync

		write(t, f.claudeSettings(), `{"permissions": {"allow": ["Bash(ls:*)"], "ask": [], "deny": []}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}, "permission": {"bash": {"ls*": "allow"}}}`)
		f.sync(t)

		write(t, f.claudeSettings(), `{"permissions": {"allow": ["Bash(git:*)"], "ask": [], "deny": []}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}, "permission": {"bash": {"git*": "deny"}}}`)
		f.sync(t)

		c := f.conflict(t, kind.Permissions, agent.OpenCodeID)
		sig := engineSignature(t, f, c)
		So(sig.Kind, ShouldEqual, kind.Permissions)

		trustRuling(t, f, sig, rulings.RulingTakeAgent)

		Convey("When the conflict recurs", func() {
			report := f.sync(t)

			Convey("Then it is never applied automatically", func() {
				So(report.RulingsApplied, ShouldBeEmpty)
				So(report.ConflictsOf(kind.Permissions), ShouldNotBeEmpty)
			})
		})
	})
}

func TestRulingMCPCommandNeverAuto(t *testing.T) {
	Convey("Given a trusted MCP command ruling", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"gamma": {"type": "stdio", "command": "v1"}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {}}`)
		f.sync(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"gamma": {"type": "stdio", "command": "v2"}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {"gamma": {"type": "local", "command": ["v3"]}}}`)
		f.sync(t)

		c := f.conflict(t, kind.MCP, agent.OpenCodeID)
		sig := engineSignature(t, f, c)
		So(sig.BlastRadius(), ShouldBeTrue)

		trustRuling(t, f, sig, rulings.RulingTakeAgent)

		Convey("When the conflict recurs", func() {
			report := f.sync(t)

			Convey("Then the blast-radius gate blocks auto-apply", func() {
				So(report.RulingsApplied, ShouldBeEmpty)
				So(report.ConflictsOf(kind.MCP), ShouldNotBeEmpty)
			})
		})
	})
}

func TestRulingScopePrecedence(t *testing.T) {
	Convey("Given a global and a host ruling on the same shape", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		rulesDiverge(t, f)
		f.sync(t)

		c := f.conflict(t, kind.Rules, agent.OpenCodeID)
		hostSig := engineSignature(t, f, c)
		globalSig := hostSig
		globalSig.Scope = rulings.ScopeGlobal

		trustRuling(t, f, globalSig, rulings.RulingTakeAgent)
		trustRuling(t, f, hostSig, rulings.RulingTakeVault)

		Convey("When the conflict recurs", func() {
			report := f.sync(t)

			Convey("Then the host ruling wins over global", func() {
				So(report.RulingsApplied, ShouldHaveLength, 1)
				So(report.RulingsApplied[0].Ruling, ShouldEqual, rulings.RulingTakeVault)
			})
		})
	})
}

func TestRulingDemotedByResolve(t *testing.T) {
	Convey("Given a trusted ruling", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		rulesDiverge(t, f)
		f.sync(t)

		c := f.conflict(t, kind.Rules, agent.OpenCodeID)
		sig := engineSignature(t, f, c)

		trustRuling(t, f, sig, rulings.RulingTakeVault)

		Convey("When a human resolves against it", func() {
			report := f.resolve(t, kind.Rules, agent.OpenCodeID, engine.TakeAgent)

			Convey("Then it is demoted to suggested", func() {
				ruling, ok := loadRulings(t, f).Get(sig)
				So(ok, ShouldBeTrue)
				So(ruling.State, ShouldEqual, rulings.StateSuggested)
				So(ruling.Confirmations, ShouldEqual, 0)
				So(report.RulingsDemoted, ShouldHaveLength, 1)
			})
		})

		Convey("When a human confirms it", func() {
			f.resolve(t, kind.Rules, agent.OpenCodeID, engine.TakeVault)

			Convey("Then the confirmation count grows", func() {
				ruling, ok := loadRulings(t, f).Get(sig)
				So(ok, ShouldBeTrue)
				So(ruling.State, ShouldEqual, rulings.StateTrusted)
				So(ruling.Confirmations, ShouldEqual, 1)
			})
		})
	})
}

func TestRulingManualThenAutoE2E(t *testing.T) {
	Convey("Given two identical-shape conflicts in a row", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		Convey("When the first is resolved by hand and trusted", func() {
			rulesDiverge(t, f)
			f.sync(t)

			c := f.conflict(t, kind.Rules, agent.OpenCodeID)
			sig := engineSignature(t, f, c)

			trustRuling(t, f, sig, rulings.RulingTakeVault)
			f.resolve(t, kind.Rules, agent.OpenCodeID, engine.TakeVault)

			Convey("Then the next same-shape conflict is applied automatically", func() {
				write(t, f.claudeRules(), "# shared changed again\n")
				write(t, f.openCodeRules(), "# shared another\n")

				report := f.sync(t)

				So(report.RulingsApplied, ShouldHaveLength, 1)
				So(report.ConflictsOf(kind.Rules), ShouldBeEmpty)
				So(read(t, f.openCodeRules()), ShouldContainSubstring, "shared changed again")
			})
		})
	})
}
