package engine_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/plugin"
	"github.com/odiumuniverse/beadle/pkg/state"
)

func ompHomeDir(home string) string   { return filepath.Join(home, ".omp") }
func ompAgentRoot(home string) string { return filepath.Join(ompHomeDir(home), "agent") }

func ompSkillsDir(home string) string { return filepath.Join(ompAgentRoot(home), "skills") }

// ompFixture enables exactly omp, so a farm assertion cannot be satisfied by
// another host's surface. PI_CONFIG_DIR is pinned by the caller.
func ompFixture(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t)
	f.config.Disable(agent.ClaudeCodeID)
	f.config.Disable(agent.OpenCodeID)
	f.config.Enable(agent.OmpID)

	if err := f.config.Save(f.vault.ConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	return f
}

func writeOMPPluginsRegistry(t *testing.T, home, key, installPath, version string) {
	t.Helper()

	write(t, filepath.Join(ompHomeDir(home), "plugins", "installed_plugins.json"),
		fmt.Sprintf(`{"version":2,"plugins":{%q:[{"scope":"user","installPath":%q,"version":%q}]}}`, key, installPath, version))
}

// ompPluginTree plants a marketplace install the way omp lays it out:
// plugins/cache/plugins/<marketplace>___<plugin>___<version>, recorded in
// plugins/installed_plugins.json.
func ompPluginTree(t *testing.T, home, marketplace, name, version string) string {
	t.Helper()

	dir := filepath.Join(ompHomeDir(home), "plugins", "cache", "plugins", marketplace+"___"+name+"___"+version)
	write(t, filepath.Join(dir, ".claude-plugin", "plugin.json"),
		fmt.Sprintf(`{"name":%q,"version":%q,"description":"omp plugin"}`, name, version))
	writeOMPPluginsRegistry(t, home, name+"@"+marketplace, dir, version)

	return dir
}

// walkTree records every path under root with its bytes, so a test can prove a
// tree was not written to.
func walkTree(t *testing.T, root string) map[string]string {
	t.Helper()

	out := map[string]string{}

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			out[path] = "<dir>"

			return nil
		}

		data, err := os.ReadFile(path) //nolint:gosec // G304: the test walks its own temp tree
		if err != nil {
			return err
		}

		out[path] = string(data)

		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("walk %s: %v", root, err)
	}

	return out
}

// readSymlinkTarget reports the symlink target at path, or false when there is none.
func readSymlinkTarget(t *testing.T, path string) (string, bool) {
	t.Helper()

	link, err := os.Readlink(path)
	if err != nil {
		return "", false
	}

	return link, true
}

func TestOmpPluginSourceParksAndFarms(t *testing.T) {
	Convey("Given an omp marketplace plugin", t, func() {
		t.Setenv("PI_CONFIG_DIR", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		enableAgents(t, f, agent.OmpID)

		// No host CLI: the unattended bundle attempt stays unregistered and
		// cannot flip the omp file modes the assertions below rely on.
		missingCLI(t)

		dir := ompPluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, dir, "alpha", "# alpha\n")
		writingAgent(t, dir, "helper", "---\nname: helper\ndescription: helps\n---\n\nHelp.\n")
		writeCommand(t, dir, "deploy", farmCommandDoc)

		before := walkTree(t, filepath.Join(ompHomeDir(f.home), "plugins"))

		report := f.sync(t)

		Convey("When the sync runs", func() {
			Convey("Then it is parked, the install tree is untouched, and the artifacts go to every host but omp", func() {
				result := pluginResult(t, report, "acme/tool")
				So(result.Action, ShouldEqual, engine.PluginCreated)
				So(result.Target, ShouldEqual, dir)

				So(pivotLink(t, f, "acme", "tool"), ShouldEqual, dir)

				// omp reads its own plugin cache natively (agent, command and
				// skill discovery all read ~/.omp/plugins), so presenting the
				// same artifacts back into ~/.omp/agent would duplicate them;
				// the other hosts still receive them.
				want := filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current", "skills", "alpha")
				So(farmLink(t, claudeSkillsDir(f.home), "alpha"), ShouldEqual, want)
				So(farmLink(t, filepath.Join(f.home, ".claude", "agents"), "tool--helper.md"),
					ShouldEqual, pluginFarmLink(f, "acme", "tool", "agents", "helper.md"))
				So(farmLink(t, filepath.Join(f.home, ".claude", "commands"), "tool--deploy.md"),
					ShouldEqual, pluginFarmLink(f, "acme", "tool", "commands", "deploy.md"))

				for _, path := range []string{
					filepath.Join(ompSkillsDir(f.home), "alpha"),
					filepath.Join(ompAgentRoot(f.home), "agents", "tool--helper.md"),
					filepath.Join(ompAgentRoot(f.home), "commands", "tool--deploy.md"),
				} {
					_, err := os.Lstat(path)
					So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)
				}

				So(walkTree(t, filepath.Join(ompHomeDir(f.home), "plugins")), ShouldResemble, before)
			})
		})
	})
}

func TestOmpPluginSourceFollowsConfigDir(t *testing.T) {
	Convey("Given PI_CONFIG_DIR relocates the omp root", t, func() {
		t.Setenv("PI_CONFIG_DIR", "relocated")

		f := ompFixture(t)
		f.emptyConfigs(t)
		missingCLI(t)

		dir := filepath.Join(f.home, "relocated", "plugins", "cache", "plugins", "acme___tool___1.0.0")
		write(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{"name":"tool","version":"1.0.0"}`)
		writeSkill(t, dir, "alpha", "# alpha\n")
		write(t, filepath.Join(f.home, "relocated", "plugins", "installed_plugins.json"),
			fmt.Sprintf(`{"version":2,"plugins":{"tool@acme":[{"scope":"user","installPath":%q,"version":"1.0.0"}]}}`, dir))

		report := f.sync(t)

		Convey("When the sync runs", func() {
			Convey("Then the relocated root is read and matches the agent's own resolution", func() {
				root, _ := agent.OmpHome(f.home)
				So(plugin.OmpRoot(f.home), ShouldEqual, root)

				result := pluginResult(t, report, "acme/tool")
				So(result.Action, ShouldEqual, engine.PluginCreated)
				So(result.Target, ShouldEqual, dir)
			})
		})
	})
}

func TestOmpDefaultRootMatchesAgentHome(t *testing.T) {
	Convey("Given no PI_CONFIG_DIR", t, func() {
		t.Setenv("PI_CONFIG_DIR", "")

		Convey("Then the plugin layer and the agent layer resolve one root", func() {
			home := t.TempDir()

			root, _ := agent.OmpHome(home)
			So(plugin.OmpRoot(home), ShouldEqual, root)
		})
	})
}

func TestOmpLinkedPluginOutsideTheCacheIsReportedNotFarmed(t *testing.T) {
	Convey("Given an omp plugin linked from a directory outside the omp tree", t, func() {
		t.Setenv("PI_CONFIG_DIR", "")

		f := ompFixture(t)
		f.emptyConfigs(t)
		missingCLI(t)

		source := filepath.Join(t.TempDir(), "tool")
		write(t, filepath.Join(source, "package.json"), `{"name":"tool","version":"2.0.0"}`)
		writeSkill(t, source, "alpha", "# alpha\n")

		nodeModules := filepath.Join(ompHomeDir(f.home), "plugins", "node_modules")
		So(os.MkdirAll(nodeModules, 0o750), ShouldBeNil)
		So(os.Symlink(source, filepath.Join(nodeModules, "tool")), ShouldBeNil)

		report := f.sync(t)

		Convey("When the sync runs", func() {
			Convey("Then the plugin is named but its outside path is refused", func() {
				So(pluginResult(t, report, "omp/tool").Action, ShouldEqual, engine.PluginCreated)
				So(containsWarning(report.Warnings, "outside the plugin cache"), ShouldBeTrue)

				_, ok := readSymlinkTarget(t, filepath.Join(ompSkillsDir(f.home), "alpha"))
				So(ok, ShouldBeFalse)
			})
		})
	})
}

func TestOmpPluginSourceInDoctor(t *testing.T) {
	Convey("Given an omp plugin and no Claude registry", t, func() {
		t.Setenv("PI_CONFIG_DIR", "")

		f := ompFixture(t)
		missingCLI(t)

		dir := ompPluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, dir, "alpha", "# alpha\n")

		issues, err := f.engine.Doctor(t.Context())

		Convey("When doctor runs", func() {
			Convey("Then omp is reported as a plugin source with its count", func() {
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityInfo, "plugin source omp: 1 plugin(s) found"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityInfo, "plugin source claude-code: not installed"), ShouldBeTrue)
			})
		})
	})
}

// ompHostState mimics the plugin state `omp plugin` keeps, and applies the
// effect of each command the engine runs, so the guard's re-read is exercised
// the way the real host behaves.
type ompHostState struct {
	Marketplace bool
	Installed   bool
	Version     string
}

func (h *ompHostState) writeState(t *testing.T, home string) {
	t.Helper()

	root := ompHomeDir(home)

	if h.Marketplace {
		write(t, filepath.Join(root, "marketplaces.json"),
			`{"version":1,"marketplaces":[{"name":"beadle","sourceType":"local","sourceUri":"/tmp/beadle"}]}`)
	} else {
		_ = os.Remove(filepath.Join(root, "marketplaces.json"))
	}

	if h.Installed {
		write(t, filepath.Join(root, "plugins", "installed_plugins.json"),
			fmt.Sprintf(`{"version":2,"plugins":{"beadle-canon@beadle":[{"scope":"user","installPath":"p","version":%q}]}}`, h.Version))
	} else {
		_ = os.Remove(filepath.Join(root, "plugins", "installed_plugins.json"))
	}
}

func (h *ompHostState) apply(t *testing.T, f *fixture, name string, args []string) {
	t.Helper()

	if name != "omp" {
		return
	}

	switch strings.Join(args, " ") {
	case "plugin marketplace add " + ompBundleDir(f):
		h.Marketplace = true
	case "plugin marketplace update beadle":
		h.Marketplace = true
	case "plugin install beadle-canon@beadle", "plugin install --force beadle-canon@beadle":
		h.Installed = true
		h.Version = ompRenderedVersion(t, f)
	case "plugin uninstall beadle-canon@beadle":
		h.Installed = false
	case "plugin marketplace remove beadle":
		h.Marketplace = false
	}

	h.writeState(t, f.home)
}

func (h *ompHostState) respond(name string, args []string) ([]byte, bool) {
	if name != "omp" || len(args) != 3 || args[0] != "plugin" || args[1] != "list" || args[2] != "--json" {
		return nil, false
	}

	entries := []map[string]any{}
	if h.Installed {
		entries = append(entries, map[string]any{"scope": "user", "installPath": "p", "version": h.Version})
	}

	data, err := json.Marshal(map[string]any{
		"npm": []any{},
		"marketplace": []map[string]any{
			{"id": "beadle-canon@beadle", "scope": "user", "entries": entries},
		},
	})
	if err != nil {
		return nil, false
	}

	return data, true
}

func ompBundleDir(f *fixture) string { return filepath.Join(f.vault.BundlesDir(), "omp") }

func ompRenderedVersion(t *testing.T, f *fixture) string {
	t.Helper()

	var doc struct {
		Version string `json:"version"`
	}

	path := filepath.Join(ompBundleDir(f), "plugins", "beadle-canon", ".claude-plugin", "plugin.json")

	So(json.Unmarshal([]byte(read(t, path)), &doc), ShouldBeNil)

	return doc.Version
}

func ompBundleCLI(t *testing.T, f *fixture, state *ompHostState) *fakeCLI {
	t.Helper()

	cli := &fakeCLI{}
	cli.respond = state.respond
	cli.after = func(name string, args []string) { state.apply(t, f, name, args) }

	return cli
}

// ompBundleFixture enables the omp agent on the canon fixture, with the omp
// CLI reachable.
func ompBundleFixture(t *testing.T) *fixture {
	t.Helper()

	t.Setenv("PI_CONFIG_DIR", "")

	f := bundleFixture(t)
	f.config.Enable(agent.OmpID)

	if err := f.config.Save(f.vault.ConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	return f
}

func TestOmpBundleEnableRegistersOnce(t *testing.T) {
	Convey("Given an omp host with no bundle registered", t, func() {
		f := ompBundleFixture(t)

		host := &ompHostState{}
		cli := ompBundleCLI(t, f, host)
		fakeRunner(t, cli)
		foundCLI(t)

		report, err := f.engine.BundlesEnable(t.Context(), "omp")

		Convey("When the bundle is enabled", func() {
			Convey("Then the marketplace is added, the install is forced and the registry confirms it", func() {
				So(err, ShouldBeNil)
				So(cli.calls, ShouldResemble, [][]string{
					{"omp", "plugin", "marketplace", "add", ompBundleDir(f)},
					{"omp", "plugin", "install", "--force", "beadle-canon@beadle"},
					{"omp", "plugin", "list", "--json"},
				})

				So(report.Bundles, ShouldHaveLength, 1)
				So(report.Bundles[0].Action, ShouldEqual, "enabled")
				So(report.Bundles[0].Registered, ShouldBeTrue)
				So(report.Bundles[0].Tier, ShouldEqual, state.VerifyExecuted)
			})

			Convey("And enabling it again is a no-op", func() {
				cli.calls = nil

				second, err := f.engine.BundlesEnable(t.Context(), "omp")

				So(err, ShouldBeNil)
				So(cli.calls, ShouldResemble, [][]string{{"omp", "plugin", "list", "--json"}})
				So(second.Bundles[0].Action, ShouldEqual, "noop")
			})
		})
	})
}

func TestOmpBundleEnableRefreshesAnExistingMarketplace(t *testing.T) {
	Convey("Given omp already knows the beadle marketplace", t, func() {
		f := ompBundleFixture(t)

		host := &ompHostState{Marketplace: true}
		host.writeState(t, f.home)

		cli := ompBundleCLI(t, f, host)

		// The real CLI exits non-zero on a repeated add; the guard must never
		// run it, so a naive implementation fails here.
		cli.failOn = map[string]error{
			"plugin marketplace add " + ompBundleDir(f): errors.New(`Marketplace "beadle" already exists`),
		}

		fakeRunner(t, cli)
		foundCLI(t)

		report, err := f.engine.BundlesEnable(t.Context(), "omp")

		Convey("When the bundle is enabled", func() {
			Convey("Then the catalog is updated instead of re-added and the plugin is force-installed", func() {
				So(err, ShouldBeNil)
				So(cli.calls, ShouldResemble, [][]string{
					{"omp", "plugin", "marketplace", "update", "beadle"},
					{"omp", "plugin", "install", "--force", "beadle-canon@beadle"},
					{"omp", "plugin", "list", "--json"},
				})
				So(report.Bundles[0].Action, ShouldEqual, "enabled")
				So(report.Bundles[0].Registered, ShouldBeTrue)
			})
		})
	})
}

func TestOmpBundleEnableWithoutCLIWritesNoState(t *testing.T) {
	Convey("Given the omp CLI is not reachable", t, func() {
		f := ompBundleFixture(t)
		missingCLI(t)

		report, err := f.engine.BundlesEnable(t.Context(), "omp")

		Convey("When the bundle is enabled", func() {
			Convey("Then it is only generated and omp's state root stays absent", func() {
				So(err, ShouldBeNil)
				So(report.Bundles[0].Action, ShouldEqual, "generated")
				So(report.Bundles[0].Registered, ShouldBeFalse)

				_, statErr := os.Stat(filepath.Join(ompHomeDir(f.home), "plugins"))
				So(errors.Is(statErr, fs.ErrNotExist), ShouldBeTrue)

				_, statErr = os.Stat(filepath.Join(ompHomeDir(f.home), "marketplaces.json"))
				So(errors.Is(statErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestOmpBundleDisableSkipsStepsOmpAlreadyDropped(t *testing.T) {
	Convey("Given an enabled omp bundle that omp no longer records", t, func() {
		f := ompBundleFixture(t)

		host := &ompHostState{}
		cli := ompBundleCLI(t, f, host)
		fakeRunner(t, cli)
		foundCLI(t)

		_, err := f.engine.BundlesEnable(t.Context(), "omp")
		So(err, ShouldBeNil)

		// The user removed the plugin and the marketplace outside beadle.
		host.Marketplace = false
		host.Installed = false
		host.writeState(t, f.home)

		cli.calls = nil

		report, err := f.engine.BundlesDisable(t.Context(), "omp")

		Convey("When the bundle is disabled", func() {
			Convey("Then no uninstall or removal runs and the bundle is gone from state", func() {
				So(err, ShouldBeNil)
				So(cli.calls, ShouldBeEmpty)
				So(report.Bundles, ShouldHaveLength, 1)
				So(report.Bundles[0].Action, ShouldEqual, "disabled")

				st, err := state.Load(f.vault.StatePath())
				So(err, ShouldBeNil)

				_, exists := st.Bundles["omp"]
				So(exists, ShouldBeFalse)
			})
		})
	})
}

func TestOmpBundleDisableUnregisters(t *testing.T) {
	Convey("Given an enabled omp bundle omp still records", t, func() {
		f := ompBundleFixture(t)

		host := &ompHostState{}
		cli := ompBundleCLI(t, f, host)
		fakeRunner(t, cli)
		foundCLI(t)

		_, err := f.engine.BundlesEnable(t.Context(), "omp")
		So(err, ShouldBeNil)

		cli.calls = nil

		report, err := f.engine.BundlesDisable(t.Context(), "omp")

		Convey("When the bundle is disabled", func() {
			Convey("Then the plugin is uninstalled and the marketplace removed", func() {
				So(err, ShouldBeNil)
				So(cli.calls, ShouldResemble, [][]string{
					{"omp", "plugin", "uninstall", "beadle-canon@beadle"},
					{"omp", "plugin", "marketplace", "remove", "beadle"},
				})
				So(report.Bundles[0].Action, ShouldEqual, "disabled")
			})
		})
	})
}

func TestOmpFileSurfacesSync(t *testing.T) {
	Convey("Given the canon and an omp agent", t, func() {
		t.Setenv("PI_CONFIG_DIR", "")

		f := ompFixture(t)
		f.emptyConfigs(t)
		missingCLI(t)

		// The MCP file must exist: no host's mcp surface is creatable, so
		// beadle merges into an existing file instead of mounting one.
		write(t, filepath.Join(ompAgentRoot(f.home), "mcp.json"), `{"mcpServers":{}}`)

		write(t, f.vault.RulesPath(), "# vault rules\n")
		write(t, f.vault.ServersPath(), `{"demo":{"transport":"stdio","command":["demo-mcp"]}}`)
		write(t, filepath.Join(f.vault.SubagentsDir(), "helper.md"), "---\nname: helper\ndescription: helps\n---\n\nHelp.\n")
		write(t, filepath.Join(f.vault.CommandsDir(), "deploy.md"), "---\ndescription: deploys\n---\n\nRun $1.\n")
		write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "---\nname: alpha\ndescription: probe\n---\n\nBody.\n")

		f.sync(t)

		Convey("When the sync runs", func() {
			Convey("Then rules, MCP, subagents and commands land in the omp agent dir while skills stay pull", func() {
				root := ompAgentRoot(f.home)

				So(read(t, filepath.Join(root, "AGENTS.md")), ShouldEqual, "# vault rules\n")

				var doc struct {
					MCPServers map[string]any `json:"mcpServers"`
				}

				So(json.Unmarshal([]byte(read(t, filepath.Join(root, "mcp.json"))), &doc), ShouldBeNil)
				So(doc.MCPServers, ShouldContainKey, "demo")
				So(read(t, filepath.Join(root, "agents", "helper.md")), ShouldContainSubstring, "helper")
				So(read(t, filepath.Join(root, "commands", "deploy.md")), ShouldContainSubstring, "Run $1.")

				_, err := os.Lstat(filepath.Join(root, "skills", "alpha"))
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}
