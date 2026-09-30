package agent

import (
	"path/filepath"

	"github.com/odiumuniverse/verger/pkg/hostpath"

	"github.com/odiumuniverse/beadle/pkg/agentid"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/project"
)

// The canonical agent ids, shared with verger. They are also the keys every
// vault document stores an agent under, so they are spelled once in
// pkg/agentid and re-exported here.
const (
	agentsMarkdown = "AGENTS.md"

	ClaudeCodeID     = agentid.Claude
	OpenCodeID       = agentid.OpenCode
	GeminiCLIID      = agentid.Gemini
	AntigravityCLIID = agentid.Antigravity
	CursorID         = agentid.Cursor
	CodexID          = agentid.Codex
	PiID             = agentid.Pi
	KiloID           = agentid.Kilo
	SharedID         = config.SharedAgentID
)

// Canonical returns the canonical id for a value a user typed: a historical
// id resolves to the id the vault is keyed by, anything else is returned
// unchanged so an unknown id is still reported as unknown.
func Canonical(id string) string {
	return agentid.Canonical(id)
}

func ClaudeCode(home, cwd string) *Agent {
	claudeResolved := surfaces(hostpath.Claude, home)
	dir := roots(hostpath.Claude, home).ConfigRoot
	appState := claudeResolved.MCPDoc
	id := project.Resolve(cwd).ID
	claudeProject := projectSurfaces(hostpath.Claude, cwd)

	return &Agent{
		ID:     ClaudeCodeID,
		Name:   "Claude Code",
		Detect: func() (bool, error) { return anyExists(appState, dir) },
		Surfaces: []Surface{
			&rulesSurface{
				path:   filepath.Join(dir, "CLAUDE.md"),
				traits: Traits{DefaultMode: config.ModeSync, Creatable: true},
			},
			&mcpSurface{
				file:    fixedPath(appState),
				pointer: mcpServersPointer,
				codec:   claudeMCP,
				traits:  Traits{DefaultMode: config.ModeSync, ReloadHint: "new Claude Code sessions load MCP changes"},
			},
			&skillsSurface{
				dir:              claudeResolved.Skills,
				ignoreUnder:      claudeResolved.IgnoreRoots,
				namespacedBundle: true,
				traits:           Traits{DefaultMode: config.ModeSync, Creatable: true},
			},
			&memorySurface{
				projects: claudeResolved.ProjectsDir,
			},
			&subagentSurface{
				kind:      kind.Subagents,
				label:     subagentLabel,
				model:     subagentModel{},
				readDirs:  claudeResolved.AgentsReads,
				writeDir:  claudeResolved.Agents,
				recursive: true,
				codec:     claudeSubagentCodec{},
				traits: Traits{
					DefaultMode: config.ModeSync,
					Creatable:   true,
					ReloadHint:  "Claude Code watches ~/.claude/agents; a directory created after the session started needs a restart",
				},
			},
			&commandSurface{
				kind:     kind.Commands,
				label:    commandLabel,
				model:    commandModel{},
				readDirs: claudeResolved.CommandsReads,
				writeDir: claudeResolved.Commands,
				codec: commandCodec{
					host: "claude", args: commandArgsClaude,
					model: true, hint: true, arguments: true, disable: true,
				},
				traits: Traits{
					DefaultMode: config.ModeSync,
					Creatable:   true,
					ReloadHint:  "Claude Code reads commands at startup: restart Claude Code to load the changes",
					Note:        "a skill with the same name wins over a legacy command (doctor reports the duplicate)",
				},
			},
			&permSurface{
				file:    fixedPath(claudeResolved.Hooks),
				pointer: "/permissions",
				codec:   claudePerms,
				traits:  Traits{DefaultMode: config.ModeSync},
			},
			&projectMCPSurface{dir: cwd, rel: projectRel(cwd, claudeProject.MCPDoc), id: id},
			&claudeRulesSurface{dir: cwd, id: id, rulesRel: projectRel(cwd, claudeProject.RulesPerFile)},
			&projectRulesSurface{dir: cwd, file: projectRel(cwd, claudeProject.Rules), id: id},
		},
	}
}

func OpenCode(home, cwd string) *Agent {
	openCodeResolved := surfaces(hostpath.OpenCode, home)
	dir := roots(hostpath.OpenCode, home).ConfigRoot

	id := project.Resolve(cwd).ID

	// The candidate list is the resolver's, most preferred first, and the last
	// entry is the document a host with neither gets.
	configFile := func() string {
		for _, candidate := range openCodeResolved.MCPDocCandidates {
			if fsutil.Exists(candidate) {
				return candidate
			}
		}

		return openCodeResolved.MCPDoc
	}

	return &Agent{
		ID:     OpenCodeID,
		Name:   "OpenCode",
		Detect: func() (bool, error) { return anyExists(dir) },
		Surfaces: []Surface{
			&rulesSurface{
				path: filepath.Join(dir, agentsMarkdown),
				traits: Traits{
					DefaultMode: config.ModeSync,
					Note:        "OpenCode V2 reads AGENTS.md only: the CLAUDE.md fallback is gone; V1 and V2 config dialects are both supported",
				},
			},
			&mcpSurface{
				file: configFile, pointer: openCodeMCPPointer, codec: openCodeMCP,
				target: openCodeMCPTarget,
				traits: Traits{
					DefaultMode: config.ModeSync,
					ReloadHint:  "OpenCode reads its config at startup: restart OpenCode to load the changes",
				},
			},
			&skillsSurface{
				dir:         openCodeResolved.Skills,
				ignoreUnder: openCodeResolved.IgnoreRoots,
				alsoReads:   withoutDir(openCodeResolved.SkillsReads, openCodeResolved.Skills),
				// Verified on OpenCode v2.0.12 with marker probes: the host
				// keeps one copy per skill id, the own config directory wins
				// over ~/.agents/skills, which wins over ~/.claude/skills.
				// A project-local .opencode/skills takes precedence over the
				// globals (probed); the project scope is outside beadle's
				// model.
				shadowing: true,
				readOrder: openCodeResolved.SkillsReads,
				// Verified against the host source (glob {*.md,**/SKILL.md}):
				// a root-level <name>.md is a skill named by its basename.
				flatSkills: true,
				traits: Traits{
					DefaultMode: config.ModePull,
					Note:        "OpenCode reads ~/.claude/skills and ~/.agents/skills natively",
				},
			},
			&commandSurface{
				kind:       kind.Commands,
				label:      commandLabel,
				model:      commandModel{},
				readDirs:   []string{filepath.Join(dir, "commands"), filepath.Join(dir, "command")},
				writeDir:   filepath.Join(dir, "commands"),
				nestedNote: "nested command ids are not synced yet",
				codec:      commandCodec{host: "opencode", args: commandArgsOpenCode, model: true},
				traits: Traits{
					DefaultMode: config.ModeSync,
					Creatable:   true,
					ReloadHint:  "OpenCode reads commands at startup: restart OpenCode to load the changes",
				},
			},
			&permSurface{
				file: configFile, pointer: openCodePermissionPointer, codec: openCodeCodec{},
				v2Codec: openCodeV2Codec{},
				target:  openCodePermissionTarget,
				traits: Traits{
					DefaultMode: config.ModeSync,
					ReloadHint:  "OpenCode reads its config at startup: restart OpenCode to load the changes",
				},
			},
			&subagentSurface{
				kind:       kind.Subagents,
				label:      subagentLabel,
				model:      subagentModel{},
				readDirs:   []string{filepath.Join(dir, "agents"), filepath.Join(dir, "agent")},
				writeDir:   filepath.Join(dir, "agents"),
				nestedNote: "nested subagent id",
				codec:      openCodeSubagentCodec{},
				traits: Traits{
					DefaultMode: config.ModeSync,
					Creatable:   true,
					Note:        "OpenCode derives the agent id from the file name; the plural agents/ directory wins over the legacy agent/ one",
				},
			},
			&projectRulesSurface{dir: cwd, file: agentsMarkdown, id: id},
		},
	}
}

func GeminiCLI(home, cwd string) *Agent {
	geminiResolved := surfaces(hostpath.Gemini, home)
	dir := roots(hostpath.Gemini, home).ConfigRoot
	geminiProject := projectSurfaces(hostpath.Gemini, cwd)
	settings := geminiResolved.MCPDoc
	id := project.Resolve(cwd).ID

	return &Agent{
		ID:     GeminiCLIID,
		Name:   "Gemini CLI",
		Detect: func() (bool, error) { return anyExists(settings, dir) },
		Surfaces: []Surface{
			&rulesSurface{
				path:   geminiResolved.Rules,
				traits: Traits{DefaultMode: config.ModeSync, Creatable: true},
			},
			&mcpSurface{
				file: fixedPath(settings), pointer: mcpServersPointer, codec: geminiMCP,
				traits: Traits{
					DefaultMode: config.ModeSync,
					ReloadHint:  "Gemini CLI: run /mcp reload or restart Gemini CLI to load MCP changes",
				},
			},
			&skillsSurface{
				dir:         geminiResolved.Skills,
				ignoreUnder: geminiResolved.IgnoreRoots,
				alsoReads:   withoutDir(geminiResolved.SkillsReads, geminiResolved.Skills),
				traits:      Traits{DefaultMode: config.ModeSync, Creatable: true},
			},
			&permSurface{
				file: fixedPath(settings), pointer: "/tools", codec: geminiPerms,
				traits: Traits{
					DefaultMode: config.ModeSync,
					ReloadHint:  "Gemini CLI reads settings.json at startup: restart Gemini CLI to load the changes",
				},
			},
			&subagentSurface{
				kind:     kind.Subagents,
				label:    subagentLabel,
				model:    subagentModel{},
				readDirs: geminiResolved.AgentsReads,
				writeDir: geminiResolved.Agents,
				codec:    geminiSubagentCodec{},
				traits: Traits{
					DefaultMode: config.ModeSync,
					Creatable:   true,
					ReloadHint:  "Gemini CLI reads agents at startup: restart Gemini CLI to load the changes",
					Note:        "kind: remote subagents are not synced (A-28)",
				},
			},
			&commandSurface{
				kind:       kind.Commands,
				label:      commandLabel,
				model:      commandModel{},
				readDirs:   geminiResolved.CommandsReads,
				writeDir:   geminiResolved.Commands,
				nestedNote: "nested command ids are not synced yet",
				exts:       []string{".toml"},
				codec:      geminiCommandCodec{},
				traits: Traits{
					DefaultMode: config.ModeSync,
					Creatable:   true,
					ReloadHint:  "Gemini CLI reads commands at startup: restart Gemini CLI to load the changes",
				},
			},
			&projectRulesSurface{dir: cwd, file: projectRel(cwd, geminiProject.Rules), id: id},
		},
	}
}

func AntigravityCLI(home, cwd string) *Agent {
	// dir is the host's own state directory (~/.gemini/antigravity-cli): the
	// host's name, not the beadle agent id (agy). The shared resolver owns it,
	// so renaming the id can never move it.
	agyResolved := surfaces(hostpath.Agy, home)
	agyProject := projectSurfaces(hostpath.Agy, cwd)
	dir := agyResolved.StateDir
	mcpConfig := agyResolved.MCPDoc
	id := project.Resolve(cwd).ID

	return &Agent{
		ID:     AntigravityCLIID,
		Name:   "Antigravity CLI",
		Detect: func() (bool, error) { return anyExists(dir, mcpConfig) },
		// Antigravity is the honest case (W7-UX §5.3): its adapter is
		// experimental and has never been live-probed, so the screen must not
		// claim it looked and found nothing — it says the probe never ran.
		// The claim is stated here rather than inferred from a missing marker,
		// because an inference would apply the same words to every host that
		// happens to lack one.
		DetectReason: func() (Detection, error) {
			found, err := anyExists(dir, mcpConfig)
			if err != nil {
				return Detection{}, err
			}

			if found {
				return Detection{Found: true, Reason: "found at " + dir}, nil
			}

			return Detection{Found: false, Missing: NeverProbed}, nil
		},
		Surfaces: []Surface{
			&mcpSurface{
				file:    fixedPath(mcpConfig),
				pointer: mcpServersPointer,
				codec:   antigravityMCP,
				traits: Traits{
					DefaultMode: config.ModeSync,
					ReloadHint:  "Antigravity CLI reads mcp_config.json at startup: restart agy to load the changes",
				},
			},
			&subagentSurface{
				kind:      kind.Subagents,
				label:     subagentLabel,
				model:     subagentModel{},
				readDirs:  agyResolved.AgentsReads,
				writeDir:  agyResolved.Agents,
				recursive: true,
				nameCheck: nameCheckNestedAgent,
				codec:     antigravitySubagentCodec{},
				traits: Traits{
					DefaultMode: config.ModeSync,
					Creatable:   true,
					ReloadHint:  "Antigravity reads agents at startup: restart agy to load the changes",
					Note:        "workspace .agents/agents/ subagents are not synced yet (A-28)",
				},
			},
			&projectMCPSurface{dir: cwd, rel: projectRel(cwd, agyProject.MCPDoc), id: id},
		},
	}
}

func Cursor(home, cwd string) *Agent {
	cursorResolved := surfaces(hostpath.Cursor, home)
	dir := roots(hostpath.Cursor, home).ConfigRoot
	cursorProject := projectSurfaces(hostpath.Cursor, cwd)
	mcpFile := cursorResolved.MCPDoc
	id := project.Resolve(cwd).ID

	return &Agent{
		ID:     CursorID,
		Name:   "Cursor",
		Detect: func() (bool, error) { return anyExists(mcpFile, dir) },
		Surfaces: []Surface{
			&mcpSurface{
				file:    fixedPath(mcpFile),
				pointer: mcpServersPointer,
				codec:   cursorMCP,
				traits:  Traits{DefaultMode: config.ModeSync, ReloadHint: "Cursor reads mcp.json at startup: reload MCP servers or restart Cursor"},
			},
			&skillsSurface{
				dir:         cursorResolved.Skills,
				ignoreUnder: cursorResolved.IgnoreRoots,
				alsoReads:   withoutDir(cursorResolved.SkillsReads, cursorResolved.Skills),
				traits: Traits{
					DefaultMode: config.ModePull,
					Note:        "Cursor reads ~/.claude/skills and ~/.agents/skills natively",
				},
			},
			&permSurface{
				file:    fixedPath(cursorResolved.Settings),
				pointer: "/permissions",
				codec:   cursorPerms,
				traits:  Traits{DefaultMode: config.ModeSync},
			},
			&subagentSurface{
				kind:     kind.Subagents,
				label:    subagentLabel,
				model:    subagentModel{},
				readDirs: cursorResolved.AgentsReads,
				writeDir: cursorResolved.Agents,
				exts:     []string{".md", ".mdc", ".markdown"},
				codec:    cursorSubagentCodec{},
				traits: Traits{
					DefaultMode: config.ModeSync,
					Creatable:   true,
					ReloadHint:  "Cursor reads agents at startup: restart Cursor to load the changes",
				},
			},
			// cursor-agent loads <project>/.claude/commands,
			// <project>/.cursor/commands, ~/.claude/commands and then
			// ~/.cursor/commands into one map keyed by id, last write wins
			// (its own loader, 2026.06.15 and 2026.09.26), with no duplicate
			// warning. The surface therefore writes — and reads — its own
			// directory only: listing another host's write target here would
			// make `locate` write the command into Claude's file instead of
			// Cursor's, and turning Claude's surface off would then take
			// Cursor's copy with it. Cursor's own copy always wins in the
			// host's map, so no dedupe against ~/.claude/commands is needed.
			&commandSurface{
				kind:     kind.Commands,
				label:    commandLabel,
				model:    commandModel{},
				readDirs: cursorResolved.CommandsReads,
				writeDir: cursorResolved.Commands,
				codec:    cursorCommandCodec{},
				traits: Traits{
					DefaultMode: config.ModeSync,
					Creatable:   true,
					ReloadHint:  "Cursor reads commands at startup: restart Cursor to load the changes",
					Note: "plain markdown: Cursor takes the title from the first line and expands only " +
						"$ARGUMENTS and $1..; Cursor also loads ~/.claude/commands, and its own copy of a " +
						"command wins there. This surface reads and writes only its own directory: a " +
						"frontmatter command file is not Cursor's and is left alone",
				},
			},
			&projectMCPSurface{dir: cwd, rel: projectRel(cwd, cursorProject.MCPDoc), id: id},
			&cursorRulesSurface{dir: cwd, id: id, rulesRel: projectRel(cwd, cursorProject.RulesPerFile)},
		},
	}
}

func Codex(home, cwd string) *Agent {
	codexResolved := surfaces(hostpath.Codex, home)
	dir := roots(hostpath.Codex, home).ConfigRoot
	id := project.Resolve(cwd).ID

	return &Agent{
		ID:     CodexID,
		Name:   "Codex CLI",
		Detect: func() (bool, error) { return anyExists(dir) },
		Surfaces: []Surface{
			&rulesSurface{
				path:   filepath.Join(dir, agentsMarkdown),
				traits: Traits{DefaultMode: config.ModeSync, Note: "AGENTS.override.md takes precedence over AGENTS.md"},
			},
			&tomlMCPSurface{
				file:  fixedPath(filepath.Join(dir, "config.toml")),
				codec: codexMCP,
				traits: Traits{
					DefaultMode: config.ModeSync,
					Creatable:   true,
					ReloadHint:  "Codex CLI reads config.toml at startup: restart Codex to load the changes",
				},
			},
			&skillsSurface{
				// The write target is the shared hub, which Codex reads
				// natively; ~/.codex/skills is the deprecated location and
				// stays a read. Both values come from the shared resolver
				// (hostpath fills Skills=hub, SkillsReads=[hub, legacy]).
				dir:         codexResolved.Skills,
				ignoreUnder: codexResolved.IgnoreRoots,
				alsoReads:   withoutDir(codexResolved.SkillsReads, codexResolved.Skills),
				traits: Traits{
					DefaultMode: config.ModePull,
					Note:        "Codex reads ~/.agents/skills natively; ~/.codex/skills is the deprecated location",
				},
			},
			&subagentSurface{
				kind:      kind.Subagents,
				label:     subagentLabel,
				model:     subagentModel{},
				readDirs:  []string{filepath.Join(dir, "agents")},
				writeDir:  filepath.Join(dir, "agents"),
				exts:      []string{".toml"},
				nameCheck: nameCheckNone,
				codec:     codexSubagentCodec{},
				traits: Traits{
					DefaultMode: config.ModeSync,
					Creatable:   true,
					ReloadHint:  "Codex CLI reads agents at startup: restart Codex to load the changes",
				},
			},
			&commandSurface{
				kind:     kind.Commands,
				label:    commandLabel,
				model:    commandModel{},
				readDirs: []string{filepath.Join(dir, "prompts")},
				writeDir: filepath.Join(dir, "prompts"),
				codec:    commandCodec{host: "codex", args: commandArgsCodex, hint: true, pullOnly: true},
				traits: Traits{
					DefaultMode: config.ModePull,
					Creatable:   true,
					Note:        "Codex prompts are deprecated: beadle pulls them into the vault and does not write them back",
				},
			},
			&projectRulesSurface{dir: cwd, file: agentsMarkdown, id: id},
		},
	}
}

func Pi(home, cwd string) *Agent {
	piResolved := surfaces(hostpath.Pi, home)
	piProject := projectSurfaces(hostpath.Pi, cwd)
	dir := roots(hostpath.Pi, home).ConfigRoot
	id := project.Resolve(cwd).ID

	return &Agent{
		ID:     PiID,
		Name:   "Pi",
		Detect: func() (bool, error) { return anyExists(dir) },
		Surfaces: []Surface{
			&rulesSurface{
				path:   piResolved.Rules,
				traits: Traits{DefaultMode: config.ModeSync, Note: "AGENTS.override.md takes precedence over AGENTS.md"},
			},
			&mcpSurface{
				file:    fixedPath(piResolved.MCPDoc),
				pointer: mcpServersPointer,
				codec:   piMCP,
				traits: Traits{
					DefaultMode: config.ModeSync,
					ReloadHint:  "Pi reads its config at startup: restart Pi to load the changes",
					Note:        "Pi has no built-in MCP; the servers work only with the third-party pi-mcp-adapter (pi install npm:pi-mcp-adapter)",
				},
			},
			&skillsSurface{
				dir:         piResolved.Skills,
				ignoreUnder: piResolved.IgnoreRoots,
				alsoReads:   withoutDir(piResolved.SkillsReads, piResolved.Skills),
				// Pi docs: root .md files are skills in ~/.pi/agent/skills
				// and .pi/skills, while root .md files in ~/.agents/skills
				// are ignored (nested group files are picked up).
				flatSkills: true,
				traits: Traits{
					DefaultMode: config.ModePull,
					Note:        "Pi reads ~/.agents/skills natively",
				},
			},
			&commandSurface{
				kind:     kind.Commands,
				label:    commandLabel,
				model:    commandModel{},
				readDirs: []string{filepath.Join(dir, "prompts")},
				writeDir: filepath.Join(dir, "prompts"),
				codec:    commandCodec{host: "pi", args: commandArgsPi, hint: true},
				traits: Traits{
					DefaultMode: config.ModeSync,
					Creatable:   true,
					ReloadHint:  "Pi reads prompt templates at startup: restart Pi to load the changes",
				},
			},
			&projectRulesSurface{dir: cwd, file: projectRel(cwd, piProject.Rules), id: id},
		},
	}
}

func Kilo(home, cwd string) *Agent {
	dir := KiloConfigDir(home)
	id := project.Resolve(cwd).ID

	configFile := func() string {
		for _, name := range []string{"kilo.jsonc", "kilo.json"} {
			if path := filepath.Join(dir, name); fsutil.Exists(path) {
				return path
			}
		}

		return filepath.Join(dir, "kilo.json")
	}

	return &Agent{
		ID:   KiloID,
		Name: "Kilo Code",
		// The legacy ~/.kilo directory is how older copies of the host are
		// found; the resolver carries it as a read directory, so the detect
		// root comes from there rather than a second literal.
		Detect: func() (bool, error) {
			for _, candidate := range append([]string{dir}, surfaces(hostpath.Kilo, home).SkillsReads...) {
				if found, err := anyExists(candidate); err != nil || found {
					return found, err
				}
			}

			return false, nil
		},
		Surfaces: []Surface{
			&rulesSurface{
				path: filepath.Join(dir, agentsMarkdown),
				traits: Traits{
					DefaultMode: config.ModeSync,
					Note:        "Kilo reads AGENTS.md; the legacy opencode.json(c) is not managed",
				},
			},
			&mcpSurface{
				file: configFile, pointer: "/mcp", codec: openCodeMCP,
				traits: Traits{
					DefaultMode: config.ModeSync,
					ReloadHint:  "Kilo reads its config at startup: restart Kilo to load the changes",
				},
			},
			kiloSkillsSurface(home),
			&permSurface{
				file: configFile, pointer: "/permission", codec: openCodeCodec{},
				traits: Traits{
					DefaultMode: config.ModeSync,
					ReloadHint:  "Kilo reads its config at startup: restart Kilo to load the changes",
				},
			},
			&commandSurface{
				kind:       kind.Commands,
				label:      commandLabel,
				model:      commandModel{},
				readDirs:   KiloCommandDirs(home),
				writeDir:   filepath.Join(KiloConfigDir(home), kiloCommandsDirName),
				nestedNote: "nested command ids are not synced yet",
				codec:      commandCodec{host: "kilo", args: commandArgsOpenCode, model: true},
				traits: Traits{
					DefaultMode: config.ModeSync,
					Creatable:   true,
					ReloadHint:  "Kilo reads commands at startup: restart Kilo to load the changes",
				},
			},
			kiloSubagentSurface(home),
			&projectRulesSurface{dir: cwd, file: agentsMarkdown, id: id},
		},
	}
}

func SharedSkills(home string) *Agent {
	// The hub is beadle's pseudo-agent, not one of the resolver's ten host
	// ids, so it has its own entry point rather than an eleventh id: giving it
	// one would let a caller ask for a config root, a rules file and a hooks
	// document it does not have.
	hub := sharedHub(home)

	return &Agent{
		ID:     SharedID,
		Name:   "Shared skills (~/.agents/skills)",
		Detect: func() (bool, error) { return true, nil },
		Surfaces: []Surface{
			&skillsSurface{
				dir:         hub.Skills,
				ignoreUnder: hub.IgnoreRoots,
				traits: Traits{
					DefaultMode: config.ModeSync,
					Creatable:   true,
					Note:        "the shared skills hub: beadle is its writer, the hosts that read it natively (OpenCode, Gemini CLI, Cursor, Codex, Copilot, oh-my-pi) keep their own copy read-only and win by name; a file another tool wrote here is adopted into the canon instead of being deleted",
				},
			},
		},
	}
}
