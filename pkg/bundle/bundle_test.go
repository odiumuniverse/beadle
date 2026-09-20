package bundle_test

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/hooks"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

var versionPattern = regexp.MustCompile(`^0\.0\.0-[0-9a-f]{12}$`)

func fixtureRequest(host bundle.Host) bundle.Request {
	return bundle.Request{
		Host: host,
		Skills: map[string]map[string][]byte{
			"alpha": {"SKILL.md": []byte("# alpha\n"), "docs/note.txt": []byte("note\n")},
		},
		Servers: kind.Items{
			"plug": mcp.Encode(mcp.Server{Command: []string{"node", "srv.js"}}),
			"web":  mcp.Encode(mcp.Server{Transport: mcp.TransportHTTP, URL: "https://example.com/mcp"}),
		},
		Hooks: map[string]hooks.Hook{
			"notify": {Event: "session-start", Command: "echo hi", Timeout: 5},
			"lint":   {Event: "post-tool", Matcher: "Bash", Command: "make lint"},
			"secret": {Event: "stop", Command: "rm -rf /"},
		},
		Approved: map[string]bool{"notify": true, "lint": true},
	}
}

func fileKeys(files map[string][]byte) []string {
	keys := slices.Collect(maps.Keys(files))
	slices.Sort(keys)

	return keys
}

func TestBundleParseHost(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]bundle.Host{
		"claude":          bundle.Claude,
		"claude-code":     bundle.Claude,
		"gemini":          bundle.Gemini,
		"gemini-cli":      bundle.Gemini,
		"antigravity":     bundle.Antigravity,
		"antigravity-cli": bundle.Antigravity,
	} {
		got, err := bundle.ParseHost(name)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}

	_, err := bundle.ParseHost("cursor")
	require.ErrorContains(t, err, "unknown bundle host")
}

func TestBundlePlanClaudeGolden(t *testing.T) {
	t.Parallel()

	result, files, err := bundle.Plan(fixtureRequest(bundle.Claude))
	require.NoError(t, err)
	require.Empty(t, result.Warnings)
	require.Regexp(t, versionPattern, result.Version)

	require.Equal(t, []string{
		".claude-plugin/marketplace.json",
		"plugins/beadle-canon/.claude-plugin/plugin.json",
		"plugins/beadle-canon/.mcp.json",
		"plugins/beadle-canon/hooks/hooks.json",
		"plugins/beadle-canon/skills/alpha/SKILL.md",
		"plugins/beadle-canon/skills/alpha/docs/note.txt",
	}, fileKeys(files))

	require.JSONEq(t, `{
		"name": "beadle",
		"owner": {"name": "beadle"},
		"description": "beadle vault canon: skills, MCP servers and approved hooks",
		"plugins": [{"name": "beadle-canon", "source": "./plugins/beadle-canon", "version": "`+result.Version+`"}]
	}`, string(files[".claude-plugin/marketplace.json"]))

	require.JSONEq(t, `{
		"name": "beadle-canon",
		"version": "`+result.Version+`",
		"description": "beadle vault canon: skills, MCP servers and approved hooks",
		"author": {"name": "beadle"}
	}`, string(files["plugins/beadle-canon/.claude-plugin/plugin.json"]))

	require.JSONEq(t, `{
		"mcpServers": {
			"plug": {"type": "stdio", "command": "node", "args": ["srv.js"]},
			"web": {"type": "http", "url": "https://example.com/mcp"}
		}
	}`, string(files["plugins/beadle-canon/.mcp.json"]))

	require.JSONEq(t, `{
		"SessionStart": [{"matcher": "*", "hooks": [{"type": "command", "command": "echo hi", "timeout": 5}]}],
		"PostToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "make lint"}]}]
	}`, string(files["plugins/beadle-canon/hooks/hooks.json"]))

	require.Equal(t, "# alpha\n", string(files["plugins/beadle-canon/skills/alpha/SKILL.md"]))
	require.NotContains(t, string(files["plugins/beadle-canon/hooks/hooks.json"]), "rm -rf")
}

func TestBundlePlanGeminiFiles(t *testing.T) {
	t.Parallel()

	req := fixtureRequest(bundle.Gemini)
	req.Hooks["stopper"] = hooks.Hook{Event: "stop", Command: "true"}
	req.Approved["stopper"] = true

	result, files, err := bundle.Plan(req)
	require.NoError(t, err)
	require.Regexp(t, versionPattern, result.Version)

	require.Len(t, result.Warnings, 1)
	require.Contains(t, result.Warnings[0], "hook stopper (stop): no gemini event; not rendered")

	require.Equal(t, []string{"gemini-extension.json", "hooks/hooks.json"}, fileKeys(files))

	require.JSONEq(t, `{
		"name": "beadle-canon",
		"version": "`+result.Version+`",
		"description": "beadle vault canon: MCP servers and approved hooks",
		"mcpServers": {
			"plug": {"command": "node", "args": ["srv.js"]},
			"web": {"httpUrl": "https://example.com/mcp"}
		}
	}`, string(files["gemini-extension.json"]))

	require.JSONEq(t, `{
		"SessionStart": [{"matcher": "*", "hooks": [{"name": "notify", "type": "command", "command": "echo hi", "timeout": 5000}]}],
		"AfterTool": [{"matcher": "Bash", "hooks": [{"name": "lint", "type": "command", "command": "make lint"}]}]
	}`, string(files["hooks/hooks.json"]))
}

func TestBundlePlanAntigravityGolden(t *testing.T) {
	t.Parallel()

	req := fixtureRequest(bundle.Antigravity)
	req.Hooks["greet"] = hooks.Hook{Event: "session-start", Matcher: "Bash", Command: "echo start"}
	req.Approved["greet"] = true
	req.Hooks["alert"] = hooks.Hook{Event: "notification", Command: "true"}
	req.Approved["alert"] = true

	result, files, err := bundle.Plan(req)
	require.NoError(t, err)
	require.Regexp(t, versionPattern, result.Version)
	require.Len(t, result.Warnings, 2)
	require.Contains(t, result.Warnings[0], "hook alert (notification): no antigravity event; not rendered")
	require.Contains(t, result.Warnings[1], "antigravity ignores matchers on PreInvocation; matcher dropped")

	require.Equal(t, []string{
		"hooks.json",
		"mcp_config.json",
		"plugin.json",
		"skills/alpha/SKILL.md",
		"skills/alpha/docs/note.txt",
	}, fileKeys(files))

	require.JSONEq(t, `{"name": "beadle-canon", "version": "`+result.Version+`"}`, string(files["plugin.json"]))

	require.JSONEq(t, `{
		"mcpServers": {
			"plug": {"command": "node", "args": ["srv.js"]},
			"web": {"serverUrl": "https://example.com/mcp"}
		}
	}`, string(files["mcp_config.json"]))

	require.JSONEq(t, `{
		"beadle-canon": {
			"PreInvocation": [
				{"type": "command", "command": "echo start"},
				{"type": "command", "command": "echo hi", "timeout": 5}
			],
			"PostToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "make lint"}]}]
		}
	}`, string(files["hooks.json"]))
}

func TestBundleRenderDeterministic(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	first, err := bundle.Render(root, fixtureRequest(bundle.Claude))
	require.NoError(t, err)
	require.True(t, first.Changed)
	require.Regexp(t, versionPattern, first.Version)

	before := treeSnapshot(t, filepath.Join(root, "claude"))

	second, err := bundle.Render(root, fixtureRequest(bundle.Claude))
	require.NoError(t, err)
	require.False(t, second.Changed, "an unchanged canon must not rewrite the bundle")
	require.Equal(t, first.Version, second.Version)
	require.Equal(t, before, treeSnapshot(t, filepath.Join(root, "claude")))
}

func TestBundleRenderCanonChange(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	first, err := bundle.Render(root, fixtureRequest(bundle.Claude))
	require.NoError(t, err)

	stale := filepath.Join(root, "claude", "plugins", "beadle-canon", "skills", "stale.md")
	require.NoError(t, os.WriteFile(stale, []byte("stale\n"), 0o600))

	req := fixtureRequest(bundle.Claude)
	req.Skills["alpha"]["SKILL.md"] = []byte("# alpha v2\n")

	second, err := bundle.Render(root, req)
	require.NoError(t, err)
	require.True(t, second.Changed)
	require.NotEqual(t, first.Version, second.Version)

	require.Equal(t, "# alpha v2\n", string(readFile(t, filepath.Join(root, "claude", "plugins", "beadle-canon", "skills", "alpha", "SKILL.md"))))
	require.NoFileExists(t, stale, "stale files of older bundle content must be removed")
}

func TestBundleVersionDependsOnApprovalAndHost(t *testing.T) {
	t.Parallel()

	req := fixtureRequest(bundle.Claude)
	base, _, err := bundle.Plan(req)
	require.NoError(t, err)

	req.Approved["secret"] = true

	approved, _, err := bundle.Plan(req)
	require.NoError(t, err)
	require.NotEqual(t, base.Version, approved.Version)

	gemini, _, err := bundle.Plan(fixtureRequest(bundle.Gemini))
	require.NoError(t, err)
	require.NotEqual(t, base.Version, gemini.Version)
}

func TestBundlePlanRejectsUnknownAgentDialect(t *testing.T) {
	t.Parallel()

	_, _, err := bundle.Plan(bundle.Request{Host: bundle.Host("cursor")})
	require.ErrorContains(t, err, "unsupported bundle host")
}

func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()

	out := map[string]string{}

	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		data, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp tree
		if err != nil {
			return err
		}

		out[filepath.ToSlash(rel)] = string(data)

		return nil
	}))

	return out
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
	require.NoError(t, err)

	return data
}

func TestBundleGoldenJSONIsValidJSON(t *testing.T) {
	t.Parallel()

	for _, host := range bundle.Hosts() {
		_, files, err := bundle.Plan(fixtureRequest(host))
		require.NoError(t, err)

		for rel, data := range files {
			if strings.HasSuffix(rel, ".json") {
				require.True(t, json.Valid(data), "%s/%s must be valid JSON", host, rel)
			}
		}
	}
}
