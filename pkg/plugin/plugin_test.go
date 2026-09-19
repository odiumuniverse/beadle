package plugin_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/plugin"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func pluginFixture(t *testing.T) (home, pluginDir, ghostDir, fallbackDir string) {
	t.Helper()

	home = t.TempDir()
	cache := filepath.Join(home, ".claude", "plugins", "cache")

	pluginDir = filepath.Join(cache, "vmkteam", "vmkteam-developer", "1.1.0")
	ghostDir = filepath.Join(cache, "acme", "ghost", "1.0.0")
	fallbackDir = filepath.Join(cache, "vmkteam", "fallback", "2.0.0")

	writeFile(t, filepath.Join(pluginDir, ".claude-plugin", "plugin.json"),
		`{"name":"vmkteam-developer","version":"1.1.0","description":"vmkteam toolkit"}`)
	writeFile(t, filepath.Join(pluginDir, "skills", "alpha", "SKILL.md"), "# alpha\n")
	writeFile(t, filepath.Join(pluginDir, "skills", "beta", "SKILL.md"), "# beta\n")
	writeFile(t, filepath.Join(pluginDir, "skills", "plain", "README.md"), "no skill\n")
	writeFile(t, filepath.Join(pluginDir, "commands", "dev.md"), "dev\n")
	writeFile(t, filepath.Join(pluginDir, "hooks", "hooks.json"), `{"description":"x","hooks":{"Setup":[],"SessionStart":[]}}`)
	writeFile(t, filepath.Join(pluginDir, ".mcp.json"), `{"mcpServers":{"fetch":{},"search":{}},"note":"${CLAUDE_PLUGIN_ROOT}/bin"}`)

	linked := filepath.Join(home, "shared-skill")
	writeFile(t, filepath.Join(linked, "SKILL.md"), "# linked\n")
	require.NoError(t, os.Symlink(linked, filepath.Join(pluginDir, "skills", "delta")))

	writeFile(t, filepath.Join(fallbackDir, ".claude-plugin", "plugin.json"), `{"version":"2.0.0"}`)

	writeFile(t, filepath.Join(cache, "acme", "tool", "1.0.0", ".orphaned_at"), "1789083161350\n")
	writeFile(t, filepath.Join(cache, "acme", "tool", "0.9.0", "plugin.json"), "{}\n")
	writeFile(t, filepath.Join(cache, "acme", "tool", "0.8.0", ".orphaned_at"), "not-a-number\n")

	writeFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), fmt.Sprintf(`{
  "version": 2,
  "plugins": {
    "vmkteam-developer@vmkteam": [
      {
        "scope": "user",
        "installPath": %q,
        "version": "1.1.0",
        "installedAt": "2026-09-15T10:00:00Z",
        "lastUpdated": "2026-09-15T10:00:00Z",
        "gitCommitSha": "abc1234"
      }
    ],
    "fallback@vmkteam": [
      {
        "scope": "user",
        "installPath": %q,
        "gitCommitSha": "beef001"
      }
    ],
    "ghost@acme": [
      {
        "scope": "user",
        "installPath": %q,
        "version": "1.0.0",
        "gitCommitSha": "def5678"
      }
    ]
  }
}`, pluginDir, fallbackDir, ghostDir))

	writeFile(t, filepath.Join(home, ".claude", "plugins", "known_marketplaces.json"), fmt.Sprintf(`{
  "vmkteam": {
    "source": {"source": "github", "repo": "git.example.com/vmkteam/plugins"},
    "installLocation": %q,
    "lastUpdated": "2026-09-15T10:00:00Z",
    "autoUpdate": true
  }
}`, filepath.Join(home, ".claude", "plugins", "marketplaces", "vmkteam")))

	return home, pluginDir, ghostDir, fallbackDir
}

func TestRead(t *testing.T) {
	t.Parallel()

	home, pluginDir, ghostDir, fallbackDir := pluginFixture(t)

	manifest, err := plugin.Read(home)
	require.NoError(t, err)

	require.Equal(t, []plugin.Plugin{
		{
			Name:         "ghost",
			Marketplace:  "acme",
			Version:      "1.0.0",
			Scope:        "user",
			InstallPath:  ghostDir,
			GitCommitSha: "def5678",
		},
		{
			Name:         "fallback",
			Marketplace:  "vmkteam",
			Version:      "2.0.0",
			Scope:        "user",
			InstallPath:  fallbackDir,
			GitCommitSha: "beef001",
		},
		{
			Name:           "vmkteam-developer",
			Marketplace:    "vmkteam",
			Version:        "1.1.0",
			Scope:          "user",
			InstallPath:    pluginDir,
			GitCommitSha:   "abc1234",
			Description:    "vmkteam toolkit",
			Skills:         []string{"alpha", "beta", "delta"},
			Commands:       []string{"dev.md"},
			Hooks:          []string{"SessionStart", "Setup"},
			MCPServers:     []string{"fetch", "search"},
			PluginRootRefs: []string{".mcp.json"},
		},
	}, manifest.Plugins)

	require.Equal(t, []string{
		"installed plugin ghost@acme points to a missing directory: " + ghostDir,
		"cannot parse .orphaned_at in " + filepath.Join(home, ".claude", "plugins", "cache", "acme", "tool", "0.8.0"),
	}, manifest.Warnings)

	require.Equal(t, []plugin.Marketplace{{
		Name:        "vmkteam",
		Repo:        "git.example.com/vmkteam/plugins",
		InstallPath: filepath.Join(home, ".claude", "plugins", "marketplaces", "vmkteam"),
		AutoUpdate:  true,
	}}, manifest.Marketplaces)

	require.Equal(t, []plugin.Orphan{
		{
			Marketplace: "acme",
			Name:        "tool",
			Version:     "0.8.0",
			Path:        filepath.Join(home, ".claude", "plugins", "cache", "acme", "tool", "0.8.0"),
		},
		{
			Marketplace: "acme",
			Name:        "tool",
			Version:     "0.9.0",
			Path:        filepath.Join(home, ".claude", "plugins", "cache", "acme", "tool", "0.9.0"),
		},
		{
			Marketplace: "acme",
			Name:        "tool",
			Version:     "1.0.0",
			Path:        filepath.Join(home, ".claude", "plugins", "cache", "acme", "tool", "1.0.0"),
			OrphanedAt:  time.UnixMilli(1789083161350),
		},
	}, manifest.Orphans)
}

func TestReadMissingRoot(t *testing.T) {
	t.Parallel()

	manifest, err := plugin.Read(t.TempDir())
	require.NoError(t, err)
	require.Empty(t, manifest.Plugins)
	require.Empty(t, manifest.Marketplaces)
	require.Empty(t, manifest.Orphans)
	require.Empty(t, manifest.Warnings)
}

func TestReadMissingRegistries(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude", "plugins"), 0o750))

	manifest, err := plugin.Read(home)
	require.NoError(t, err)
	require.Len(t, manifest.Warnings, 2)
	require.Contains(t, manifest.Warnings[0], "installed_plugins.json not found")
	require.Contains(t, manifest.Warnings[1], "known_marketplaces.json not found")
}

func TestReadMalformedInstalled(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), "{oops")

	_, err := plugin.Read(home)
	require.Error(t, err)
}

func TestReadEmptyHome(t *testing.T) {
	t.Parallel()

	_, err := plugin.Read("")
	require.Error(t, err)
}
