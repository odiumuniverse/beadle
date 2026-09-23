package agent

import (
	"path/filepath"

	"github.com/odiumuniverse/beadle/pkg/config"
)

// Kilo path layout (Kilo CLI 1.0, an OpenCode fork). The host code reads
// skills from {configDir}/{skill,skills}, where configDir is ~/.config/kilo
// (KILO_CONFIG_DIR can move it); the docs name ~/.kilo/skills. Agent and
// command files use the same singular/plural pairs ({agent,agents} and
// {command,commands}) and are pinned here for the future kinds (A-28/A-29).
const (
	kiloSkillsDirName   = "skills"
	kiloSkillDirName    = "skill"
	kiloAgentsDirName   = "agents"
	kiloAgentDirName    = "agent"
	kiloCommandsDirName = "commands"
	kiloCommandDirName  = "command"
)

// KiloConfigDir is the global config directory the Kilo host code resolves
// (~/.config/kilo).
func KiloConfigDir(home string) string {
	return filepath.Join(home, ".config", "kilo")
}

// KiloSkillsDir is the canonical Kilo skills write target: the plural
// directory inside the config directory the host code reads.
func KiloSkillsDir(home string) string {
	return filepath.Join(KiloConfigDir(home), kiloSkillsDirName)
}

// KiloSkillReadDirs lists every global directory Kilo can read skills from,
// with the canonical write target first. The host code reads
// {configDir}/{skill,skills}; ~/.kilo/{skills,skill} is the path the Kilo
// docs name and older copies may live there.
func KiloSkillReadDirs(home string) []string {
	return []string{
		KiloSkillsDir(home),
		filepath.Join(KiloConfigDir(home), kiloSkillDirName),
		filepath.Join(home, ".kilo", kiloSkillsDirName),
		filepath.Join(home, ".kilo", kiloSkillDirName),
	}
}

// KiloAgentDirs lists the global directories Kilo reads agent markdown files
// from: the host code globs {agent,agents}/**/*.md. The agents kind itself is
// a future task (A-28); the paths are pinned here so the adapter and its
// tests share one definition.
func KiloAgentDirs(home string) []string {
	return []string{
		filepath.Join(KiloConfigDir(home), kiloAgentsDirName),
		filepath.Join(KiloConfigDir(home), kiloAgentDirName),
	}
}

// KiloCommandDirs lists the global directories Kilo reads command markdown
// files from: the host code globs {command,commands}/**/*.md (A-29).
func KiloCommandDirs(home string) []string {
	return []string{
		filepath.Join(KiloConfigDir(home), kiloCommandsDirName),
		filepath.Join(KiloConfigDir(home), kiloCommandDirName),
	}
}

// kiloSkillsSurface is the Kilo skills surface. Beadle reads every directory
// the host can read (dual-read: the host-code pair and the docs pair) and
// writes the canonical host-code path, so copies outside it are reported by
// doctor instead of being silently duplicated.
//
// The read order puts the canonical write target first, then its singular
// twin, the docs pair and the ~/.agents/skills compat input. That order is
// beadle's product decision for the global model (the directory beadle writes
// ranks first), not a claim about the host's internal precedence: the host
// keeps one skill per name (SkillV2.list maps by name, the last registered
// source wins) and a discovered ancestor .kilo directory registers after the
// global config directory, so the winner between the two awaits a live probe
// (COVERAGE A-35).
func kiloSkillsSurface(home string) *skillsSurface {
	readOrder := append(KiloSkillReadDirs(home), filepath.Join(home, ".agents", "skills"))

	return &skillsSurface{
		dir:         readOrder[0],
		ignoreUnder: []string{filepath.Join(home, ".claude", "plugins")},
		alsoReads:   readOrder[1:],
		shadowing:   true,
		readOrder:   readOrder,
		traits: Traits{
			DefaultMode: config.ModePull,
			Note:        "Kilo shows one copy per skill name (host code) and reads both ~/.config/kilo/skills (canonical, host code) and ~/.kilo/skills (docs path) plus ~/.agents/skills; beadle writes the canonical one",
		},
	}
}
