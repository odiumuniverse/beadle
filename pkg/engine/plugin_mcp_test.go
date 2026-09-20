package engine_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

func writeMCPServers(t *testing.T, pluginDir, content string) {
	t.Helper()

	write(t, filepath.Join(pluginDir, ".mcp.json"), content)
}

func pluginPivot(f *fixture, marketplace, name string) string {
	return filepath.Join(f.vault.PluginsDir(), marketplace, name, "current")
}

func hostMCPServers(t *testing.T, path, pointer string) map[string]map[string]any {
	t.Helper()

	var doc map[string]any
	if err := json.Unmarshal([]byte(read(t, path)), &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}

	servers, ok := doc[pointer].(map[string]any)
	if !ok {
		t.Fatalf("no %s in %s", pointer, path)
	}

	out := make(map[string]map[string]any, len(servers))

	for name, entry := range servers {
		value, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("server %s is not an object", name)
		}

		out[name] = value
	}

	return out
}

func ledgerServers(t *testing.T, f *fixture, key string) []string {
	t.Helper()

	var doc struct {
		Plugins map[string]struct {
			Servers []string `json:"servers"`
		} `json:"plugins"`
	}

	if err := json.Unmarshal([]byte(read(t, f.vault.PluginsLedgerPath())), &doc); err != nil {
		t.Fatalf("unmarshal ledger: %v", err)
	}

	rec, ok := doc.Plugins[key]
	if !ok {
		t.Fatalf("ledger must contain %s", key)
	}

	return rec.Servers
}

func warnedAbout(report *engine.Report, substr string) bool {
	return slices.ContainsFunc(report.Kind(kind.MCP).Warnings, func(warning string) bool {
		return strings.Contains(warning, substr)
	})
}

func TestPluginMCPRendersIntoHosts(t *testing.T) {
	Convey("Given a plugin exposing stdio, http and sse servers", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeMCPServers(t, plugin, `{"mcpServers": {
			"plug": {
				"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug",
				"args": ["serve", "${CLAUDE_PLUGIN_ROOT}"],
				"env": {"ROOT": "${CLAUDE_PLUGIN_ROOT}"}
			},
			"web": {"type": "http", "url": "https://example.com/mcp"},
			"events": {"type": "sse", "url": "https://example.com/sse"}
		}}`)

		f.sync(t)

		pivot := pluginPivot(f, "acme", "tool")

		claude := hostMCPServers(t, f.claudeConfig(), "mcpServers")
		env, ok := claude["plug"]["env"].(map[string]any)
		So(ok, ShouldBeTrue)

		openCode := hostMCPServers(t, f.openCodeConfig(), "mcp")

		claudeBefore := read(t, f.claudeConfig())
		openCodeBefore := read(t, f.openCodeConfig())

		report := f.sync(t)

		result, ok := report.Kind(kind.MCP).Agent(agent.ClaudeCodeID)

		Convey("When it renders into both hosts", func() {
			Convey("Then paths resolve per dialect, the second sync is a noop and nothing leaks to the canon", func() {
				So(report, ShouldNotBeNil)
				So(report.Kind(kind.MCP).Warnings, ShouldBeEmpty)

				So(claude["plug"]["command"], ShouldEqual, pivot+"/bin/plug")
				So(claude["plug"]["args"], ShouldResemble, []any{"serve", pivot})
				So(env["ROOT"], ShouldEqual, pivot)

				So(claude["web"]["type"], ShouldEqual, "http")
				So(claude["web"]["url"], ShouldEqual, "https://example.com/mcp")
				So(claude["events"]["type"], ShouldEqual, "sse")

				So(openCode["plug"]["type"], ShouldEqual, "local")
				So(openCode["plug"]["command"], ShouldResemble, []any{pivot + "/bin/plug", "serve", pivot})
				So(openCode["web"]["type"], ShouldEqual, "remote")
				So(openCode["events"]["type"], ShouldEqual, "remote")

				So(ok, ShouldBeTrue)
				So(report.Action(kind.MCP, agent.ClaudeCodeID), ShouldEqual, engine.ActionNoop)
				So(result.Note, ShouldNotContainSubstring, "did not keep")

				So(read(t, f.claudeConfig()), ShouldEqual, claudeBefore)
				So(read(t, f.openCodeConfig()), ShouldEqual, openCodeBefore)

				_, canonErr := os.Stat(f.vault.ServersPath())
				So(errors.Is(canonErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestPluginMCPSurvivesUpgrade(t *testing.T) {
	Convey("Given a plugin that is upgraded", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeMCPServers(t, plugin, `{"mcpServers": {"plug": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug", "args": ["v1"]}}}`)

		f.sync(t)

		pivot := pluginPivot(f, "acme", "tool")

		upgraded := pluginTree(t, f.home, "acme", "tool", "2.0.0")
		writeMCPServers(t, upgraded, `{"mcpServers": {"plug": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug", "args": ["v2"]}}}`)

		f.sync(t)

		claude := hostMCPServers(t, f.claudeConfig(), "mcpServers")

		report := f.sync(t)

		Convey("When the upgraded content resolves", func() {
			Convey("Then the pivot path is stable and the server updates", func() {
				So(hostMCPServers(t, f.claudeConfig(), "mcpServers")["plug"]["command"], ShouldEqual, pivot+"/bin/plug")
				So(ledgerServers(t, f, "acme/tool"), ShouldResemble, []string{"plug"})

				So(claude["plug"]["command"], ShouldEqual, pivot+"/bin/plug")
				So(claude["plug"]["args"], ShouldResemble, []any{"v2"})
				So(read(t, filepath.Join(pivot, ".mcp.json")), ShouldContainSubstring, `"v2"`)
				So(report.Action(kind.MCP, agent.ClaudeCodeID), ShouldEqual, engine.ActionNoop)
			})
		})
	})
}

func TestPluginMCPRefusesUnknownVariable(t *testing.T) {
	Convey("Given a plugin server with an unknown variable", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeMCPServers(t, plugin, `{"mcpServers": {
			"good": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/good"},
			"bad": {"command": "${MY_VAR}"}
		}}`)

		report := f.sync(t)

		claude := hostMCPServers(t, f.claudeConfig(), "mcpServers")

		So(report, ShouldNotBeNil)

		Convey("When it renders", func() {
			Convey("Then the bad server is dropped while the good one stays", func() {
				So(claude, ShouldContainKey, "good")
				So(claude, ShouldNotContainKey, "bad")
				So(warnedAbout(report, "MY_VAR"), ShouldBeTrue)
			})
		})

		Convey("When an upgrade loses the variable too", func() {
			upgraded := pluginTree(t, f.home, "acme", "tool", "2.0.0")
			writeMCPServers(t, upgraded, `{"mcpServers": {"good": {"command": "${MY_VAR}"}}}`)

			f.sync(t)

			claude := hostMCPServers(t, f.claudeConfig(), "mcpServers")

			_, canonErr := os.Stat(f.vault.ServersPath())

			Convey("Then the server is removed and nothing reaches the canon", func() {
				So(claude, ShouldNotContainKey, "good")
				So(errors.Is(canonErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestPluginMCPCanonWins(t *testing.T) {
	Convey("Given a plugin server colliding with the canon", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		canon := mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"canon-cmd"}})
		canonDoc := `{"alpha": ` + string(canon) + `}`
		write(t, f.vault.ServersPath(), canonDoc)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeMCPServers(t, plugin, `{"mcpServers": {"alpha": {"command": "${CLAUDE_PLUGIN_ROOT}/plugin-cmd"}}}`)

		report := f.sync(t)

		claude := hostMCPServers(t, f.claudeConfig(), "mcpServers")

		Convey("When it renders", func() {
			Convey("Then the canon wins and is unchanged", func() {
				So(claude["alpha"]["command"], ShouldEqual, "canon-cmd")
				So(warnedAbout(report, "collides with the vault canon"), ShouldBeTrue)
				So(read(t, f.vault.ServersPath()), ShouldEqual, canonDoc)
			})
		})
	})
}

func TestPluginMCPPluginCollision(t *testing.T) {
	Convey("Given two plugins exposing the same server name", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		first := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeMCPServers(t, first, `{"mcpServers": {"shared": {"command": "first-cmd"}}}`)

		second := pluginTree(t, f.home, "beta", "other", "1.0.0")
		writeMCPServers(t, second, `{"mcpServers": {"shared": {"command": "second-cmd"}}}`)

		report := f.sync(t)

		claude := hostMCPServers(t, f.claudeConfig(), "mcpServers")

		Convey("When it renders", func() {
			Convey("Then the first plugin wins and the collision is warned", func() {
				So(claude["shared"]["command"], ShouldEqual, "first-cmd")
				So(warnedAbout(report, "already provided by acme/tool"), ShouldBeTrue)
			})
		})
	})
}

func TestPluginMCPUserEditNotAdopted(t *testing.T) {
	Convey("Given a user edit of a plugin-owned server", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeMCPServers(t, plugin, `{"mcpServers": {"plug": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug"}}}`)

		f.sync(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"plug": {"type": "stdio", "command": "/tmp/evil"}}}`)

		f.sync(t)

		claude := hostMCPServers(t, f.claudeConfig(), "mcpServers")

		Convey("When it re-renders", func() {
			Convey("Then the plugin server is restored and never adopted", func() {
				So(claude["plug"]["command"], ShouldEqual, pluginPivot(f, "acme", "tool")+"/bin/plug")

				_, canonErr := os.Stat(f.vault.ServersPath())
				So(errors.Is(canonErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestPluginMCPOwnershipMonotonicOnUpgrade(t *testing.T) {
	Convey("Given a plugin whose servers change across upgrades", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeMCPServers(t, plugin, `{"mcpServers": {"plug": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug"}}}`)

		f.sync(t)
		So(ledgerServers(t, f, "acme/tool"), ShouldResemble, []string{"plug"})

		upgraded := pluginTree(t, f.home, "acme", "tool", "2.0.0")
		writeMCPServers(t, upgraded, `{"mcpServers": {"plug2": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug2"}}}`)

		f.sync(t)

		Convey("When the agent tries to re-adopt a retired server", func() {
			So(ledgerServers(t, f, "acme/tool"), ShouldResemble, []string{"plug", "plug2"})

			write(t, f.claudeConfig(), `{"mcpServers": {"plug": {"type": "stdio", "command": "/tmp/evil"}}}`)

			f.sync(t)

			_, canonErr := os.Stat(f.vault.ServersPath())

			Convey("Then ownership is monotonic and a retired server is never adopted", func() {
				So(errors.Is(canonErr, fs.ErrNotExist), ShouldBeTrue)
				So(hostMCPServers(t, f.claudeConfig(), "mcpServers"), ShouldNotContainKey, "plug")
			})
		})
	})
}

func TestPluginMCPRemovedWithPlugin(t *testing.T) {
	Convey("Given a plugin removed from the registry", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeMCPServers(t, plugin, `{"mcpServers": {"plug": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug"}}}`)

		f.sync(t)

		removeFromRegistry(t, f.home, "acme", "tool")
		So(os.RemoveAll(plugin), ShouldBeNil)

		Convey("When sync runs", func() {
			f.sync(t)

			_, canonErr := os.Stat(f.vault.ServersPath())

			Convey("Then the server is removed everywhere and ownership is kept", func() {
				So(hostMCPServers(t, f.claudeConfig(), "mcpServers"), ShouldNotContainKey, "plug")
				So(hostMCPServers(t, f.openCodeConfig(), "mcp"), ShouldNotContainKey, "plug")
				So(errors.Is(canonErr, fs.ErrNotExist), ShouldBeTrue)
				So(ledgerServers(t, f, "acme/tool"), ShouldResemble, []string{"plug"})
			})
		})
	})
}

func TestPluginMCPFailSafeOnBrokenLedger(t *testing.T) {
	Convey("Given a broken ownership ledger", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeMCPServers(t, plugin, `{"mcpServers": {"plug": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug"}}}`)

		f.sync(t)

		claudeBefore := read(t, f.claudeConfig())

		So(os.Remove(f.vault.PluginsLedgerPath()), ShouldBeNil)
		So(os.MkdirAll(f.vault.PluginsLedgerPath(), 0o700), ShouldBeNil)

		report := f.sync(t)

		Convey("When sync runs with a directory as the ledger", func() {
			So(read(t, f.claudeConfig()), ShouldEqual, claudeBefore)
			So(warnedAbout(report, "ledger"), ShouldBeTrue)

			_, canonErr := os.Stat(f.vault.ServersPath())
			So(errors.Is(canonErr, fs.ErrNotExist), ShouldBeTrue)

			Convey("And once the ledger is repaired it rebuilds", func() {
				So(os.Remove(f.vault.PluginsLedgerPath()), ShouldBeNil)

				report = f.sync(t)
				So(read(t, f.claudeConfig()), ShouldEqual, claudeBefore)
				So(report.Action(kind.MCP, agent.ClaudeCodeID), ShouldEqual, engine.ActionNoop)
				So(ledgerServers(t, f, "acme/tool"), ShouldResemble, []string{"plug"})
			})
		})
	})
}

func TestPluginMCPDryRunAndPull(t *testing.T) {
	Convey("Given a plugin with ownership and a dry run", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeMCPServers(t, plugin, `{"mcpServers": {"plug": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug"}}}`)

		f.sync(t)

		claudeBefore := read(t, f.claudeConfig())

		So(os.Remove(f.vault.PluginsLedgerPath()), ShouldBeNil)

		report := f.run(t, engine.SyncOptions{DryRun: true})

		Convey("When a dry run then a real sync and a pull run", func() {
			So(report.Kind(kind.MCP).Warnings, ShouldBeEmpty)
			So(read(t, f.claudeConfig()), ShouldEqual, claudeBefore)

			_, ledgerErr := os.Stat(f.vault.PluginsLedgerPath())
			So(errors.Is(ledgerErr, fs.ErrNotExist), ShouldBeTrue)

			f.sync(t)
			So(ledgerServers(t, f, "acme/tool"), ShouldResemble, []string{"plug"})

			write(t, f.claudeConfig(), `{"mcpServers": {}}`)

			pullBefore := read(t, f.claudeConfig())
			ledgerBefore := read(t, f.vault.PluginsLedgerPath())

			_, err := f.engine.Sync(t.Context(), engine.SyncOptions{Direction: config.ModePull})
			So(err, ShouldBeNil)

			_, canonErr := os.Stat(f.vault.ServersPath())

			Convey("Then dry run persists nothing and pull leaves the ledger alone", func() {
				So(read(t, f.claudeConfig()), ShouldEqual, pullBefore)
				So(read(t, f.vault.PluginsLedgerPath()), ShouldEqual, ledgerBefore)
				So(errors.Is(canonErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestPluginMCPRejectsInvalidLedgerKeys(t *testing.T) {
	Convey("Given a ledger with a traversal key", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		evil := filepath.Join(claudePluginsDir(f.home), "cache", "evil", "1.0.0")
		write(t, filepath.Join(evil, ".mcp.json"), `{"mcpServers": {"evil": {"command": "/tmp/evil"}}}`)

		target, err := json.Marshal(evil)
		So(err, ShouldBeNil)

		write(t, f.vault.PluginsLedgerPath(), `{"version": 1, "plugins": {"../../evil": {"version": "1.0.0", "target": `+string(target)+`}}}`)

		f.sync(t)

		Convey("When sync runs", func() {
			Convey("Then the evil server never reaches the hosts", func() {
				So(hostMCPServers(t, f.claudeConfig(), "mcpServers"), ShouldNotContainKey, "evil")
				So(hostMCPServers(t, f.openCodeConfig(), "mcp"), ShouldNotContainKey, "evil")
			})
		})
	})
}

func TestPluginMCPWarningsVisible(t *testing.T) {
	Convey("Given a plugin server with an unknown variable", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeMCPServers(t, plugin, `{"mcpServers": {"bad": {"command": "${MY_VAR}"}}}`)

		report := f.sync(t)

		kindReport := report.Kind(kind.MCP)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When sync and doctor run", func() {
			Convey("Then the warning is visible in both", func() {
				So(kindReport.Warnings, ShouldNotBeEmpty)
				So(warnedAbout(report, "MY_VAR"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityInfo, "MY_VAR"), ShouldBeTrue)
			})
		})
	})
}
