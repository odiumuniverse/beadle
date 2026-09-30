package cli

import (
	"fmt"
	"io"

	"github.com/odiumuniverse/beadle/pkg/engine"
)

// The words a human sees (§1 of the UX spec). Everything beadle prints for a
// person is one of these, and each is something the reader can act on: what
// was written, what was not and why, what a rule refused, what the user changed
// under us, and what beadle never wrote at all. The words beadle uses internally
// (a cell, a kind, a surface, a pivot, a strategy) never reach this output; they
// stay in the JSON document and in the log lines, where a program reads them.
//
// `in sync` is the sixth, added because the first five have no word for the
// most common run there is: the one where the vault and every host already
// agree. Without it, that run had to borrow `delivered`, and a table that says
// "delivered" for a run that wrote nothing teaches the reader not to believe
// the one word that does mean their files were written.
const (
	wordDelivered = "delivered"
	wordSkipped   = "skipped"
	wordBlocked   = "blocked"
	wordYourEdit  = "your edit"
	wordNotOurs   = "not ours"
	wordFailed    = "failed"
	wordInSync    = "in sync"
)

// noAgentReason is why a kind did nothing when no agent is on the vault. The
// command that changes it travels once, in the run's note, and not on every
// kind's line: the fact is about the vault, not about rules.
const noAgentReason = "no agent is on for this vault"

// kindStatus is the one decision both channels make about a kind, so the human
// table and the JSON document cannot say different things about the same run —
// which is how "delivered" came to cover a run that wrote nothing in one
// channel and not the other.
//
// The order matters. A push is a fact about files whatever the run had no agent
// for: the shared surface alone can write, and that write was delivered. Only
// when nothing was written does "delivered or in sync?" have an answer, and
// with no agent on the vault there is nothing to be in sync WITH — no host was
// ever read — so the run skips for that one reason.
func kindStatus(kr engine.KindReport, noActiveAgents bool) (string, string) {
	for _, result := range kr.Agents {
		if result.Action == engine.ActionPushed || result.Action == engine.ActionWouldPush {
			return wordDelivered, ""
		}
	}

	if noActiveAgents {
		return wordSkipped, noAgentReason
	}

	return wordInSync, "nothing changed"
}

// reasons collects the footnotes a table refers to by number, in the order the
// lines first needed them, so the reader scans the table and then reads the
// reasons that apply to it. Every non-`delivered` line ends with its number: the
// reason is never an internal type name, and never the whole line.
type reasons struct {
	lines []string
	seen  map[string]int
}

func newReasons() *reasons { return &reasons{seen: map[string]int{}} }

// add records one reason and returns the number the line carries. A reason is
// facts first, the runnable command last, in backticks.
func (r *reasons) add(text string) int {
	if n, ok := r.seen[text]; ok {
		return n
	}

	r.lines = append(r.lines, text)
	n := len(r.lines)
	r.seen[text] = n

	return n
}

// word renders the state for a line, with its footnote when there is a reason.
func (r *reasons) word(state, reason string) string {
	if reason == "" {
		return state
	}

	return fmt.Sprintf("%s [%d]", state, r.add(reason))
}

// print writes the footnote block, or nothing when every line was `delivered`.
func (r *reasons) print(w io.Writer) {
	if len(r.lines) == 0 {
		return
	}

	fmt.Fprintln(w)

	for i, line := range r.lines {
		fmt.Fprintf(w, "  [%d] %s\n", i+1, line)
	}
}
