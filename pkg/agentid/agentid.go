// Package agentid owns the agent id vocabulary beadle and verger share, so a
// user moving between the two tools — or running beadle, which embeds verger
// — never meets two names for one agent.
//
// The package is a leaf and imports nothing: pkg/config renames the ids of a
// stored vault through it, and pkg/agent (which pkg/config is imported by)
// cannot be that dependency. Every caller outside pkg/agentid reaches the
// table through pkg/agent, which re-exports the ids and Canonical.
package agentid

import "maps"

// The canonical agent ids.
const (
	Claude      = "claude"
	Gemini      = "gemini"
	Antigravity = "agy"
	DSH         = "dsh"
	OpenCode    = "opencode"
	Cursor      = "cursor"
	Codex       = "codex"
	Pi          = "pi"
	Kilo        = "kilo"
	Omp         = "omp"
	Shared      = "shared"
)

// aliases maps every agent id beadle wrote before the rename to the canonical
// one. The vault migration renames the keys the table covers and this is the
// only place that says which spelling is historical: an id beadle never wrote
// (a host directory name, a marketplace name) is not an agent id and must not
// be rewritten.
var aliases = map[string]string{
	"claude-code":      Claude,
	"gemini-cli":       Gemini,
	"antigravity-cli":  Antigravity,
	"deepseek-harness": DSH,
}

// Canonical returns the canonical id for value: value itself when it is
// already canonical or is not a known alias, so an unknown id still reaches
// the caller and is reported as unknown.
func Canonical(value string) string {
	if canonical, ok := aliases[value]; ok {
		return canonical
	}

	return value
}

// Aliases returns a copy of the historical-id table.
func Aliases() map[string]string {
	return maps.Clone(aliases)
}
