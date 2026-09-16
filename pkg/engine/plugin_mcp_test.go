package engine_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/agent"
	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/engine"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
	"github.com/odiumuniverse/agents-sync/pkg/mcp"
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
	require.NoError(t, json.Unmarshal([]byte(read(t, path)), &doc))

	servers, ok := doc[pointer].(map[string]any)
	require.True(t, ok, "no %s in %s", pointer, path)

	out := make(map[string]map[string]any, len(servers))

	for name, entry := range servers {
		value, ok := entry.(map[string]any)
		require.True(t, ok, "server %s is not an object", name)

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

	require.NoError(t, json.Unmarshal([]byte(read(t, f.vault.PluginsLedgerPath())), &doc))

	rec, ok := doc.Plugins[key]
	require.True(t, ok, "ledger must contain %s", key)

	return rec.Servers
}

func TestPluginMCPRendersIntoHosts(t *testing.T) {
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

	report := f.sync(t)
	require.Empty(t, report.Kind(kind.MCP).Warnings)

	pivot := pluginPivot(f, "acme", "tool")

	claude := hostMCPServers(t, f.claudeConfig(), "mcpServers")
	require.Equal(t, pivot+"/bin/plug", claude["plug"]["command"])
	require.Equal(t, []any{"serve", pivot}, claude["plug"]["args"])

	env, ok := claude["plug"]["env"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, pivot, env["ROOT"])

	require.Equal(t, "http", claude["web"]["type"])
	require.Equal(t, "https://example.com/mcp", claude["web"]["url"])
	require.Equal(t, "sse", claude["events"]["type"])

	openCode := hostMCPServers(t, f.openCodeConfig(), "mcp")
	require.Equal(t, "local", openCode["plug"]["type"])
	require.Equal(t, []any{pivot + "/bin/plug", "serve", pivot}, openCode["plug"]["command"])
	require.Equal(t, "remote", openCode["web"]["type"])
	require.Equal(t, "remote", openCode["events"]["type"], "the SSE transport is normalized per host")

	claudeBefore := read(t, f.claudeConfig())
	openCodeBefore := read(t, f.openCodeConfig())

	report = f.sync(t)
	require.Equal(t, engine.ActionNoop, report.Action(kind.MCP, agent.ClaudeCodeID))

	result, ok := report.Kind(kind.MCP).Agent(agent.ClaudeCodeID)
	require.True(t, ok)
	require.NotContains(t, result.Note, "did not keep")

	require.Equal(t, claudeBefore, read(t, f.claudeConfig()))
	require.Equal(t, openCodeBefore, read(t, f.openCodeConfig()))
	require.NoFileExists(t, f.vault.ServersPath(), "plugin servers must not leak into the canon")
}

func TestPluginMCPSurvivesUpgrade(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeMCPServers(t, plugin, `{"mcpServers": {"plug": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug", "args": ["v1"]}}}`)

	f.sync(t)

	pivot := pluginPivot(f, "acme", "tool")
	require.Equal(t, pivot+"/bin/plug", hostMCPServers(t, f.claudeConfig(), "mcpServers")["plug"]["command"])
	require.Equal(t, []string{"plug"}, ledgerServers(t, f, "acme/tool"))

	upgraded := pluginTree(t, f.home, "acme", "tool", "2.0.0")
	writeMCPServers(t, upgraded, `{"mcpServers": {"plug": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug", "args": ["v2"]}}}`)

	f.sync(t)

	claude := hostMCPServers(t, f.claudeConfig(), "mcpServers")
	require.Equal(t, pivot+"/bin/plug", claude["plug"]["command"], "the pivot path is stable across upgrades")
	require.Equal(t, []any{"v2"}, claude["plug"]["args"])
	require.Contains(t, read(t, filepath.Join(pivot, ".mcp.json")), `"v2"`, "the path resolves to the upgraded content")

	report := f.sync(t)
	require.Equal(t, engine.ActionNoop, report.Action(kind.MCP, agent.ClaudeCodeID))
}

func TestPluginMCPRefusesUnknownVariable(t *testing.T) {
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
	require.Contains(t, claude, "good")
	require.NotContains(t, claude, "bad")

	warned := slices.ContainsFunc(report.Kind(kind.MCP).Warnings, func(warning string) bool {
		return strings.Contains(warning, "MY_VAR")
	})
	require.True(t, warned, "warnings: %v", report.Kind(kind.MCP).Warnings)

	upgraded := pluginTree(t, f.home, "acme", "tool", "2.0.0")
	writeMCPServers(t, upgraded, `{"mcpServers": {"good": {"command": "${MY_VAR}"}}}`)

	f.sync(t)

	claude = hostMCPServers(t, f.claudeConfig(), "mcpServers")
	require.NotContains(t, claude, "good", "a server that lost its variables is removed")
	require.NoFileExists(t, f.vault.ServersPath())
}

func TestPluginMCPCanonWins(t *testing.T) {
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
	require.Equal(t, "canon-cmd", claude["alpha"]["command"])

	warned := slices.ContainsFunc(report.Kind(kind.MCP).Warnings, func(warning string) bool {
		return strings.Contains(warning, "collides with the vault canon")
	})
	require.True(t, warned, "warnings: %v", report.Kind(kind.MCP).Warnings)
	require.Equal(t, canonDoc, read(t, f.vault.ServersPath()), "the canon must not change")
}

func TestPluginMCPPluginCollision(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	first := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeMCPServers(t, first, `{"mcpServers": {"shared": {"command": "first-cmd"}}}`)

	second := pluginTree(t, f.home, "beta", "other", "1.0.0")
	writeMCPServers(t, second, `{"mcpServers": {"shared": {"command": "second-cmd"}}}`)

	report := f.sync(t)

	claude := hostMCPServers(t, f.claudeConfig(), "mcpServers")
	require.Equal(t, "first-cmd", claude["shared"]["command"])

	warned := slices.ContainsFunc(report.Kind(kind.MCP).Warnings, func(warning string) bool {
		return strings.Contains(warning, "already provided by acme/tool")
	})
	require.True(t, warned, "warnings: %v", report.Kind(kind.MCP).Warnings)
}

func TestPluginMCPUserEditNotAdopted(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeMCPServers(t, plugin, `{"mcpServers": {"plug": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug"}}}`)

	f.sync(t)

	write(t, f.claudeConfig(), `{"mcpServers": {"plug": {"type": "stdio", "command": "/tmp/evil"}}}`)

	f.sync(t)

	claude := hostMCPServers(t, f.claudeConfig(), "mcpServers")
	require.Equal(t, pluginPivot(f, "acme", "tool")+"/bin/plug", claude["plug"]["command"])
	require.NoFileExists(t, f.vault.ServersPath(), "a plugin-owned server must never be adopted")
}

func TestPluginMCPOwnershipMonotonicOnUpgrade(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeMCPServers(t, plugin, `{"mcpServers": {"plug": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug"}}}`)

	f.sync(t)
	require.Equal(t, []string{"plug"}, ledgerServers(t, f, "acme/tool"))

	upgraded := pluginTree(t, f.home, "acme", "tool", "2.0.0")
	writeMCPServers(t, upgraded, `{"mcpServers": {"plug2": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug2"}}}`)

	f.sync(t)
	require.Equal(t, []string{"plug", "plug2"}, ledgerServers(t, f, "acme/tool"), "ownership is monotonic across upgrades")

	write(t, f.claudeConfig(), `{"mcpServers": {"plug": {"type": "stdio", "command": "/tmp/evil"}}}`)

	f.sync(t)

	require.NoFileExists(t, f.vault.ServersPath(), "a retired plugin server must never be adopted")
	require.NotContains(t, hostMCPServers(t, f.claudeConfig(), "mcpServers"), "plug")
}

func TestPluginMCPRemovedWithPlugin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeMCPServers(t, plugin, `{"mcpServers": {"plug": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug"}}}`)

	f.sync(t)

	removeFromRegistry(t, f.home, "acme", "tool")
	require.NoError(t, os.RemoveAll(plugin))

	f.sync(t)

	require.NotContains(t, hostMCPServers(t, f.claudeConfig(), "mcpServers"), "plug")
	require.NotContains(t, hostMCPServers(t, f.openCodeConfig(), "mcp"), "plug")
	require.NoFileExists(t, f.vault.ServersPath())
	require.Equal(t, []string{"plug"}, ledgerServers(t, f, "acme/tool"), "ownership is monotonic")
}

func TestPluginMCPFailSafeOnBrokenLedger(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeMCPServers(t, plugin, `{"mcpServers": {"plug": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug"}}}`)

	f.sync(t)

	claudeBefore := read(t, f.claudeConfig())

	require.NoError(t, os.Remove(f.vault.PluginsLedgerPath()))
	require.NoError(t, os.MkdirAll(f.vault.PluginsLedgerPath(), 0o700))

	report := f.sync(t)

	require.Equal(t, claudeBefore, read(t, f.claudeConfig()), "a broken ledger must not wipe the host config")
	require.NoFileExists(t, f.vault.ServersPath())

	warned := slices.ContainsFunc(report.Kind(kind.MCP).Warnings, func(warning string) bool {
		return strings.Contains(warning, "ledger")
	})
	require.True(t, warned, "warnings: %v", report.Kind(kind.MCP).Warnings)

	require.NoError(t, os.Remove(f.vault.PluginsLedgerPath()))

	report = f.sync(t)
	require.Equal(t, claudeBefore, read(t, f.claudeConfig()))
	require.Equal(t, engine.ActionNoop, report.Action(kind.MCP, agent.ClaudeCodeID))
	require.Equal(t, []string{"plug"}, ledgerServers(t, f, "acme/tool"))
}

func TestPluginMCPDryRunAndPull(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeMCPServers(t, plugin, `{"mcpServers": {"plug": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug"}}}`)

	f.sync(t)

	claudeBefore := read(t, f.claudeConfig())

	require.NoError(t, os.Remove(f.vault.PluginsLedgerPath()))

	report := f.run(t, engine.SyncOptions{DryRun: true})
	require.Empty(t, report.Kind(kind.MCP).Warnings)
	require.Equal(t, claudeBefore, read(t, f.claudeConfig()))
	require.NoFileExists(t, f.vault.PluginsLedgerPath(), "dry run must not persist ownership")

	f.sync(t)
	require.Equal(t, []string{"plug"}, ledgerServers(t, f, "acme/tool"))

	write(t, f.claudeConfig(), `{"mcpServers": {}}`)

	pullBefore := read(t, f.claudeConfig())
	ledgerBefore := read(t, f.vault.PluginsLedgerPath())

	_, err := f.engine.Sync(t.Context(), engine.SyncOptions{Direction: config.ModePull})
	require.NoError(t, err)
	require.Equal(t, pullBefore, read(t, f.claudeConfig()))
	require.Equal(t, ledgerBefore, read(t, f.vault.PluginsLedgerPath()), "pull must not touch the ownership ledger")
	require.NoFileExists(t, f.vault.ServersPath())
}

func TestPluginMCPRejectsInvalidLedgerKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	evil := filepath.Join(claudePluginsDir(f.home), "cache", "evil", "1.0.0")
	write(t, filepath.Join(evil, ".mcp.json"), `{"mcpServers": {"evil": {"command": "/tmp/evil"}}}`)

	target, err := json.Marshal(evil)
	require.NoError(t, err)

	write(t, f.vault.PluginsLedgerPath(), `{"version": 1, "plugins": {"../../evil": {"version": "1.0.0", "target": `+string(target)+`}}}`)

	f.sync(t)

	require.NotContains(t, hostMCPServers(t, f.claudeConfig(), "mcpServers"), "evil")
	require.NotContains(t, hostMCPServers(t, f.openCodeConfig(), "mcp"), "evil")
}

func TestPluginMCPWarningsVisible(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeMCPServers(t, plugin, `{"mcpServers": {"bad": {"command": "${MY_VAR}"}}}`)

	report := f.sync(t)

	kindReport := report.Kind(kind.MCP)
	require.NotEmpty(t, kindReport.Warnings)

	warned := slices.ContainsFunc(kindReport.Warnings, func(warning string) bool {
		return strings.Contains(warning, "MY_VAR")
	})
	require.True(t, warned, "warnings: %v", kindReport.Warnings)

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityInfo, "MY_VAR"), "issues: %v", issues)
}
