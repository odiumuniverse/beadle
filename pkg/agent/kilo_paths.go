package agent

import (
	"path/filepath"

	"github.com/odiumuniverse/verger/pkg/hostpath"

	"github.com/odiumuniverse/beadle/pkg/config"
)

// Kilo path layout (Kilo CLI 1.0, an OpenCode fork). Every path comes from
// verger's shared resolver (D-C): KILO_CONFIG_DIR, then XDG_CONFIG_HOME, then
// <home>/.config/kilo, with the host's own singular/plural read pairs. The
// directory names below are the host's own and stay here only as documentation
// of what the resolver returns.
const (
	kiloSkillsDirName   = "skills"
	kiloSkillDirName    = "skill"
	kiloAgentsDirName   = "agents"
	kiloAgentDirName    = "agent"
	kiloCommandsDirName = "commands"
	kiloCommandDirName  = "command"
)

// KiloConfigDir is the global config directory the Kilo host code resolves.
// KILO_CONFIG_DIR wins, then XDG_CONFIG_HOME, then <home>/.config/kilo.
func KiloConfigDir(home string) string {
	return roots(hostpath.Kilo, home).ConfigRoot
}

// KiloConfigRoots lists every config root Kilo still reads, the write target
// first. With KILO_CONFIG_DIR set the host reads that root AND the effective
// XDG root, which a first-non-empty-wins chain cannot express.
func KiloConfigRoots(home string) []string {
	resolved := surfaces(hostpath.Kilo, home)
	if len(resolved.ConfigReads) > 0 {
		return resolved.ConfigReads
	}

	return []string{roots(hostpath.Kilo, home).ConfigRoot}
}

// KiloSkillsDir is the canonical Kilo skills write target: the plural
// directory inside the config directory the host code reads.
func KiloSkillsDir(home string) string {
	return surfaces(hostpath.Kilo, home).Skills
}

// KiloSkillReadDirs lists every global directory Kilo can read skills from,
// with the canonical write target first. The host code reads
// {configDir}/{skill,skills}; ~/.kilo/{skills,skill} is the path the Kilo
// docs name and older copies may live there.
func KiloSkillReadDirs(home string) []string {
	return surfaces(hostpath.Kilo, home).SkillsReads
}

// KiloAgentDirs lists the global directories Kilo reads agent markdown files
// from: the host code globs {agent,agents}/**/*.md. The agents kind itself is
// a future task (A-28); the paths are pinned here so the adapter and its
// tests share one definition.
func KiloAgentDirs(home string) []string {
	return surfaces(hostpath.Kilo, home).AgentsReads
}

// KiloCommandDirs lists the global directories Kilo reads command markdown
// files from: the host code globs {command,commands}/**/*.md (A-29).
func KiloCommandDirs(home string) []string {
	return surfaces(hostpath.Kilo, home).CommandsReads
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
	resolved := surfaces(hostpath.Kilo, home)
	hub := filepath.Join(sharedAgentsDir(hostpath.Kilo, home), "skills")
	readOrder := make([]string, 0, len(resolved.SkillsReads)+1)
	readOrder = append(readOrder, resolved.SkillsReads...)
	readOrder = append(readOrder, hub)

	return &skillsSurface{
		dir:         readOrder[0],
		ignoreUnder: resolved.IgnoreRoots,
		alsoReads:   readOrder[1:],
		shadowing:   true,
		readOrder:   readOrder,
		traits: Traits{
			DefaultMode: config.ModePull,
			Note:        "Kilo shows one copy per skill name (host code) and reads both ~/.config/kilo/skills (canonical, host code) and ~/.kilo/skills (docs path) plus ~/.agents/skills; beadle writes the canonical one",
		},
	}
}
