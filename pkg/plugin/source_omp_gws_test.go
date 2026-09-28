package plugin_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/plugin"
)

// ompPluginsDir is the user plugin root of omp.
func ompPluginsDir(home string) string {
	return filepath.Join(home, ".omp", "plugins")
}

// ompCacheDir is the cached install directory of one marketplace plugin; omp
// names it <marketplace>___<plugin>___<version>.
func ompCacheDir(home, marketplace, name, version string) string {
	return filepath.Join(ompPluginsDir(home), "cache", "plugins", marketplace+"___"+name+"___"+version)
}

// writeOMPRegistry writes omp's installed_plugins.json with one plugin.
func writeOMPRegistry(t *testing.T, home, name, marketplace, installPath, version string) {
	t.Helper()

	writeFile(t, filepath.Join(ompPluginsDir(home), "installed_plugins.json"),
		fmt.Sprintf(`{"version":2,"plugins":{%q:[{"scope":"user","installPath":%q,"version":%q}]}}`,
			name+"@"+marketplace, installPath, version))
}

// writeOMPSymlink links a node_modules entry the way `omp plugin link` does.
func writeOMPSymlink(t *testing.T, home, name, target string) {
	t.Helper()

	dir := filepath.Join(ompPluginsDir(home), "node_modules")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}

	if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
		t.Fatalf("symlink %s: %v", name, err)
	}
}

func TestReadAllOMPMarketplacePlugin(t *testing.T) {
	Convey("Given an omp marketplace install", t, func() {
		t.Setenv("PI_CONFIG_DIR", "")

		home := t.TempDir()
		dir := ompCacheDir(home, "acme", "tool", "1.2.0")

		writeFile(t, filepath.Join(dir, ".claude-plugin", "plugin.json"),
			`{"name":"tool","version":"1.2.0","description":"omp plugin"}`)
		writeFile(t, filepath.Join(dir, "skills", "alpha", "SKILL.md"), "# alpha\n")
		writeFile(t, filepath.Join(dir, "agents", "helper.md"), "# helper\n")
		writeFile(t, filepath.Join(dir, "commands", "dev.md"), "# dev\n")
		writeFile(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers":{"fetch":{}}}`)

		writeOMPRegistry(t, home, "tool", "acme", dir, "1.2.0")

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the plugin is reported from omp's own registry", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldHaveLength, 1)

				p := manifest.Plugins[0]
				So(p.Source, ShouldEqual, plugin.SourceOMP)
				So(p.Origin, ShouldEqual, "acme")
				So(p.Name, ShouldEqual, "tool")
				So(p.Version, ShouldEqual, "1.2.0")
				So(p.Description, ShouldEqual, "omp plugin")
				So(p.Scope, ShouldEqual, "user")
				So(p.InstallPath, ShouldEqual, dir)
				So(p.Skills, ShouldResemble, []string{"alpha"})
				So(p.Agents, ShouldResemble, []string{"helper"})
				So(p.Commands, ShouldResemble, []string{"dev"})
				So(p.Hooks, ShouldBeEmpty)
				So(p.MCPServers, ShouldResemble, []string{"fetch"})
				So(manifest.Warnings, ShouldBeEmpty)
			})
		})
	})
}

func TestReadAllOMPVersionComesFromTheClaudeManifest(t *testing.T) {
	Convey("Given an omp plugin whose version lives only in the .omp-plugin manifest", t, func() {
		t.Setenv("PI_CONFIG_DIR", "")

		home := t.TempDir()
		dir := ompCacheDir(home, "acme", "tool", "0.0.0")

		writeFile(t, filepath.Join(dir, ".omp-plugin", "plugin.json"), `{"name":"tool","version":"9.9.9"}`)
		writeOMPRegistry(t, home, "tool", "acme", dir, "0.0.0")

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the recorded version is reported, not the one omp ignores", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldHaveLength, 1)
				So(manifest.Plugins[0].Version, ShouldEqual, "0.0.0")
			})
		})
	})
}

func TestReadAllOMPLinkedPlugin(t *testing.T) {
	Convey("Given an omp plugin linked from a source directory", t, func() {
		t.Setenv("PI_CONFIG_DIR", "")

		home := t.TempDir()
		source := filepath.Join(t.TempDir(), "tool")

		writeFile(t, filepath.Join(source, "package.json"),
			`{"name":"tool","version":"2.0.0","description":"linked plugin","omp":{"extensions":[]}}`)
		writeFile(t, filepath.Join(source, "skills", "alpha", "SKILL.md"), "# alpha\n")

		writeOMPSymlink(t, home, "tool", source)

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the linked package is reported under the host namespace", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldHaveLength, 1)

				p := manifest.Plugins[0]
				So(p.Source, ShouldEqual, plugin.SourceOMP)
				So(p.Origin, ShouldEqual, plugin.SourceOMP)
				So(p.Name, ShouldEqual, "tool")
				So(p.Version, ShouldEqual, "2.0.0")
				So(p.Skills, ShouldResemble, []string{"alpha"})
				So(p.InstallPath, ShouldEqual, filepath.Join(ompPluginsDir(home), "node_modules", "tool"))
			})
		})
	})
}

func TestReadAllOMPRegistryWinsOverNodeModules(t *testing.T) {
	Convey("Given a marketplace install that also has a node_modules link", t, func() {
		t.Setenv("PI_CONFIG_DIR", "")

		home := t.TempDir()
		dir := ompCacheDir(home, "acme", "tool", "1.2.0")

		writeFile(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{"name":"tool","version":"1.2.0"}`)
		writeOMPRegistry(t, home, "tool", "acme", dir, "1.2.0")
		writeOMPSymlink(t, home, "tool", dir)

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the plugin is reported once, from the registry", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldHaveLength, 1)
				So(manifest.Plugins[0].Origin, ShouldEqual, "acme")
				So(manifest.Plugins[0].InstallPath, ShouldEqual, dir)
			})
		})
	})
}

func TestReadAllOMPMissingInstallPathWarns(t *testing.T) {
	Convey("Given an omp registry entry whose install directory is gone", t, func() {
		t.Setenv("PI_CONFIG_DIR", "")

		home := t.TempDir()
		dir := ompCacheDir(home, "acme", "tool", "1.2.0")

		writeOMPRegistry(t, home, "tool", "acme", dir, "1.2.0")

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the plugin is skipped with a warning", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldBeEmpty)
				So(manifest.Warnings, ShouldHaveLength, 1)
				So(manifest.Warnings[0], ShouldContainSubstring, "points to a missing directory")
			})
		})
	})
}

func TestReadAllOMPHooksAreReportedNotParsed(t *testing.T) {
	Convey("Given an omp plugin that ships hook code modules", t, func() {
		t.Setenv("PI_CONFIG_DIR", "")

		home := t.TempDir()
		dir := ompCacheDir(home, "acme", "tool", "1.2.0")

		writeFile(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{"name":"tool","version":"1.2.0"}`)
		writeFile(t, filepath.Join(dir, "hooks", "pre", "guard.ts"), "export default () => {}\n")
		writeFile(t, filepath.Join(dir, "hooks", "hooks.json"), `{"hooks":{"PreToolUse":[]}}`)
		writeOMPRegistry(t, home, "tool", "acme", dir, "1.2.0")

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the modules are named and the Claude hook file is not read", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldHaveLength, 1)
				So(manifest.Plugins[0].Hooks, ShouldBeEmpty)
				So(manifest.Warnings, ShouldHaveLength, 1)
				So(manifest.Warnings[0], ShouldContainSubstring, "code modules, not a command-hook file")
			})
		})
	})
}

func TestReadAllOMPRelocatesWithConfigDir(t *testing.T) {
	Convey("Given PI_CONFIG_DIR relocates the omp root", t, func() {
		t.Setenv("PI_CONFIG_DIR", "relocated")

		home := t.TempDir()
		dir := filepath.Join(home, "relocated", "plugins", "cache", "plugins", "acme___tool___1.0.0")

		writeFile(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{"name":"tool","version":"1.0.0"}`)
		writeFile(t, filepath.Join(home, "relocated", "plugins", "installed_plugins.json"),
			fmt.Sprintf(`{"version":2,"plugins":{"tool@acme":[{"scope":"user","installPath":%q,"version":"1.0.0"}]}}`, dir))

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(home)

			Convey("Then the relocated root is read", func() {
				So(err, ShouldBeNil)
				So(plugin.OmpRoot(home), ShouldEqual, filepath.Join(home, "relocated"))
				So(manifest.Plugins, ShouldHaveLength, 1)
				So(manifest.Plugins[0].InstallPath, ShouldEqual, dir)
			})
		})
	})
}

func TestReadAllOMPSilentWithoutPluginRoot(t *testing.T) {
	Convey("Given a home without an omp plugin root", t, func() {
		t.Setenv("PI_CONFIG_DIR", "")

		Convey("When every host is read", func() {
			manifest, err := plugin.ReadAll(t.TempDir())

			Convey("Then no omp plugin and no warning appears", func() {
				So(err, ShouldBeNil)
				So(manifest.Plugins, ShouldBeEmpty)
				So(manifest.Warnings, ShouldBeEmpty)
			})
		})
	})
}
