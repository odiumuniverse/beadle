package cli

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// readSyncDocument runs the JSON sync and decodes it, so a test can read the
// statuses instead of grepping a rendered object.
func readSyncDocument(t *testing.T, args ...string) syncDocument {
	t.Helper()

	stdout, _, err := runCLISplit(t, append(args, "--json")...)
	if err != nil {
		t.Fatalf("run %v: %v", args, err)
	}

	var doc syncDocument
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("decode the sync document: %v", err)
	}

	return doc
}

// The word on a kind's line is a claim about the user's files. "delivered"
// means something was written; a run that wrote nothing must not say it, or the
// reader stops believing the one word that does mean "your agent is up to date".
func TestSyncSaysWhatItWroteAndWhatItDidNot(t *testing.T) {
	Convey("Given an initialized vault with no host agent on it", t, func() {
		home := t.TempDir()
		empty := t.TempDir()

		t.Setenv("HOME", home)
		isolateTestRoots(t)

		t.Setenv("PATH", empty)

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		// The scenario is "no host agent on this vault", so it says so rather
		// than trusting whatever the machine running the suite happens to have
		// installed: an enabled agent is a fact of the vault, and this test is
		// about the other case.
		disableEveryAgent(t, home)

		Convey("When the vault syncs", func() {
			first, err := runCLI(t, "sync")
			So(err, ShouldBeNil)

			Convey("Then no kind claims a delivery it did not make", func() {
				for _, line := range statusLines(first) {
					So(line, ShouldNotContainSubstring, wordDelivered)
				}
			})

			Convey("Then every kind with nothing to say says why it said nothing", func() {
				lines := statusLines(first)
				So(lines, ShouldNotBeEmpty)

				for _, line := range lines {
					So(line, ShouldContainSubstring, wordSkipped)
					So(line, ShouldContainSubstring, noAgentReason)
				}
			})

			Convey("Then the command that changes it is offered once, not once per kind", func() {
				// The fact is about the vault, not about rules, so the way to
				// change it travels once — as the run's note.
				So(strings.Count(first, "beadle agents enable <agent>"), ShouldEqual, 1)
			})

			Convey("Then the one kind that really did write shows what it wrote", func() {
				// The shared skills surface belongs to beadle and is on without
				// an agent, so a first sync does write there. It gets rows, not
				// a bare word, because a row is what says WHAT was written.
				So(first, ShouldContainSubstring, "→ shared")
			})
		})
	})
}

// The document is what a script reads, so the same facts have to be in it under
// the same words: a status per kind, and the reason when it is not the good one.
func TestTheSyncDocumentSaysTheSameThingAsTheTable(t *testing.T) {
	Convey("Given an initialized vault with no host agent on it", t, func() {
		home := t.TempDir()
		empty := t.TempDir()

		t.Setenv("HOME", home)
		isolateTestRoots(t)

		t.Setenv("PATH", empty)

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		disableEveryAgent(t, home)

		Convey("When the JSON sync document is read", func() {
			doc := readSyncDocument(t, "sync")

			Convey("Then the run-level reason travels with the rows", func() {
				So(doc.NoActiveAgents, ShouldBeTrue)
			})

			Convey("Then a kind that wrote nothing is skipped, with the reason", func() {
				var skipped int

				for _, k := range doc.Kinds {
					if k.Status == wordDelivered {
						continue
					}

					skipped++

					So(k.Status, ShouldEqual, wordSkipped)
					So(k.Detail, ShouldEqual, noAgentReason)
				}

				So(skipped, ShouldBeGreaterThan, 0)
			})
		})
	})
}

// statusLines are the one-word-per-kind lines of the human table, trimmed. A
// bare heading (a block whose own lines carry the words) is not one of them.
func statusLines(out string) []string {
	var lines []string

	for line := range strings.SplitSeq(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if isKindLine(trimmed) && hasUserWord(trimmed) {
			lines = append(lines, trimmed)
		}
	}

	return lines
}

// The three words are one decision, and the decision is the finding: a run that
// wrote nothing must not say it wrote something, and a run with no host to
// compare against must not say it agreed with one.
func TestKindStatusIsOneDecisionWithThreeAnswers(t *testing.T) {
	Convey("Given what a run did with one kind", t, func() {
		pushed := engine.KindReport{Kind: kind.Rules, Agents: []engine.AgentResult{
			{Agent: "claude", Action: engine.ActionPushed},
		}}

		Convey("Then a write is delivered, with nothing to explain", func() {
			word, detail := kindStatus(pushed, true)
			So(word, ShouldEqual, wordDelivered)
			So(detail, ShouldBeEmpty)
		})

		Convey("Then a no-op on a vault with no host is skipped for that reason", func() {
			word, detail := kindStatus(engine.KindReport{Kind: kind.Rules}, true)
			So(word, ShouldEqual, wordSkipped)
			So(detail, ShouldEqual, noAgentReason)
		})

		Convey("Then a no-op with a host is in sync", func() {
			quiet := engine.KindReport{Kind: kind.Rules, Agents: []engine.AgentResult{
				{Agent: "claude", Action: engine.ActionNoop},
			}}

			word, detail := kindStatus(quiet, false)
			So(word, ShouldEqual, wordInSync)
			So(detail, ShouldEqual, "nothing changed")
		})

		Convey("Then a would-be write in a dry run is still delivered", func() {
			// A dry run writes nothing on purpose, and the word has to say so
			// honestly: it is the plan that was delivered, not a lie by omission
			// dressed as "nothing to do".
			planned := engine.KindReport{Kind: kind.Rules, Agents: []engine.AgentResult{
				{Agent: "claude", Action: engine.ActionWouldPush},
			}}

			word, _ := kindStatus(planned, false)
			So(word, ShouldEqual, wordDelivered)
		})
	})
}

// disableEveryAgent turns every agent off in this vault, so a test about a
// vault with none does not depend on what the machine running the suite has
// installed or left enabled.
func disableEveryAgent(t *testing.T, home string) {
	t.Helper()

	for _, a := range agent.All(home, home) {
		// The shared skills surface is beadle's own directory, not a host: it
		// stays on, and it is why "no host agent" and "nothing anywhere" are
		// different scenarios.
		if a.ID == config.SharedAgentID {
			continue
		}

		if _, err := runCLI(t, "agents", "disable", a.ID); err != nil {
			t.Fatalf("disable %s: %v", a.ID, err)
		}
	}
}
