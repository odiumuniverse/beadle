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

// ompConfiguredFixture enables exactly omp and plants the agent config.yml a
// real install has, so the host is detected by its config and the test never
// depends on an omp binary on the machine's PATH.
func ompConfiguredFixture(t *testing.T) *fixture {
	t.Helper()

	f := ompFixture(t)
	write(t, filepath.Join(ompAgentRoot(f.home), "config.yml"), "setupVersion: 2\n")

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
				So(hasIssue(issues, engine.SeverityInfo, "plugin source claude: not installed"), ShouldBeTrue)
			})
		})
	})
}

// ompHostState mimics the plugin state `omp plugin` keeps, and applies the
// effect of each command the engine runs, so the guard's re-read is exercised
// the way the real host behaves.
type ompHostState struct {
	Marketplace bool
	// SourceURI is the sourceUri omp recorded for the marketplace: the
	// directory `marketplace add` was given. It stays empty for a state the
	// fixture never created through the CLI.
	SourceURI string
	Installed bool
	Version   string
}

func (h *ompHostState) writeState(t *testing.T, home string) {
	t.Helper()

	root := ompHomeDir(home)

	if h.Marketplace {
		write(t, filepath.Join(root, "marketplaces.json"),
			fmt.Sprintf(`{"version":1,"marketplaces":[{"name":"beadle","sourceType":"local","sourceUri":%q,`+
				`"catalogPath":"p/plugins/cache/marketplaces/beadle/marketplace.json"}]}`, h.SourceURI))
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
		h.SourceURI = ompBundleDir(f)
	case "plugin marketplace update beadle":
		h.Marketplace = true
	case "plugin install beadle-canon@beadle", "plugin install --force beadle-canon@beadle":
		h.Installed = true
		h.Version = ompRenderedVersion(t, f)
	case "plugin uninstall beadle-canon@beadle":
		h.Installed = false
	case "plugin marketplace remove beadle":
		h.Marketplace = false
		h.SourceURI = ""
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

		host := &ompHostState{Marketplace: true, SourceURI: ompBundleDir(f)}
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

		f := ompConfiguredFixture(t)
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
