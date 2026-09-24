package engine

import (
	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/command"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/plugin"
)

// farmCommandSpec presents plugin-sourced commands to the hosts whose command
// directories take files. Claude Code reads plugin commands natively, and the
// Codex prompt surface is pull-only, so both are left alone. A Claude plugin
// may still package TOML commands for Gemini (cross-host packages ship both):
// those lift through the same codec as a Gemini extension's commands.
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
	SourceExts: map[string][]string{
		plugin.SourceClaudeCode: {markdownExt, ".toml"},
		plugin.SourceGeminiCLI:  {".toml"},
		plugin.SourceCursor:     {markdownExt, ".mdc", ".markdown", ".txt"},
	},
	Lift: agent.LiftGeminiCommand,
}

// farmPluginCommands presents plugin-sourced commands to every file surface.
func (e *Engine) farmPluginCommands(active []*agent.Agent) ([]FarmResult, []string) {
	return e.farmPluginFiles(active, farmCommandSpec)
}
