package agent

import (
	"os"
	"path/filepath"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/project"
)

const (
	agentsMarkdown = "AGENTS.md"

	ClaudeCodeID     = "claude-code"
	OpenCodeID       = "opencode"
	GeminiCLIID      = "gemini-cli"
	AntigravityCLIID = "antigravity-cli"
	CursorID         = "cursor"
	CodexID          = "codex"
	PiID             = "pi"
	KiloID           = "kilo"
	SharedID         = "shared"
)

func ClaudeCode(home, cwd string) *Agent {
	dir := filepath.Join(home, ".claude")
	appState := filepath.Join(home, ".claude.json")
	id := project.Resolve(cwd).ID

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
				dir:              filepath.Join(dir, "skills"),
				ignoreUnder:      []string{filepath.Join(dir, "plugins")},
				namespacedBundle: true,
				traits:           Traits{DefaultMode: config.ModeSync, Creatable: true},
			},
			&memorySurface{
				projects: filepath.Join(dir, "projects"),
			},
			&permSurface{
				file:    fixedPath(filepath.Join(dir, "settings.json")),
				pointer: "/permissions",
				codec:   claudePerms,
				traits:  Traits{DefaultMode: config.ModeSync},
			},
			&projectMCPSurface{dir: cwd, rel: ".mcp.json", id: id},
		},
	}
}

func OpenCode(home, cwd string) *Agent {
	dir := filepath.Join(home, ".config", "opencode")
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		dir = filepath.Join(xdg, "opencode")
	}

	id := project.Resolve(cwd).ID

	configFile := func() string {
		for _, name := range []string{"opencode.jsonc", "opencode.json"} {
			if path := filepath.Join(dir, name); fsutil.Exists(path) {
				return path
			}
		}

		return filepath.Join(dir, "opencode.json")
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
				dir:         filepath.Join(dir, "skills"),
				ignoreUnder: []string{filepath.Join(home, ".claude", "plugins")},
				alsoReads:   []string{filepath.Join(home, ".claude", "skills"), filepath.Join(home, ".agents", "skills")},
				// The V2 docs define the read order (own directory last, so
				// highest precedence); they are untested, so shadowing stays
				// off and the order is informational only.
				readOrder: []string{
					filepath.Join(dir, "skills"),
					filepath.Join(home, ".agents", "skills"),
					filepath.Join(home, ".claude", "skills"),
				},
				traits: Traits{
					DefaultMode: config.ModePull,
					Note:        "OpenCode reads ~/.claude/skills and ~/.agents/skills natively",
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
			&projectRulesSurface{dir: cwd, file: agentsMarkdown, id: id},
		},
	}
}

func GeminiCLI(home, cwd string) *Agent {
	dir := filepath.Join(home, ".gemini")
	settings := filepath.Join(dir, "settings.json")
	id := project.Resolve(cwd).ID

	return &Agent{
		ID:     GeminiCLIID,
		Name:   "Gemini CLI",
		Detect: func() (bool, error) { return anyExists(settings, dir) },
		Surfaces: []Surface{
			&rulesSurface{
				path:   filepath.Join(dir, "GEMINI.md"),
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
				dir:         filepath.Join(dir, "skills"),
				ignoreUnder: []string{filepath.Join(home, ".claude", "plugins")},
				alsoReads:   []string{filepath.Join(home, ".agents", "skills")},
				traits:      Traits{DefaultMode: config.ModeSync, Creatable: true},
			},
			&permSurface{
				file: fixedPath(settings), pointer: "/tools", codec: geminiPerms,
				traits: Traits{
					DefaultMode: config.ModeSync,
					ReloadHint:  "Gemini CLI reads settings.json at startup: restart Gemini CLI to load the changes",
				},
			},
			&projectRulesSurface{dir: cwd, file: "GEMINI.md", id: id},
		},
	}
}

func AntigravityCLI(home, cwd string) *Agent {
	dir := filepath.Join(home, ".gemini", "antigravity-cli")
	mcpConfig := filepath.Join(home, ".gemini", "config", "mcp_config.json")
	id := project.Resolve(cwd).ID

	return &Agent{
		ID:     AntigravityCLIID,
		Name:   "Antigravity CLI",
		Detect: func() (bool, error) { return anyExists(dir, mcpConfig) },
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
			&projectMCPSurface{dir: cwd, rel: ".agents/mcp_config.json", id: id},
		},
	}
}

func Cursor(home, cwd string) *Agent {
	dir := filepath.Join(home, ".cursor")
	mcpFile := filepath.Join(dir, "mcp.json")
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
				dir:         filepath.Join(dir, "skills"),
				ignoreUnder: []string{filepath.Join(home, ".claude", "plugins")},
				alsoReads:   []string{filepath.Join(home, ".claude", "skills"), filepath.Join(home, ".agents", "skills")},
				traits: Traits{
					DefaultMode: config.ModePull,
					Note:        "Cursor reads ~/.claude/skills and ~/.agents/skills natively",
				},
			},
			&permSurface{
				file:    fixedPath(filepath.Join(dir, "cli-config.json")),
				pointer: "/permissions",
				codec:   cursorPerms,
				traits:  Traits{DefaultMode: config.ModeSync},
			},
			&projectMCPSurface{dir: cwd, rel: ".cursor/mcp.json", id: id},
			&cursorRulesSurface{dir: cwd, id: id},
		},
	}
}

func Codex(home, cwd string) *Agent {
	dir := filepath.Join(home, ".codex")
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
				dir:         filepath.Join(dir, "skills"),
				ignoreUnder: []string{filepath.Join(home, ".claude", "plugins")},
				alsoReads:   []string{filepath.Join(home, ".agents", "skills")},
				traits: Traits{
					DefaultMode: config.ModePull,
					Note:        "Codex reads ~/.agents/skills natively; ~/.codex/skills is the deprecated location",
				},
			},
			&projectRulesSurface{dir: cwd, file: agentsMarkdown, id: id},
		},
	}
}

func Pi(home, cwd string) *Agent {
	dir := filepath.Join(home, ".pi", "agent")
	id := project.Resolve(cwd).ID

	return &Agent{
		ID:     PiID,
		Name:   "Pi",
		Detect: func() (bool, error) { return anyExists(dir) },
		Surfaces: []Surface{
			&rulesSurface{
				path:   filepath.Join(dir, agentsMarkdown),
				traits: Traits{DefaultMode: config.ModeSync, Note: "AGENTS.override.md takes precedence over AGENTS.md"},
			},
			&mcpSurface{
				file:    fixedPath(filepath.Join(dir, "mcp.json")),
				pointer: mcpServersPointer,
				codec:   piMCP,
				traits: Traits{
					DefaultMode: config.ModeSync,
					ReloadHint:  "Pi reads its config at startup: restart Pi to load the changes",
				},
			},
			&skillsSurface{
				dir:         filepath.Join(dir, "skills"),
				ignoreUnder: []string{filepath.Join(home, ".claude", "plugins")},
				alsoReads:   []string{filepath.Join(home, ".agents", "skills")},
				traits: Traits{
					DefaultMode: config.ModePull,
					Note:        "Pi reads ~/.agents/skills natively",
				},
			},
			&projectRulesSurface{dir: cwd, file: agentsMarkdown, id: id},
		},
	}
}

func Kilo(home, cwd string) *Agent {
	dir := filepath.Join(home, ".config", "kilo")
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
		ID:     KiloID,
		Name:   "Kilo Code",
		Detect: func() (bool, error) { return anyExists(dir) },
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
			&skillsSurface{
				dir:         filepath.Join(home, ".kilo", "skills"),
				ignoreUnder: []string{filepath.Join(home, ".claude", "plugins")},
				alsoReads:   []string{filepath.Join(home, ".agents", "skills")},
				traits: Traits{
					DefaultMode: config.ModePull,
					Note:        "Kilo reads ~/.agents/skills natively",
				},
			},
			&permSurface{
				file: configFile, pointer: "/permission", codec: openCodeCodec{},
				traits: Traits{
					DefaultMode: config.ModeSync,
					ReloadHint:  "Kilo reads its config at startup: restart Kilo to load the changes",
				},
			},
			&projectRulesSurface{dir: cwd, file: agentsMarkdown, id: id},
		},
	}
}

func SharedSkills(home string) *Agent {
	return &Agent{
		ID:     SharedID,
		Name:   "Shared skills (~/.agents/skills)",
		OptIn:  true,
		Detect: func() (bool, error) { return true, nil },
		Surfaces: []Surface{
			&skillsSurface{
				dir:         filepath.Join(home, ".agents", "skills"),
				ignoreUnder: []string{filepath.Join(home, ".claude", "plugins")},
				traits: Traits{
					DefaultMode: config.ModeSync,
					Creatable:   true,
					Note:        "read natively by OpenCode, Gemini CLI, Cursor, Codex and Copilot",
				},
			},
		},
	}
}
