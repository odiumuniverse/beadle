package agent

import (
	"path/filepath"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// kiloSubagentSurface presents the vault subagent canon to Kilo Code. Kilo
// is an OpenCode fork: the same strict v2 schema applies, the id is the path
// relative to the agents directory, and the legacy {mode,modes} directories
// hold primary agents.
func kiloSubagentSurface(home string) *subagentSurface {
	configDir := KiloConfigDir(home)

	readDirs := append(KiloAgentDirs(home),
		filepath.Join(configDir, "modes"),
		filepath.Join(configDir, "mode"),
	)

	return &subagentSurface{
		kind:       kind.Subagents,
		label:      subagentLabel,
		model:      subagentModel{},
		readDirs:   readDirs,
		writeDir:   filepath.Join(configDir, kiloAgentsDirName),
		nestedNote: "nested subagent id",
		codec:      &openCodeSubagentCodec{host: "kilo", primaryDirs: []string{modeKey, "modes"}},
		traits: Traits{
			DefaultMode: config.ModeSync,
			Creatable:   true,
			Note: "Kilo Code derives the agent id from the file name; beadle reads the " +
				"~/.config/kilo/{agents,agent} pair plus the legacy {modes,mode} directories " +
				"(mode(s) hold primary agents) and writes the plural agents/ directory — the read " +
				"order is a beadle product decision, not a claim about host precedence",
		},
	}
}
