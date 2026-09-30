package agent_test

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// The paths a host keeps a canon item in, per agent and per kind. The rows are
// the **write targets a pull/push actually uses** — the surface path a delivery
// lands on — not the resolver's internals: if an adapter is wired to the wrong
// resolver field, the delivered file moves, and this table is what notices.
//
// The expected values are the ones the shared resolver produces for a home with
// no environment set, so the table doubles as a readable statement of where
// each host reads and writes.
var hostPathMatrix = []struct {
	id    string
	build func(home, cwd string) *agent.Agent
	paths map[kind.ID]string
}{
	{agent.ClaudeCodeID, agent.ClaudeCode, map[kind.ID]string{
		kind.Rules:     ".claude/CLAUDE.md",
		kind.Skills:    ".claude/skills",
		kind.Subagents: ".claude/agents",
		kind.Commands:  ".claude/commands",
		kind.MCP:       ".claude.json",
	}},
	{agent.CodexID, agent.Codex, map[kind.ID]string{
		kind.Rules:     ".codex/AGENTS.md",
		kind.Skills:    ".agents/skills",
		kind.Subagents: ".codex/agents",
		kind.Commands:  ".codex/prompts",
		kind.MCP:       ".codex/config.toml",
	}},
	{agent.GeminiCLIID, agent.GeminiCLI, map[kind.ID]string{
		kind.Rules:     ".gemini/GEMINI.md",
		kind.Skills:    ".gemini/skills",
		kind.Subagents: ".gemini/agents",
		kind.Commands:  ".gemini/commands",
		kind.MCP:       ".gemini/settings.json",
	}},
	{agent.CursorID, agent.Cursor, map[kind.ID]string{
		kind.Skills:    ".cursor/skills",
		kind.Subagents: ".cursor/agents",
		kind.Commands:  ".cursor/commands",
		kind.MCP:       ".cursor/mcp.json",
	}},
	{agent.OpenCodeID, agent.OpenCode, map[kind.ID]string{
		kind.Rules:     ".config/opencode/AGENTS.md",
		kind.Skills:    ".config/opencode/skills",
		kind.Subagents: ".config/opencode/agents",
		kind.Commands:  ".config/opencode/commands",
		kind.MCP:       ".config/opencode/opencode.json",
	}},
	{agent.KiloID, agent.Kilo, map[kind.ID]string{
		kind.Rules:     ".config/kilo/AGENTS.md",
		kind.Skills:    ".config/kilo/skills",
		kind.Subagents: ".config/kilo/agents",
		kind.Commands:  ".config/kilo/commands",
		kind.MCP:       ".config/kilo/kilo.json",
	}},
	{agent.PiID, agent.Pi, map[kind.ID]string{
		kind.Rules:    ".pi/agent/AGENTS.md",
		kind.Skills:   ".pi/agent/skills",
		kind.Commands: ".pi/agent/prompts",
		kind.MCP:      ".pi/agent/mcp.json",
	}},
	{agent.AntigravityCLIID, agent.AntigravityCLI, map[kind.ID]string{
		kind.Subagents: ".gemini/config/agents",
		kind.MCP:       ".gemini/config/mcp_config.json",
	}},
	{agent.DSHID, agent.DSH, map[kind.ID]string{
		kind.Rules:  ".dsh/AGENTS.md",
		kind.Skills: ".dsh/skills",
		kind.MCP:    ".dsh/cordis.patch.yml",
	}},
	{agent.OmpID, agent.Omp, map[kind.ID]string{
		kind.Rules:     ".omp/agent/AGENTS.md",
		kind.Skills:    ".omp/agent/skills",
		kind.Subagents: ".omp/agent/agents",
		kind.Commands:  ".omp/agent/commands",
		kind.MCP:       ".omp/agent/mcp.json",
	}},
}

// envKeys are every environment variable the resolver reads. The matrix clears
// every one of them first, so a developer's own value cannot make a row pass or
// fail by accident.
var envKeys = []string{
	"XDG_CONFIG_HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "GEMINI_CLI_HOME",
	"OPENCODE_CONFIG_DIR", "KILO_CONFIG_DIR", "CURSOR_CONFIG_DIR",
	"PI_CODING_AGENT_DIR", "DSH_HOME", "DSH_AGENTS_HOME", "PI_CONFIG_DIR",
	"OMP_PROFILE", "PI_PROFILE",
}

// TestHostPathMatrix pins, for all ten agents, the paths a pull/push writes to,
// in an environment where nothing is set. A surface wired to the wrong
// resolver field moves its path and fails here.
func TestHostPathMatrix(t *testing.T) {
	Convey("Given a home and an empty environment", t, func() {
		home := t.TempDir()
		cwd := t.TempDir()
		clearHostEnv(t)

		for _, row := range hostPathMatrix {
			Convey("When "+row.id+" is constructed", func() {
				a := row.build(home, cwd)

				for _, k := range sortedKinds(row.paths) {
					Convey("Then its "+string(k)+" write target is the resolver's", func() {
						surface := a.Surface(k)
						So(surface, ShouldNotBeNil)
						So(surface.Path(), ShouldEqual, filepath.Join(home, filepath.FromSlash(row.paths[k])))
					})
				}
			})
		}
	})
}

// TestHostPathMatrixEnv moves the roots a host honours and asserts the write
// target follows, per agent. The XDG family is the shared case; each host's own
// override is the case beadle did not have before the resolver took over.
func TestHostPathMatrixEnv(t *testing.T) {
	Convey("Given a home and a moved root", t, func() {
		home := t.TempDir()
		cwd := t.TempDir()
		xdg := t.TempDir()
		override := t.TempDir()
		clearHostEnv(t)

		cases := []struct {
			name   string
			env    map[string]string
			id     string
			kind   kind.ID
			expect string
		}{
			{"opencode follows XDG", map[string]string{"XDG_CONFIG_HOME": "{xdg}"}, agent.OpenCodeID, kind.Skills, "{xdg}/opencode/skills"},
			{"kilo follows XDG", map[string]string{"XDG_CONFIG_HOME": "{xdg}"}, agent.KiloID, kind.Skills, "{xdg}/kilo/skills"},
			{"kilo prefers its own override", map[string]string{"XDG_CONFIG_HOME": "{xdg}", "KILO_CONFIG_DIR": "{override}"}, agent.KiloID, kind.Skills, "{override}/skills"},
			{"opencode prefers its own override", map[string]string{"XDG_CONFIG_HOME": "{xdg}", "OPENCODE_CONFIG_DIR": "{override}"}, agent.OpenCodeID, kind.Skills, "{override}/skills"},
			{"claude moves its whole tree", map[string]string{"CLAUDE_CONFIG_DIR": "{override}"}, agent.ClaudeCodeID, kind.Skills, "{override}/skills"},
			{"claude moves the state document too", map[string]string{"CLAUDE_CONFIG_DIR": "{override}"}, agent.ClaudeCodeID, kind.MCP, "{override}/.claude.json"},
			{"codex follows CODEX_HOME", map[string]string{"CODEX_HOME": "{override}"}, agent.CodexID, kind.Commands, "{override}/prompts"},
			{"pi follows its agent dir", map[string]string{"PI_CODING_AGENT_DIR": "{override}"}, agent.PiID, kind.MCP, "{override}/mcp.json"},
			{"dsh follows DSH_HOME", map[string]string{"DSH_HOME": "{override}"}, agent.DSHID, kind.Skills, "{override}/skills"},
			{"omp follows PI_CONFIG_DIR", map[string]string{"PI_CONFIG_DIR": "alt"}, agent.OmpID, kind.Skills, "{home}/alt/agent/skills"},
		}

		for _, tc := range cases {
			Convey("When "+tc.name, func() {
				clearHostEnv(t)

				for name, value := range tc.env {
					t.Setenv(name, expand(value, home, xdg, override))
				}

				surface := allAgentsFor(t, home, cwd)[tc.id].Surface(tc.kind)

				Convey("Then the write target follows the environment", func() {
					So(surface, ShouldNotBeNil)
					So(surface.Path(), ShouldEqual, expand(tc.expect, home, xdg, override))
				})
			})
		}
	})
}

// TestHostPathMatrixGOOS pins the one honest GOOS statement available: the
// resolver's Env.GOOS is a dead field (nothing reads it) and its only
// GOOS-sensitive entry point is ManagedPolicy, which no adapter uses — so every
// root above must be identical for both operating systems. A future adapter
// that does consult the environment per OS would break this, deliberately.
func TestHostPathMatrixGOOS(t *testing.T) {
	Convey("Given a home", t, func() {
		home := t.TempDir()
		cwd := t.TempDir()
		clearHostEnv(t)

		Convey("Then the write targets do not depend on the operating system", func() {
			for _, row := range hostPathMatrix {
				a := row.build(home, cwd)

				for _, k := range sortedKinds(row.paths) {
					So(a.Surface(k).Path(), ShouldEqual, filepath.Join(home, filepath.FromSlash(row.paths[k])))
				}
			}
		})

		Convey("And the suite runs on both", func() {
			So([]string{"darwin", "linux"}, ShouldContain, runtime.GOOS)
		})
	})
}

// sortedKinds lists the kinds a row covers in a stable order, so a failure names
// one kind at a time.
func sortedKinds(paths map[kind.ID]string) []kind.ID {
	out := make([]kind.ID, 0, len(paths))
	for k := range paths {
		out = append(out, k)
	}

	slices.Sort(out)

	return out
}

// substitute is strings.ReplaceAll under a name this file owns.
func substitute(value, old, replacement string) string {
	return strings.ReplaceAll(value, old, replacement)
}

// clearHostEnv empties every variable the resolver reads, so a row is a function
// of the fixture and not of the machine running the suite.
func clearHostEnv(t *testing.T) {
	t.Helper()

	for _, key := range envKeys {
		t.Setenv(key, "")
	}
}

// allAgentsFor builds the registry once and indexes it by id.
func allAgentsFor(t *testing.T, home, cwd string) map[string]*agent.Agent {
	t.Helper()

	out := map[string]*agent.Agent{}
	for _, a := range agent.All(home, cwd) {
		out[a.ID] = a
	}

	return out
}

// expand substitutes the fixture directories into an expectation.
func expand(value, home, xdg, override string) string {
	return substitute(substitute(substitute(value, "{home}", home), "{xdg}", xdg), "{override}", override)
}
