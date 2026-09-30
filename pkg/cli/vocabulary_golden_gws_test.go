package cli

import (
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// The human output is a contract, so it is compared as text. A machine's own
// host programs would change which agents read `installed`, so the golden runs
// with an empty PATH: every agent is a known fact and nothing is detected.
func TestHumanOutputIsTheUsersVocabulary(t *testing.T) {
	Convey("Given an initialized vault on a machine with no agents installed", t, func() {
		home := t.TempDir()
		empty := t.TempDir()

		t.Setenv("HOME", home)
		isolateTestRoots(t)

		t.Setenv("PATH", empty)

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		// "No agents installed" is expressed by the empty PATH, but a host is
		// detected by its configuration directory too, and `beadle init` seeds
		// some. The scenario this golden is about is a vault with no host
		// agent, so the vault says so rather than the machine.
		disableEveryAgent(t, home)

		Convey("When the reader-facing commands run", func() {
			agents, err := runCLI(t, "agents")
			So(err, ShouldBeNil)

			kinds, err := runCLI(t, "kinds")
			So(err, ShouldBeNil)

			synced, err := runCLI(t, "sync")
			So(err, ShouldBeNil)

			status, err := runCLI(t, "status")
			So(err, ShouldBeNil)

			outputs := map[string]string{"agents": agents, "kinds": kinds, "sync": synced, "status": status}

			Convey("Then every line is in the five words", func() {
				for _, out := range outputs {
					for line := range strings.SplitSeq(out, "\n") {
						if !isItemLine(line) {
							continue
						}

						if !hasUserWord(line) {
							t.Errorf("line without a user word: %q", line)
						}
					}
				}
			})

			Convey("Then no internal word reaches the reader", func() {
				for _, banned := range []string{
					"rung", "ladder", "tombstone", "hands-off", "pivot",
					"surface", "foreign", "drift", "synth", "loose", "silenced",
					"strategy", "file surface",
				} {
					for _, out := range outputs {
						So(out, ShouldNotContainSubstring, banned)
					}
				}
			})

			Convey("Then the agents table states a reason and points at the command", func() {
				So(agents, ShouldContainSubstring, "agents:")
				So(agents, ShouldContainSubstring, "skipped [1]")
				So(agents, ShouldContainSubstring, "[1] turned off for this vault — run `beadle agents enable claude`")
			})

			Convey("Then the kinds table uses the same words", func() {
				So(kinds, ShouldContainSubstring, "rules")
				So(kinds, ShouldContainSubstring, "delivered")
			})

			Convey("Then a clean sync says what it did, in one word", func() {
				// This machine has no host agent at all, so the honest word is
				// the one that says nothing was written and why — not
				// `delivered`, which means files were written.
				So(synced, ShouldContainSubstring, noAgentReason)
				So(synced, ShouldNotContainSubstring, wordDelivered)
			})
		})
	})
}

// isItemLine is a table row or a report line: something that ends in a state
// word rather than a heading, a note or a footnote.
func isItemLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "note:") {
		return false
	}

	for _, banned := range []string{"agents:", "vault:", "bundles:", "dry run:"} {
		if strings.HasPrefix(trimmed, banned) {
			return false
		}
	}

	// A bare heading (a block whose lines each carry their own word) and a
	// location line (a path, not a state) are not items.
	if isKindLine(trimmed) && !hasUserWord(trimmed) {
		return false
	}

	if strings.Contains(trimmed, string(filepath.Separator)) && !hasUserWord(trimmed) {
		return false
	}

	return strings.HasPrefix(trimmed, "  ") || isKindLine(trimmed)
}

func isKindLine(line string) bool {
	for _, k := range []string{"rules", "mcp", "skills", "permissions", "memory", "projects", "subagents", "commands", "plugins", "digest", "inbox"} {
		if strings.HasPrefix(line, k) {
			return true
		}
	}

	return false
}

func hasUserWord(line string) bool {
	// `in sync` is in the list because it is now a word a reader sees: it is
	// what a kind's line says when the run wrote nothing and the vault and the
	// hosts already agree. It used to be on the banned list above, and the run
	// that writes nothing had to borrow `delivered` instead.
	for _, word := range []string{wordDelivered, wordSkipped, wordBlocked, wordYourEdit, wordNotOurs, wordFailed, wordInSync} {
		if strings.Contains(line, word) {
			return true
		}
	}

	return false
}
