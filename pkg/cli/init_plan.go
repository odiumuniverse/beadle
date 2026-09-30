package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// The onboarding screen (W7-UX §5.2): `beadle init` is kept, and its default
// screen becomes a plan the user reads before anything is written. One question
// at the end, and one line at the bottom saying how to undo each piece
// separately — because a single "how do I turn this off" that only takes the
// whole thing down is not an answer.

// planHost is one agent row: what we found, and why.
type planHost struct {
	ID     string
	Name   string
	Found  bool
	Reason string // why it was not found, or how it was found
}

// planKind is one synchronised kind and how many items it holds.
type planKind struct {
	Kind  kind.ID
	Count int
}

// plan is the whole screen, as data, so a test can assert on it without
// matching prose and a future flag can change the wording without touching the
// decisions.
type plan struct {
	// Already is true when the vault exists and nothing about this run would
	// change it. The screen then reads as a summary and asks nothing.
	Already bool
	// Exists settles the wording: a vault that is already there must never be
	// announced as one about to be created, whatever else the run has found.
	// Already above is the narrower question - nothing to do at all - and the
	// two are not the same test.
	Exists bool

	Vault  string
	Hosts  []planHost
	Kinds  []planKind
	Plugin string // what happens to plugins, through verger

	// Untouched is the honest list of what this run will not write. A plan
	// that only says what it will do leaves the user guessing about the rest.
	Untouched []string

	// Gone lists agents the vault has enabled that are no longer found. A
	// user who uninstalled an agent has sync pointing at a host that is not
	// there, and saying so is the difference between a warning they can act on
	// and a mystery.
	Gone []string

	// New lists agents found on this machine that the vault does not have
	// enabled yet. On a second run this is the whole point of the screen: an
	// agent installed since the last run is announced by name, never picked up
	// silently and never missed.
	New []string

	// Background describes the watcher, which is the one thing that keeps
	// running after the command exits.
	Background string
}

// buildPlan gathers the plan without writing anything. Every fact it reports is
// read, never assumed.
func buildPlan(vaultRoot, home string, agents []*agent.Agent, counts map[kind.ID]int) plan {
	p := plan{Vault: vaultRoot}

	for _, a := range agents {
		// The typed reason, so "the CLI is not installed" and "installed but
		// no config" are different lines with different fixes.
		d := agent.DetectReasonOf(a, home, "")
		p.Hosts = append(p.Hosts, planHost{ID: a.ID, Name: a.Name, Found: d.Found, Reason: d.Line()})
	}

	// Counts in a fixed order, so two runs on the same machine print the same
	// screen and a diff of two runs means something changed.
	for _, k := range []kind.ID{
		kind.Rules, kind.Skills, kind.Permissions, kind.Memory,
		kind.Projects, kind.Subagents, kind.Commands,
	} {
		if n := counts[k]; n > 0 {
			p.Kinds = append(p.Kinds, planKind{Kind: k, Count: n})
		}
	}

	p.Plugin = "plugins are managed by verger; beadle asks it, and installs none by itself"
	p.Untouched = []string{
		"files in an agent's own directory are only read",
		"anything git tracks is left byte-identical",
		"a secret never lands in a file a repository tracks",
		"no file outside the vault and the agents listed above is written",
	}
	p.Background = "a background watcher keeps the agents in step; `beadle sync` does the same once"

	return p
}

// alreadyInitialised reports whether the vault exists and this run would change
// nothing, which is what makes a second `beadle init` a summary rather than a
// question. A newly found agent makes it NOT already-initialised: that is the
// one change a re-run exists to pick up, and it must be asked about.
func alreadyInitialised(vaultRoot string, p plan) bool {
	if !existsDir(vaultRoot) {
		return false
	}

	if len(p.New) > 0 {
		return false
	}

	// A vault that exists means this is a second run. The earlier version
	// required *nothing* to be found, so on any machine with even one agent
	// the re-run asked again and wrote again — which is the opposite of
	// idempotent. Deciding "something changed" needs the config, and that is
	// a follow-up; the safe default is a summary that asks nothing.
	return true
}

// render writes the screen. It is separated from buildPlan so the decisions are
// testable and the wording is one place to change.
func (p plan) render(out io.Writer) {
	if p.Already {
		fmt.Fprintln(out, "Already set up, nothing to change.")
		fmt.Fprintln(out)

		p.renderAgents(out)
		p.renderKinds(out)
		renderNew(out, p.New)
		renderGone(out, p.Gone)

		return
	}

	if p.Exists {
		// The vault is there. Saying "Here is what I found. I will create
		// <path>" here told a user with a working vault that beadle was about
		// to build one, and then asked them to confirm it: a second run that
		// read as a first run.
		fmt.Fprintf(out, "vault already exists at %s\n\n", p.Vault)

		p.renderAgents(out)
		p.renderKinds(out)
		renderNew(out, p.New)
		renderGone(out, p.Gone)

		return
	}

	fmt.Fprintf(out, "Here is what I found. I will create %s and nothing else.\n\n", p.Vault)

	p.renderAgents(out)
	p.renderKinds(out)

	fmt.Fprintln(out)
	fmt.Fprintln(out, "  plugins")
	fmt.Fprintf(out, "    %s\n", p.Plugin)

	fmt.Fprintln(out)
	fmt.Fprintln(out, "  what I will not touch")

	for _, line := range p.Untouched {
		fmt.Fprintf(out, "    %s\n", line)
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "  after this\n    %s\n", p.Background)

	renderNew(out, p.New)
	renderGone(out, p.Gone)
}

// renderNew announces an agent that appeared since the last run. It is a
// named line, not a count: a user who installed Claude yesterday needs to see
// "Claude Code" on the screen, not "1 new agent".
func renderNew(out io.Writer, names []string) {
	if len(names) == 0 {
		return
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "  new since the last run")

	for _, name := range names {
		fmt.Fprintf(out, "    %s found — it is not enabled yet\n", name)
	}
}

// renderGone announces an enabled agent that has vanished, with the command
// that stops it. It is not a warning the user can ignore: the vault still
// believes the agent is there.
func renderGone(out io.Writer, names []string) {
	if len(names) == 0 {
		return
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "  enabled but no longer on this machine")

	for _, name := range names {
		fmt.Fprintf(out, "    %s no longer found; still enabled — beadle agents disable %s\n", name, name)
	}
}

// renderAgents prints the found/not-found table.
func (p plan) renderAgents(out io.Writer) {
	if len(p.Hosts) == 0 {
		return
	}

	width := 0
	for _, h := range p.Hosts {
		if len(h.Name) > width {
			width = len(h.Name)
		}
	}

	for _, h := range p.Hosts {
		mark := "not found"
		if h.Found {
			mark = "found    "
		}

		fmt.Fprintf(out, "  %-*s  %s  %s\n", width, h.Name, mark, h.Reason)
	}
}

// renderKinds prints what will be synchronised, with the counts.
func (p plan) renderKinds(out io.Writer) {
	if len(p.Kinds) == 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "  nothing to synchronize yet — the vault is empty, which is fine")

		return
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "  what will be synchronized")

	for _, k := range p.Kinds {
		fmt.Fprintf(out, "    %-12s %d\n", k.Kind, k.Count)
	}
}

// renderHowToStop prints the last line: every piece switched off on its own.
// A user who wants one agent and not the watcher must be able to say so.
func renderHowToStop(out io.Writer) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  to turn things off, one at a time")
	fmt.Fprintln(out, "    beadle agents --disable <agent>   stop syncing one agent")
	fmt.Fprintln(out, "    beadle daemon uninstall           stop the background watcher")
	fmt.Fprintln(out, "    beadle status                    see what is on right now")
	fmt.Fprintln(out, "    beadle eject                     move the vault out of the way")
}

// dryRunNotice is the last line when nothing was written, so a preview cannot
// be mistaken for a run.
func dryRunNotice(out io.Writer) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  nothing was written: this was a preview (--dry-run)")
}

// askCreate renders the one question. The default is yes, because a user who
// ran `beadle init` has already decided they want the vault; the question is
// about the agents it will enable, not about whether to proceed.
func askCreate(out io.Writer, found int) {
	// One noun, used once. The earlier wording said "Set up 1 agent and enable
	// the 1 agent found", which reads like two different counts of the same
	// thing and makes the user check the number twice.
	if found == 1 {
		fmt.Fprint(out, "\n  Create the vault and enable the 1 agent found?  [Y/n] ")

		return
	}

	fmt.Fprintf(out, "\n  Create the vault and enable the %d agents found?  [Y/n] ", found)
}

// countFound is how many hosts the plan found, for the question's number.
func (p plan) countFound() int {
	n := 0

	for _, h := range p.Hosts {
		if h.Found {
			n++
		}
	}

	return n
}

func existsDir(path string) bool {
	// A home-derived path by design, and `os.Stat` opens nothing: it asks
	// whether a directory is there, which is the whole question.
	info, err := os.Stat(path) //nolint:gosec // G703: Stat on a home-derived path reads no file and writes none

	return err == nil && info.IsDir()
}

// answerIsYes reads one answer. Anything that is not a plain "n" is yes,
// because a user pressing enter has said yes and a script piping a blank line
// has said yes; only an explicit no is a no.
func answerIsYes(answer string) bool {
	answer = strings.ToLower(strings.TrimSpace(answer))

	return answer == "" || answer == "y" || answer == "yes"
}
