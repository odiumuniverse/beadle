package agent

import (
	"github.com/odiumuniverse/verger/pkg/hostpath"
)

// InboxPath returns the append-only inbox file of an agent, or "" when the
// agent has no inbox.
//
// The shared resolver answers a path for every host that has an inbox document,
// claude included. Claude is deliberately absent from the list below: the
// decision (docs/tasks/orchestration-2026-09-29.md) is that beadle creates no
// file in the user's home for it, so the resolver's answer is not consumed
// here. The resolver describes what the host reads; the consumer decides.
func InboxPath(home, id string) string {
	// The shared resolver names the inbox of every host that has one. Claude
	// is deliberately absent: the decision (docs/tasks/orchestration-2026-09-29.md)
	// is that beadle creates no file in the user's home for it, so the
	// resolver's answer is not consumed. The resolver describes what the host
	// reads; the consumer decides.
	if id == ClaudeCodeID {
		return ""
	}

	if id == OpenCodeID {
		return surfaces(hostpath.OpenCode, home).Inbox
	}

	// The hub pseudo-agent and any id the resolver does not know have no inbox.
	if !hostpath.Known(id) {
		return ""
	}

	resolved, err := hostpath.Surfaces(id, hostEnv(home))
	if err != nil {
		return ""
	}

	return resolved.Inbox
}
