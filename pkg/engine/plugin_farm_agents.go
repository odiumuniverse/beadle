package engine

import (
	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/subagent"
)

// farmAgentSpec presents plugin-sourced subagents to the hosts whose agent
// directories take files. Claude Code reads plugin agents natively, so its
// surface is left alone.
var farmAgentSpec = farmFileSpec{
	Kind:      kind.Subagents,
	DirName:   farmAgentsDirName,
	Label:     "agent",
	Artifacts: farmAgentsDirName,
	Skip:      map[string]bool{agent.ClaudeCodeID: true},
	ValidName: subagent.ValidName,
	Rename:    renameFarmAgent,
}

// farmPluginAgents presents plugin-sourced agents to every file surface.
func (e *Engine) farmPluginAgents(active []*agent.Agent) ([]FarmResult, []string) {
	return e.farmPluginFiles(active, farmAgentSpec)
}

// renameFarmAgent rewrites the agent identity to the namespaced name: the host
// compares the definition name with the file name, and the plugin's own name
// would collide with the canon namespace.
func renameFarmAgent(ns string, markdown []byte) ([]byte, error) {
	doc, err := subagent.Parse(markdown)
	if err != nil {
		return nil, err
	}

	doc.Name = ns

	return subagent.Render(doc), nil
}
