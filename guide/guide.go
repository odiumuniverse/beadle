// Package guide carries the beadle guides: a machine-facing document for AI
// agents and a human guide. Both are embedded from this directory, so the
// repository files stay the single source for the binary and for GitHub.
package guide

import (
	_ "embed"
)

//go:embed ai-agents.md
var aiAgents []byte

//go:embed humans.md
var humans []byte

// AI returns the guide for AI agents.
func AI() string {
	return string(aiAgents)
}

// Humans returns the guide for humans.
func Humans() string {
	return string(humans)
}
