package engine_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/hooks"
	"github.com/odiumuniverse/beadle/pkg/skill"
)

// TestA52PluginRemovedRetiresArtifacts covers the panel's deletion policy for
// a plugin removed from its host registry (the Claude installed_plugins.json)
// while its install cache stays on disk: every artifact beadle presented is
// pruned — skills links, agent/command renders, MCP records and hook renders —
// without a talking stub, and the doctor explains the outcome.
func TestA52PluginRemovedRetiresArtifacts(t *testing.T) {
	Convey("Given a Claude plugin delivered to the other hosts", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.CursorID)
		f.enableAgent(t, agent.CodexID)

		write(t, filepath.Join(f.home, ".cursor", "mcp.json"), `{"mcpServers":{}}`)
		write(t, filepath.Join(f.home, ".codex", "config.toml"), "")
		write(t, filepath.Join(f.home, ".codex", "hooks.json"), "{}\n")

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")
		write(t, filepath.Join(plugin, "agents", "helper.md"), "---\nname: helper\ndescription: helper agent\n---\n\nHelp.\n")
		write(t, filepath.Join(plugin, ".mcp.json"), `{"mcpServers": {"plug": {"command": "plug"}}}`)
		write(t, filepath.Join(plugin, "hooks", "hooks.json"),
			`{"hooks":{"PostToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"bash \"${CLAUDE_PLUGIN_ROOT}/audit.sh\""}]}]}}`)

		f.sync(t)

		_, err := f.engine.ApprovePluginHooks("acme/tool")
		So(err, ShouldBeNil)

		f.sync(t)

		skillLink := filepath.Join(openCodeSkillsDir(f.home), "alpha")
		codexHooks := filepath.Join(f.home, ".codex", "hooks.json")
		farmRoot := filepath.Join(f.vault.PluginsDir(), "farm", "acme", "tool")

		Convey("Then skills, MCP, renders and hooks reach the other hosts", func() {
			_, linkErr := os.Lstat(skillLink)
			So(linkErr, ShouldBeNil)
			So(hostMCPServers(t, f.openCodeConfig(), "mcp"), ShouldContainKey, "plug")
			So(read(t, codexHooks), ShouldContainSubstring, "acme")

			entries, readErr := os.ReadDir(farmRoot)
			So(readErr, ShouldBeNil)
			So(entries, ShouldNotBeEmpty)

			Convey("When the host registry drops the plugin and the cache stays", func() {
				removeFromRegistry(t, f.home, "acme", "tool")

				report := f.sync(t)
				result := pluginResult(t, report, "acme/tool")

				issues, doctorErr := f.engine.Doctor(t.Context())
				So(doctorErr, ShouldBeNil)

				Convey("Then every artifact is retired, no stub appears and the doctor explains", func() {
					So(result.Action, ShouldEqual, engine.PluginRetired)
					So(result.Note, ShouldContainSubstring, "removed from the plugin registry")

					// The pivot, the rendered farm artifacts and the skills
					// link are gone; nothing is stubbed.
					for _, path := range []string{
						filepath.Join(f.vault.PluginsDir(), "acme", "tool"),
						filepath.Join(f.vault.PluginsDir(), "farm", "acme", "tool"),
						filepath.Join(quarantineDir(f, "acme", "tool"), "current"),
						skillLink,
					} {
						_, statErr := os.Lstat(path)
						So(errors.Is(statErr, fs.ErrNotExist), ShouldBeTrue)
					}

					// The MCP record and the hook renders are removed.
					So(hostMCPServers(t, f.openCodeConfig(), "mcp"), ShouldNotContainKey, "plug")
					So(read(t, codexHooks), ShouldNotContainSubstring, "acme")

					// Doctor: the retire is an Info, the leftover approval is
					// an Error pointing at the explicit revoke; no heal.
					So(hasIssue(issues, engine.SeverityInfo, "plugin acme/tool was removed from its host registry"), ShouldBeTrue)
					So(hasIssue(issues, engine.SeverityError, "revoke with `beadle hooks revoke --plugin acme/tool`"), ShouldBeTrue)
					So(hasIssue(issues, engine.SeverityError, "run beadle heal"), ShouldBeFalse)

					Convey("And the host cache is left alone", func() {
						_, cacheErr := os.Stat(plugin)
						So(cacheErr, ShouldBeNil)
					})
				})
			})
		})
	})
}

// TestA52CodexCacheRemovedRetires pins the directory-backed source rule: for
// Codex (and the other non-Claude hosts) the install cache itself is the
// inventory, so a removed cache reads as "not installed any more" and the
// record retires. The registry-alive-but-path-missing quarantine case needs a
// registry that outlives the path, which only Claude's installed_plugins.json
// provides (see TestPluginReconcileQuarantinesMissingTarget).
func TestA52CodexCacheRemovedRetires(t *testing.T) {
	Convey("Given a Codex plugin whose install cache disappears", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		target := codexPluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, target, "alpha", "# alpha\n")

		f.sync(t)

		So(ledgerRecord(t, f, "acme/tool").Source, ShouldEqual, "codex")

		So(os.RemoveAll(filepath.Dir(target)), ShouldBeNil)

		report := f.sync(t)
		result := pluginResult(t, report, "acme/tool")

		Convey("When the next sync runs", func() {
			_, pivotErr := os.Stat(filepath.Join(f.vault.PluginsDir(), "acme", "tool"))

			Convey("Then the record retires instead of lingering or stubbing", func() {
				So(result.Action, ShouldEqual, engine.PluginRetired)
				So(errors.Is(pivotErr, fs.ErrNotExist), ShouldBeTrue)
				So(ledgerRecord(t, f, "acme/tool").RetiredAt.IsZero(), ShouldBeFalse)
			})
		})
	})
}

// TestA52RetireSkipsInvalidLedgerKey keeps the report honest: a hand-edited
// ledger key that is not a plugin key is skipped, never reported as retired.
func TestA52RetireSkipsInvalidLedgerKey(t *testing.T) {
	Convey("Given a ledger holding a hand-edited invalid key", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		f.sync(t)

		ledger := read(t, f.vault.PluginsLedgerPath())
		write(t, f.vault.PluginsLedgerPath(), strings.Replace(ledger, `"acme/tool"`, `"broken"`, 1))

		report := f.sync(t)

		Convey("When the sync retires the removed plugins", func() {
			Convey("Then the invalid key is skipped, not retired", func() {
				result := pluginResult(t, report, "broken")
				So(result.Action, ShouldEqual, engine.PluginSkipped)
				So(result.Note, ShouldEqual, "invalid plugin key")
				So(read(t, f.vault.PluginsLedgerPath()), ShouldNotContainSubstring, "retired_at")
			})
		})
	})
}

// TestA52QuarantineThenRetirePrunesStub covers the stub cleanup on the
// quarantine → retire edge: a broken install first stubs the farm links, then
// the host registry drops the plugin and the same sync removes the stub, the
// links and the empty marketplace directory.
func TestA52QuarantineThenRetirePrunesStub(t *testing.T) {
	Convey("Given a quarantined plugin that is then removed from the registry", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		f.sync(t)

		// The registry record stays: only the install path is gone, which is
		// the broken-install case that quarantines.
		So(os.RemoveAll(plugin), ShouldBeNil)

		report := f.sync(t)
		So(pluginResult(t, report, "acme/tool").Action, ShouldEqual, engine.PluginQuarantined)

		for _, dir := range []string{claudeSkillsDir(f.home), openCodeSkillsDir(f.home)} {
			key, ok := skill.IsStubDir(filepath.Join(dir, "alpha"))
			So(ok, ShouldBeTrue)
			So(key, ShouldEqual, "acme/tool")
		}

		removeFromRegistry(t, f.home, "acme", "tool")

		report = f.sync(t)

		Convey("When the registry drops it too", func() {
			Convey("Then the stub and the artifacts are retired", func() {
				So(pluginResult(t, report, "acme/tool").Action, ShouldEqual, engine.PluginRetired)

				for _, dir := range []string{claudeSkillsDir(f.home), openCodeSkillsDir(f.home)} {
					_, linkErr := os.Lstat(filepath.Join(dir, "alpha"))
					So(errors.Is(linkErr, fs.ErrNotExist), ShouldBeTrue)
				}

				_, quarantineErr := os.Stat(quarantineDir(f, "acme", "tool"))
				So(errors.Is(quarantineErr, fs.ErrNotExist), ShouldBeTrue)

				_, marketErr := os.Stat(filepath.Join(f.vault.PluginsDir(), "acme"))
				So(errors.Is(marketErr, fs.ErrNotExist), ShouldBeTrue)

				rec := ledgerRecord(t, f, "acme/tool")
				So(rec.RetiredAt.IsZero(), ShouldBeFalse)
				So(rec.QuarantinedAt.IsZero(), ShouldBeTrue)

				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityError, "is quarantined"), ShouldBeFalse)
				So(hasIssue(issues, engine.SeverityInfo, "was removed from its host registry"), ShouldBeTrue)
			})
		})
	})
}

// TestA52RetiredPluginHookRendersNowhere covers the bundle side of the panel's
// hook rule: a retired plugin's approved hook leaves every native bundle too,
// while the canon entry waits for an explicit revoke.
func TestA52RetiredPluginHookRendersNowhere(t *testing.T) {
	Convey("Given a retired plugin whose hook stays approved", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		write(t, filepath.Join(plugin, "hooks", "hooks.json"),
			`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"bash \"${CLAUDE_PLUGIN_ROOT}/start.sh\""}]}]}}`)

		f.sync(t)

		_, err := f.engine.ApprovePluginHooks("acme/tool")
		So(err, ShouldBeNil)

		f.sync(t)

		canon, err := hooks.Load(f.vault.HooksPath())
		So(err, ShouldBeNil)
		So(canon, ShouldContainKey, "tool--session-start-1")
		So(engine.RenderableHooksForTest(f.engine, "gemini", canon), ShouldContainKey, "tool--session-start-1")

		removeFromRegistry(t, f.home, "acme", "tool")
		f.sync(t)

		Convey("When the plugin is gone", func() {
			Convey("Then no bundle renders the hook and doctor asks for the revoke", func() {
				canon, err := hooks.Load(f.vault.HooksPath())
				So(err, ShouldBeNil)
				So(canon, ShouldContainKey, "tool--session-start-1")

				for _, host := range []string{"claude", "gemini", "antigravity"} {
					So(engine.RenderableHooksForTest(f.engine, host, canon), ShouldNotContainKey, "tool--session-start-1")
				}

				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityError, "beadle hooks revoke --plugin acme/tool"), ShouldBeTrue)
			})
		})
	})
}

// TestA52RetiredPluginPinDoesNotRecreatePivot keeps the retire durable: a
// pinned version of a retired plugin must not bring its pivot directory back.
func TestA52RetiredPluginPinDoesNotRecreatePivot(t *testing.T) {
	Convey("Given a pinned plugin removed from the registry", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeSkill(t, plugin, "alpha", "# alpha\n")

		So(f.config.SetPluginPin(agent.ClaudeCodeID, "acme/tool", "1.0.0"), ShouldBeNil)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		f.sync(t)

		removeFromRegistry(t, f.home, "acme", "tool")
		f.sync(t)

		Convey("When the pin outlives the plugin", func() {
			_, pivotErr := os.Stat(filepath.Join(f.vault.PluginsDir(), "acme", "tool"))

			Convey("Then the pivot stays retired and is not recreated", func() {
				So(ledgerRecord(t, f, "acme/tool").RetiredAt.IsZero(), ShouldBeFalse)
				So(errors.Is(pivotErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

// TestA52RetiredPluginNeverAdopted keeps the ownership invariant: a retired
// plugin's MCP server names stay owned, so a stale host copy can never become
// canon (and a reinstall revives the plugin instead).
func TestA52RetiredPluginNeverAdopted(t *testing.T) {
	Convey("Given a retired plugin that owned an MCP server", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		write(t, filepath.Join(plugin, ".mcp.json"), `{"mcpServers": {"plug": {"command": "plug"}}}`)

		f.sync(t)

		removeFromRegistry(t, f.home, "acme", "tool")
		f.sync(t)

		Convey("When the host still carries the server", func() {
			write(t, f.openCodeConfig(), `{"mcp": {"plug": {"type": "local", "command": ["/tmp/evil"]}}}`)

			f.sync(t)

			_, vaultErr := os.Stat(f.vault.ServersPath())

			Convey("Then it is removed, never adopted into the canon", func() {
				So(errors.Is(vaultErr, fs.ErrNotExist), ShouldBeTrue)
				So(hostMCPServers(t, f.openCodeConfig(), "mcp"), ShouldNotContainKey, "plug")
			})
		})
	})
}
