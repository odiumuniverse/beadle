package agent

import (
	"os"
	"path/filepath"

	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
	"github.com/odiumuniverse/agents-sync/pkg/memory"
)

const (
	ClaudeCodeID = "claude-code"
	OpenCodeID   = "opencode"
	GeminiCLIID  = "gemini-cli"
	CursorID     = "cursor"
	SharedID     = "shared"
)

func ClaudeCode(home string) *Agent {
	dir := filepath.Join(home, ".claude")
	appState := filepath.Join(home, ".claude.json")

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
				dir:         filepath.Join(dir, "skills"),
				ignoreUnder: []string{filepath.Join(dir, "plugins")},
				traits:      Traits{DefaultMode: config.ModeSync, Creatable: true},
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
		},
	}
}

func OpenCode(home, cwd string) *Agent {
	dir := filepath.Join(home, ".config", "opencode")
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		dir = filepath.Join(xdg, "opencode")
	}

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
				path: filepath.Join(dir, "AGENTS.md"),
				traits: Traits{
					DefaultMode: config.ModeSync,
					Note:        "without its own AGENTS.md OpenCode reads ~/.claude/CLAUDE.md natively",
				},
			},
			&mcpSurface{
				file: configFile, pointer: "/mcp", codec: openCodeMCP,
				traits: Traits{
					DefaultMode: config.ModeSync,
					ReloadHint:  "OpenCode reads its config at startup: restart OpenCode to load the changes",
				},
			},
			&skillsSurface{
				dir:         filepath.Join(dir, "skills"),
				ignoreUnder: []string{filepath.Join(home, ".claude", "plugins")},
				traits: Traits{
					DefaultMode: config.ModePull,
					Note:        "OpenCode reads ~/.claude/skills and ~/.agents/skills natively",
				},
			},
			&permSurface{
				file: configFile, pointer: "/permission", codec: openCodeCodec{},
				traits: Traits{
					DefaultMode: config.ModeSync,
					ReloadHint:  "OpenCode reads its config at startup: restart OpenCode to load the changes",
				},
			},
			&projectRulesSurface{dir: cwd, file: "AGENTS.md", slug: memory.Slug(cwd)},
		},
	}
}

func GeminiCLI(home, cwd string) *Agent {
	dir := filepath.Join(home, ".gemini")
	settings := filepath.Join(dir, "settings.json")

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
				traits:      Traits{DefaultMode: config.ModeSync, Creatable: true},
			},
			&permSurface{
				file: fixedPath(settings), pointer: "/tools", codec: geminiPerms,
				traits: Traits{
					DefaultMode: config.ModeSync,
					ReloadHint:  "Gemini CLI reads settings.json at startup: restart Gemini CLI to load the changes",
				},
			},
			&projectRulesSurface{dir: cwd, file: "GEMINI.md", slug: memory.Slug(cwd)},
		},
	}
}

func Cursor(home string) *Agent {
	dir := filepath.Join(home, ".cursor")
	mcpFile := filepath.Join(dir, "mcp.json")

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
