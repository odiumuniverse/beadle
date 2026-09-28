package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/project"
)

// OmpID identifies the oh-my-pi (omp) adapter. omp is not Pi: its user root
// is ~/.omp (CONFIG_DIR_NAME=.omp) and it excludes ~/.pi from source
// discovery, so the existing Pi adapter does not cover it.
const OmpID = "omp"

const (
	ompDirName         = ".omp"
	ompAgentDirName    = "agent"
	ompProfilesDirName = "profiles"
	ompConfigFile      = "config.yml"
	ompBinary          = "omp"
	ompHomeEnv         = "PI_CONFIG_DIR"
	ompAgentDirEnv     = "PI_CODING_AGENT_DIR"
	ompProfileEnv      = "OMP_PROFILE"
	ompProfileOldEnv   = "PI_PROFILE"
	ompDefaultProfile  = "default"
	// omp reads the project context and project MCP servers from the
	// nearest non-empty .omp directory, not from the checkout root, so the
	// project scope needs its own rel.
	ompProjectRulesRel = ".omp/AGENTS.md"
	ompProjectMCPRel   = ".omp/mcp.json"
)

// OmpHomeNote explains the empty PI_CONFIG_DIR case: omp ignores the empty
// value instead of resolving it against the working directory.
const OmpHomeNote = "PI_CONFIG_DIR is empty; omp ignores it and falls back to ~/.omp (it is not resolved to cwd)"

// OmpHome resolves the omp user root the way omp does: PI_CONFIG_DIR names
// the root directory under $HOME (normally .omp), and an empty value is
// ignored so the default ~/.omp applies. The second result reports whether
// PI_CONFIG_DIR was set to an empty value.
//
// The value is taken as a name under HOME, never as a root of its own: with
// PI_CONFIG_DIR=/abs/path omp reads $HOME/abs/path (live-verified on
// 18.4.1 — a skill placed in the absolute path is invisible, one in
// $HOME/abs/path/agent/skills is discovered).
func OmpHome(home string) (string, bool) {
	value, ok := os.LookupEnv(ompHomeEnv)
	if ok && value != "" {
		return filepath.Join(home, value), false
	}

	return filepath.Join(home, ompDirName), ok
}

// OmpProfile returns the active named profile: OMP_PROFILE wins when it is
// defined (including when it is explicitly empty), PI_PROFILE is the legacy
// fallback, and "default", an empty value or whitespace select the default
// profile.
func OmpProfile() (string, bool) {
	if value, ok := os.LookupEnv(ompProfileEnv); ok {
		return ompProfileName(value)
	}

	return ompProfileName(os.Getenv(ompProfileOldEnv))
}

func ompProfileName(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || value == ompDefaultProfile {
		return "", false
	}

	return value, true
}

// OmpAgentDir resolves the omp agent directory: a named profile relocates it
// to <home>/profiles/<name>/agent and ignores PI_CODING_AGENT_DIR, which
// otherwise overrides the default <home>/agent location.
func OmpAgentDir(home string) string {
	root, _ := OmpHome(home)

	if profile, ok := OmpProfile(); ok {
		return filepath.Join(root, ompProfilesDirName, profile, ompAgentDirName)
	}

	if value := os.Getenv(ompAgentDirEnv); value != "" {
		return value
	}

	return filepath.Join(root, ompAgentDirName)
}

// OmpDetected reports whether omp is present: its agent config.yml exists or
// the omp binary is on PATH. Detection must stay total and cheap — an error
// aborts the whole sync.
func OmpDetected(home string) (bool, error) {
	found, err := anyExists(filepath.Join(OmpAgentDir(home), ompConfigFile))
	if err != nil || found {
		return found, err
	}

	if _, err := exec.LookPath(ompBinary); err == nil {
		return true, nil
	}

	return false, nil
}

// OmpConfigured reports whether omp has been set up for this home: its agent
// config.yml exists. OmpDetected is broader on purpose — the binary alone
// means beadle can deliver into a fresh home — so the doctor and status
// distinguish "installed with a config" from "binary on PATH only".
func OmpConfigured(home string) bool {
	return fsutil.Exists(filepath.Join(OmpAgentDir(home), ompConfigFile))
}

// OmpStateRoot returns the directory that holds the profile-visible omp state
// next to the agent directory (plugins/, marketplaces.json): the profile
// directory when a named profile is active, the user root otherwise. It is
// the one resolver every package must use — a second copy of this rule drifts
// as soon as a profile is active.
func OmpStateRoot(home string) string {
	root, _ := OmpHome(home)

	if profile, ok := OmpProfile(); ok {
		return filepath.Join(root, ompProfilesDirName, profile)
	}

	return root
}

// OmpPluginsDir returns the directory holding omp's plugin state
// (installed_plugins.json, marketplaces.json, node_modules, caches): a named
// profile relocates it next to the profile's agent dir, exactly like
// OmpAgentDir (live-verified on 18.4.1: a registry planted under
// ~/.omp/profiles/work/plugins is the one `OMP_PROFILE=work omp plugin list`
// reads). PI_CODING_AGENT_DIR does not move it — the plugin state is a
// sibling of agent/, not a child.
func OmpPluginsDir(home string) string {
	return filepath.Join(OmpStateRoot(home), "plugins")
}

// OmpProfiles lists the profile directories under <home>/profiles; it feeds
// the doctor only.
func OmpProfiles(home string) []string {
	root, _ := OmpHome(home)

	entries, err := os.ReadDir(filepath.Join(root, ompProfilesDirName))
	if err != nil {
		return nil
	}

	var names []string

	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}

	slices.Sort(names)

	return names
}

// ompMCPFile returns the omp-native MCP config the adapter writes: the
// primary mcp.json in the agent directory. omp also reads the compatibility
// file .mcp.json, but the primary file wins by precedence, so beadle only
// ever writes mcp.json and leaves the compatibility file to omp.
func ompMCPFile(home string) string {
	return filepath.Join(OmpAgentDir(home), "mcp.json")
}

// Omp builds the oh-my-pi adapter: the user context file
// <agentDir>/AGENTS.md (the only user-level context file omp loads — it
// shadows ~/.claude/CLAUDE.md and ~/.agents/AGENTS.md), the native MCP file
// <agentDir>/mcp.json (the Claude dialect, omp's own preferred config), the
// native skills directory <agentDir>/skills with the shared ~/.agents/skills
// root read-only (the native provider outranks the agents provider by name),
// the task agents <agentDir>/agents/*.md, the file commands
// <agentDir>/commands/*.md, and the project scope <cwd>/.omp/{AGENTS.md,
// mcp.json}. omp's approval policy (tools.approval*, bash.patterns) lives in
// the host-owned config.yml and is not managed.
func Omp(home, cwd string) *Agent {
	agentDir := OmpAgentDir(home)
	skills := filepath.Join(agentDir, "skills")
	shared := filepath.Join(home, ".agents", "skills")

	id := project.Resolve(cwd).ID

	return &Agent{
		ID:     OmpID,
		Name:   "oh-my-pi",
		Detect: func() (bool, error) { return OmpDetected(home) },
		Surfaces: []Surface{
			&rulesSurface{
				path: filepath.Join(agentDir, agentsMarkdown),
				traits: Traits{
					DefaultMode: config.ModeSync,
					Creatable:   true,
					Note:        "the only user-level context file omp loads: it shadows ~/.claude/CLAUDE.md and ~/.agents/AGENTS.md; the rulebook zone (RULES.md and rules/*.md) is a separate omp mechanism and is left to the tools that own it; the approval policy lives in config.yml and is not managed",
				},
			},
			&mcpSurface{
				file:    func() string { return ompMCPFile(home) },
				pointer: mcpServersPointer,
				codec:   claudeMCP,
				traits: Traits{
					DefaultMode: config.ModeSync,
					ReloadHint:  "omp reads MCP config at startup: restart omp or run /mcp reload",
					Note:        "~/.omp/agent/.mcp.json is read by omp but not written; disabledServers/enabledServers are not managed",
				},
			},
			&skillsSurface{
				dir:         skills,
				ignoreUnder: []string{filepath.Join(home, ".claude", "plugins")},
				alsoReads:   []string{shared},
				// omp dedups skills by name, priority-first: native .omp
				// skills (100) win over the shared agents home (70).
				shadowing: true,
				readOrder: []string{skills, shared},
				codec:     ompSkillCodec{},
				traits: Traits{
					DefaultMode: config.ModePull,
					Creatable:   true,
					Note:        "omp reads ~/.agents/skills natively (agents provider, priority 70); the native dir (100) wins by name",
				},
			},
			&subagentSurface{
				kind:     kind.Subagents,
				label:    subagentLabel,
				model:    subagentModel{},
				readDirs: []string{filepath.Join(agentDir, "agents")},
				writeDir: filepath.Join(agentDir, "agents"),
				codec:    ompSubagentCodec{},
				traits: Traits{
					DefaultMode: config.ModeSync,
					Creatable:   true,
					ReloadHint:  "omp discovers task agents per session",
				},
			},
			&commandSurface{
				kind:     kind.Commands,
				label:    commandLabel,
				model:    commandModel{},
				readDirs: []string{filepath.Join(agentDir, "commands")},
				writeDir: filepath.Join(agentDir, "commands"),
				codec:    commandCodec{host: "omp", args: commandArgsOmp},
				traits: Traits{
					DefaultMode: config.ModeSync,
					Creatable:   true,
					ReloadHint:  "omp has no command watcher: /reload-plugins or restart",
				},
			},
			&projectMCPSurface{dir: cwd, rel: ompProjectMCPRel, id: id},
			&projectRulesSurface{dir: cwd, file: ompProjectRulesRel, id: id},
		},
	}
}
