package plugin_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/plugin"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
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

	if err := os.Symlink(linked, filepath.Join(pluginDir, "skills", "delta")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

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
	Convey("Given a plugin cache with installs, a ghost, orphans and a marketplace", t, func() {
		home, pluginDir, ghostDir, fallbackDir := pluginFixture(t)

		Convey("When the manifest is read", func() {
			manifest, err := plugin.Read(home)

			Convey("Then plugins, warnings, marketplaces and orphans match", func() {
				So(err, ShouldBeNil)

				So(manifest.Plugins, ShouldResemble, []plugin.Plugin{
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
				})

				So(manifest.Warnings, ShouldResemble, []string{
					"installed plugin ghost@acme points to a missing directory: " + ghostDir,
					"cannot parse .orphaned_at in " + filepath.Join(home, ".claude", "plugins", "cache", "acme", "tool", "0.8.0"),
				})

				So(manifest.Marketplaces, ShouldResemble, []plugin.Marketplace{{
					Name:        "vmkteam",
					Repo:        "git.example.com/vmkteam/plugins",
					InstallPath: filepath.Join(home, ".claude", "plugins", "marketplaces", "vmkteam"),
					AutoUpdate:  true,
				}})

				So(manifest.Orphans, ShouldResemble, []plugin.Orphan{
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
				})
			})
		})
	})
}

func TestReadMissingRoot(t *testing.T) {
	Convey("Given an empty home", t, func() {
		Convey("When the manifest is read", func() {
			manifest, err := plugin.Read(t.TempDir())

			Convey("Then everything is empty", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldBeEmpty)
				So(manifest.Marketplaces, ShouldBeEmpty)
				So(manifest.Orphans, ShouldBeEmpty)
				So(manifest.Warnings, ShouldBeEmpty)
			})
		})
	})
}

func TestReadMissingRegistries(t *testing.T) {
	Convey("Given a plugins directory without registries", t, func() {
		home := t.TempDir()
		So(os.MkdirAll(filepath.Join(home, ".claude", "plugins"), 0o750), ShouldBeNil)

		Convey("When the manifest is read", func() {
			manifest, err := plugin.Read(home)

			Convey("Then both missing registries are warned about", func() {
				So(err, ShouldBeNil)
				So(manifest.Warnings, ShouldHaveLength, 2)
				So(manifest.Warnings[0], ShouldContainSubstring, "installed_plugins.json not found")
				So(manifest.Warnings[1], ShouldContainSubstring, "known_marketplaces.json not found")
			})
		})
	})
}

func TestReadMalformedInstalled(t *testing.T) {
	Convey("Given a malformed installed_plugins.json", t, func() {
		home := t.TempDir()
		writeFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), "{oops")

		Convey("When the manifest is read", func() {
			_, err := plugin.Read(home)

			Convey("Then it fails", func() {
				So(err, ShouldBeError)
			})
		})
	})
}

func TestReadEmptyHome(t *testing.T) {
	Convey("Given an empty home path", t, func() {
		Convey("When the manifest is read", func() {
			_, err := plugin.Read("")

			Convey("Then it fails", func() {
				So(err, ShouldBeError)
			})
		})
	})
}
