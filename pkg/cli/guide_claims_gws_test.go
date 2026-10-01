package cli

import (
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// The claims in guide/humans.md and README.md that could not be checked on a
// live machine, pinned here against a test vault instead. Every test names the
// guide line it pins, so a guide edit that changes what is promised shows up as
// a test whose comment no longer describes what the code does.
//
// The fixture is rules conflicts whose three sides differ: base "# v1", vault
// "# vault v2", agents "# agent v2". That is what makes a resolution observable
// — with two sides equal, "take the vault" and "take the agent" would write the
// same bytes and the tests would pass for the wrong reason.

// gwsConflict is one open conflict, kept as the fields a resolution needs.
type gwsConflict struct {
	id, file, base, vault, agent string
}

// gwsDivergentRules is a vault with the rules conflicts open, one per agent
// that carries a copy.
func gwsDivergentRules(t *testing.T) (home string, conflicts []gwsConflict) {
	t.Helper()

	home = gwsHome(t)
	gwsRules(t, home, "# v1\n", "# v1\n")
	gwsInitSync(t)

	// The vault's own canon and the hosts move apart from the same base, which
	// is the shape a three-way merge cannot reconcile on its own.
	gwsWrite(t, filepath.Join(home, ".beadle", "rules", "base.md"), "# vault v2\n")
	// Only OpenCode moves: a host still at the base has nothing to reconcile,
	// so exactly one conflict is open and it is the one the guide's `resolve
	// <id>` names. See NIGHT-pS-15.md for what two agents on one document do.
	gwsWrite(t, filepath.Join(home, ".config", "opencode", "AGENTS.md"), "# agent v2\n")

	if _, err := gwsRun(t, "sync"); err != nil && !gwsBlocked(err) {
		t.Fatal(err)
	}

	views := gwsConflictViews(t, "conflicts", "--json")
	if len(views.Conflicts) == 0 {
		t.Fatal("the fixture opened no conflict, so nothing here would be resolved")
	}

	for _, c := range views.Conflicts {
		conflicts = append(conflicts, gwsConflict{
			id:    c.ID,
			file:  c.File,
			base:  string(c.Base),
			vault: string(c.Vault),
			agent: string(c.AgentHash),
		})
	}

	return home, conflicts
}

// gwsResolveEvery settles each conflict on its own ID. The guide's form is
// `resolve <id> --take …`, one id per run, so the tests use exactly that rather
// than the --all shorthand, which would pin a different command.
func gwsResolveEvery(t *testing.T, conflicts []gwsConflict, args ...string) string {
	t.Helper()

	out := make([]string, 0, len(conflicts))

	for _, c := range conflicts {
		got, err := gwsRun(t, append([]string{"resolve", c.id}, args...)...)
		if err != nil {
			t.Fatalf("resolve %s %v: %v\n%s", c.id, args, err, got)
		}

		out = append(out, got)
	}

	return strings.Join(out, "")
}

// gwsResolvedBytes is what a resolution is supposed to leave behind: both
// hosts carrying the chosen bytes, and no conflict left open.
func gwsResolvedBytes(t *testing.T, home, want string) {
	t.Helper()

	So(gwsRead(t, filepath.Join(home, ".claude", "CLAUDE.md")), ShouldEqual, want)
	So(gwsRead(t, filepath.Join(home, ".config", "opencode", "AGENTS.md")), ShouldEqual, want)
	So(gwsConflictViews(t, "conflicts", "--json").Conflicts, ShouldHaveLength, 0)
}

// guide/humans.md:122 — `beadle resolve <id> --take vault` keeps the vault
// value. "Keeps" is the load-bearing word: the agents end up with the vault's
// bytes, not with theirs.
func TestGuideResolveTakeVaultKeepsTheVaultValue(t *testing.T) {
	Convey("Given conflicts the vault and the agents both moved on", t, func() {
		home, conflicts := gwsDivergentRules(t)

		Convey("When each is settled in favour of the vault", func() {
			out := gwsResolveEvery(t, conflicts, "--take", "vault")

			Convey("Then both hosts carry the vault's bytes and no conflict is left open", func() {
				So(out, ShouldNotContainSubstring, "refused")
				gwsResolvedBytes(t, home, "# vault v2\n")
			})
		})
	})
}

// guide/humans.md:123 — `beadle resolve <id> --take agent` takes the agent
// value. This is the case where the choice is real rather than a formality: the
// vault holds something different from every host.
func TestGuideResolveTakeAgentTakesTheAgentValue(t *testing.T) {
	Convey("Given conflicts the vault and the agents both moved on", t, func() {
		home, conflicts := gwsDivergentRules(t)

		Convey("When each is settled in favour of the agents", func() {
			out := gwsResolveEvery(t, conflicts, "--take", "agent")

			Convey("Then both hosts carry the agent's bytes and no conflict is left open", func() {
				So(out, ShouldNotContainSubstring, "refused")
				gwsResolvedBytes(t, home, "# agent v2\n")
			})
		})
	})
}

// README.md:292 — `resolve [id…] --take vault|agent|file`. The third source has
// no counterpart in the two above: the decision is the content of the conflict
// file the user edited, so the test edits that file instead of naming a side.
// guide/humans.md:122-123 lists only vault and agent — this claim lives in the
// README table and in the hint `resolve` prints for a materialized conflict.
func TestGuideResolveTakeFileUsesTheEditedConflictFile(t *testing.T) {
	Convey("Given conflicts the user can open as files", t, func() {
		home, conflicts := gwsDivergentRules(t)

		Convey("When every file is edited by hand and resolved in favour of it", func() {
			for _, c := range conflicts {
				gwsWrite(t, c.file, "# merged by hand\n")
			}

			out := gwsResolveEvery(t, conflicts, "--take", "file")

			Convey("Then both hosts carry the edited bytes and no conflict is left open", func() {
				So(out, ShouldNotContainSubstring, "refused")
				gwsResolvedBytes(t, home, "# merged by hand\n")
			})
		})
	})
}

// guide/humans.md:126 and :131 — the three `--expect-*` flags are the binding
// the guide promises, and a mismatch "is refused as stale-conflict instead of"
// being applied. One test per flag, because each expectation is a separate
// input and each has to refuse on its own; a guard that only ever checked the
// base would pass both of these by being absent.
//
// Every run binds the two hashes that are still correct and gets one wrong, so
// a refusal can only come from the flag under test.

// The vault moved after the decision was read. The guide's form for these
// flags is the --from one (humans.md:125-126), which is what binds a decision to
// what the user actually read.
func TestGuideResolveRefusesWhenExpectVaultDoesNotMatch(t *testing.T) {
	Convey("Given a conflict bound to the hashes the user read", t, func() {
		home, conflicts := gwsDivergentRules(t)
		c := conflicts[0]
		merged := filepath.Join(home, "merged.md")
		gwsWrite(t, merged, "# merged\n")

		Convey("When the merged result is applied with a stale vault expectation", func() {
			out, err := gwsRun(t, "resolve", c.id, "--from", merged,
				"--expect-base", c.base, "--expect-vault", "not-the-vault-hash", "--expect-agent", c.agent)

			Convey("Then it is refused as a stale conflict and nothing is written", func() {
				So(err, ShouldBeError)
				So(out, ShouldContainSubstring, "stale-conflict")
				So(gwsRead(t, filepath.Join(home, ".config", "opencode", "AGENTS.md")), ShouldEqual, "# agent v2\n")
				So(gwsConflictViews(t, "conflicts", "--json").Conflicts, ShouldNotBeEmpty)
			})
		})
	})
}

// The agent moved after the decision was read.
func TestGuideResolveRefusesWhenExpectAgentDoesNotMatch(t *testing.T) {
	Convey("Given a conflict bound to the hashes the user read", t, func() {
		home, conflicts := gwsDivergentRules(t)
		c := conflicts[0]
		merged := filepath.Join(home, "merged.md")
		gwsWrite(t, merged, "# merged\n")

		Convey("When the merged result is applied with a stale agent expectation", func() {
			out, err := gwsRun(t, "resolve", c.id, "--from", merged,
				"--expect-base", c.base, "--expect-vault", c.vault, "--expect-agent", "not-the-agent-hash")

			Convey("Then it is refused as a stale conflict and nothing is written", func() {
				So(err, ShouldBeError)
				So(out, ShouldContainSubstring, "stale-conflict")
				So(gwsRead(t, filepath.Join(home, ".config", "opencode", "AGENTS.md")), ShouldEqual, "# agent v2\n")
				So(gwsConflictViews(t, "conflicts", "--json").Conflicts, ShouldNotBeEmpty)
			})
		})
	})
}
