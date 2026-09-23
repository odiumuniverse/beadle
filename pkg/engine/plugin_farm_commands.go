package engine

import (
	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/command"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// farmCommandSpec presents plugin-sourced commands to the hosts whose command
// directories take files. Claude Code reads plugin commands natively, and the
// Codex prompt surface is pull-only, so both are left alone.
var farmCommandSpec = farmFileSpec{
	Kind:      kind.Commands,
	DirName:   farmCommandsDirName,
	Label:     "command",
	Artifacts: farmCommandsDirName,
	Skip: map[string]bool{
		agent.ClaudeCodeID: true,
		agent.CodexID:      true,
	},
	ValidName: command.ValidName,
}

// farmPluginCommands presents plugin-sourced commands to every file surface.
func (e *Engine) farmPluginCommands(active []*agent.Agent) ([]FarmResult, []string) {
	return e.farmPluginFiles(active, farmCommandSpec)
}
