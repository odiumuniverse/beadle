package engine_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/hooks"
)

// a31InstallPlugin writes a fake Claude plugin cache entry for one installed
// plugin, links its pivot and returns the install path.
func a31InstallPlugin(t *testing.T, f *fixture, marketplace, name, hooksJSON string) string {
	t.Helper()

	install := filepath.Join(f.home, ".claude", "plugins", "cache", marketplace, name, "1.0.0")
	write(t, filepath.Join(install, "hooks", "hooks.json"), hooksJSON)

	write(t, filepath.Join(f.home, ".claude", "plugins", "installed_plugins.json"),
		fmt.Sprintf(`{"plugins":{%q:[{"scope":"user","installPath":%q,"version":"1.0.0"}]}}`, name+"@"+marketplace, install))

	pivot := filepath.Join(f.vault.PluginsDir(), marketplace, name, "current")

	if err := os.MkdirAll(filepath.Dir(pivot), 0o700); err != nil {
		t.Fatalf("mkdir pivot: %v", err)
	}

	_ = os.Remove(pivot)

	if err := os.Symlink(install, pivot); err != nil {
		t.Fatalf("link pivot: %v", err)
	}

	return install
}

// a31Hooks is the canon-mappable slice plus three definitions the canon cannot
// express: an unsupported event, an async handler and a data-directory
// variable.
const a31Hooks = `{
  "hooks": {
    "PostToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "bash \"${CLAUDE_PLUGIN_ROOT}/hook.sh\"", "timeout": 30}]}],
    "SessionStart": [{"hooks": [{"type": "command", "command": "bash \"${CLAUDE_PLUGIN_ROOT}/start.sh\""}]}],
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "ignored.sh"}]}],
    "Stop": [{"hooks": [{"type": "command", "command": "bg.sh", "async": true}]}],
    "Notification": [{"hooks": [{"type": "command", "command": "uses ${CLAUDE_PLUGIN_DATA}/x"}]}]
  }
}`

// a31PivotCommand is the pivot-expanded command of a plugin hook.
func a31PivotCommand(f *fixture, marketplace, name, script string) string {
	return fmt.Sprintf("bash %q", filepath.Join(f.vault.PluginsDir(), marketplace, name, "current", script))
}

// a31HookIssues filters doctor issues down to the plugin-hooks check.
func a31HookIssues(issues []engine.Issue, substr string) []engine.Issue {
	var out []engine.Issue

	for _, issue := range issues {
		if strings.Contains(issue.Message, "plugin hooks:") && strings.Contains(issue.Message, substr) {
			out = append(out, issue)
		}
	}

	return out
}

func TestPluginHooksApprove(t *testing.T) {
	Convey("Given an installed plugin with command hooks", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		a31InstallPlugin(t, f, "acme", "tool", a31Hooks)

		Convey("When the plugin is approved", func() {
			report, err := f.engine.ApprovePluginHooks("acme/tool")
			So(err, ShouldBeNil)

			Convey("Then the canon holds the expanded hooks and the config approves them", func() {
				canon, err := hooks.Load(f.vault.HooksPath())
				So(err, ShouldBeNil)
				So(canon, ShouldHaveLength, 2)

				post, ok := canon["tool--post-tool-1"]
				So(ok, ShouldBeTrue)
				So(post.Event, ShouldEqual, "post-tool")
				So(post.Matcher, ShouldEqual, "Bash")
				So(post.Command, ShouldEqual, a31PivotCommand(f, "acme", "tool", "hook.sh"))
				So(post.Timeout, ShouldEqual, 30)
				So(post.Source, ShouldEqual, "plugin:acme/tool")

				So(canon["tool--session-start-1"].Command, ShouldEqual, a31PivotCommand(f, "acme", "tool", "start.sh"))
				So(f.config.HookApproved("tool--post-tool-1"), ShouldBeTrue)
				So(f.config.HookApproved("tool--session-start-1"), ShouldBeTrue)
			})

			Convey("Then the unexpressible definitions are aggregated and warned", func() {
				So(a36HasText(report.Warnings, "unsupported events"), ShouldBeTrue)
				So(a36HasText(report.Warnings, `unsupported variable "CLAUDE_PLUGIN_DATA"`), ShouldBeTrue)
				So(a36HasText(report.Warnings, `unsupported field(s) async`), ShouldBeTrue)
				So(a36HasText(report.Notes, "2 approved, 0 refreshed, 0 removed, 3 skipped"), ShouldBeTrue)
			})

			Convey("Then doctor is silent about the approved plugin", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(a31HookIssues(issues, "acme/tool"), ShouldBeEmpty)
			})

			Convey("Then a second approve is a no-op", func() {
				before, err := hooks.Load(f.vault.HooksPath())
				So(err, ShouldBeNil)

				again, err := f.engine.ApprovePluginHooks("acme/tool")
				So(err, ShouldBeNil)
				So(a36HasText(again.Notes, "0 approved, 0 refreshed, 0 removed"), ShouldBeTrue)

				after, err := hooks.Load(f.vault.HooksPath())
				So(err, ShouldBeNil)
				So(after, ShouldResemble, before)
			})
		})
	})
}

func TestPluginHooksDrift(t *testing.T) {
	Convey("Given an approved plugin whose hooks changed", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		a31InstallPlugin(t, f, "acme", "tool", a31Hooks)

		if _, err := f.engine.ApprovePluginHooks("acme/tool"); err != nil {
			t.Fatalf("approve: %v", err)
		}

		a31InstallPlugin(t, f, "acme", "tool",
			`{"hooks": {"PostToolUse": [{"hooks": [{"type": "command", "command": "bash \"${CLAUDE_PLUGIN_ROOT}/hook-v2.sh\""}]}]}}`)

		Convey("Then doctor reports the drift", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)
			So(hasIssue(issues, engine.SeverityInfo, "hooks changed since approval"), ShouldBeTrue)
		})

		Convey("Then re-approving refreshes the entry and removes the stale one", func() {
			report, err := f.engine.ApprovePluginHooks("acme/tool")
			So(err, ShouldBeNil)
			So(a36HasText(report.Notes, "0 approved, 1 refreshed, 1 removed"), ShouldBeTrue)

			canon, err := hooks.Load(f.vault.HooksPath())
			So(err, ShouldBeNil)
			So(canon["tool--post-tool-1"].Command, ShouldEqual, a31PivotCommand(f, "acme", "tool", "hook-v2.sh"))
			So(canon, ShouldNotContainKey, "tool--session-start-1")
		})
	})
}

func TestPluginHooksCollision(t *testing.T) {
	Convey("Given a canon hook already using the projected name", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		a31InstallPlugin(t, f, "acme", "tool", a31Hooks)

		write(t, f.vault.HooksPath(), `{"tool--post-tool-1": {"event": "stop", "command": "authored.sh"}}`)

		Convey("Then doctor counts only the non-collided hook as pending", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)
			So(hasIssue(issues, engine.SeverityWarn, "taken by other canon hooks"), ShouldBeTrue)
			So(hasIssue(issues, engine.SeverityInfo, "ships 1 hook(s)"), ShouldBeTrue)
		})

		Convey("When the plugin is approved", func() {
			report, err := f.engine.ApprovePluginHooks("acme/tool")
			So(err, ShouldBeNil)

			Convey("Then the canon wins, the collision warns and the authored hook stays unapproved", func() {
				So(a36HasText(report.Warnings, "already exists in the canon"), ShouldBeTrue)

				canon, err := hooks.Load(f.vault.HooksPath())
				So(err, ShouldBeNil)
				So(canon["tool--post-tool-1"].Command, ShouldEqual, "authored.sh")
				So(canon["tool--session-start-1"].Command, ShouldEqual, a31PivotCommand(f, "acme", "tool", "start.sh"))

				So(f.config.HookApproved("tool--post-tool-1"), ShouldBeFalse)
				So(f.config.HookApproved("tool--session-start-1"), ShouldBeTrue)
			})

			Convey("Then doctor reports the collision once, not as drift", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityWarn, "taken by other canon hooks"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityInfo, "hooks changed since approval"), ShouldBeFalse)
				So(a31HookIssues(issues, "ships"), ShouldBeEmpty)
			})
		})
	})
}

func TestPluginHooksSelfPlugin(t *testing.T) {
	Convey("Given beadle's own bundle in the plugin cache", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		a31InstallPlugin(t, f, "beadle", "beadle-canon", `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "own.sh"}]}]}}`)

		Convey("When the plugin is scanned and approved", func() {
			report, err := f.engine.ApprovePluginHooks("beadle/beadle-canon")
			So(err, ShouldBeError)

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then it is refused and never offered", func() {
				So(report.Notes, ShouldBeEmpty)
				So(a31HookIssues(issues, "beadle-canon"), ShouldBeEmpty)
			})
		})
	})
}

func TestPluginHooksPlaceholders(t *testing.T) {
	Convey("Given a plugin hook with an oversized timeout and host placeholders", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		a31InstallPlugin(t, f, "acme", "tool", `{"hooks": {
			"PreToolUse": [{"hooks": [{"type": "command", "command": "run ${CLAUDE_PROJECT_DIR}/x.sh"}]}],
			"PostToolUse": [{"hooks": [{"type": "command", "command": "run ${user_config.foo}"}]}],
			"Stop": [{"hooks": [{"type": "command", "command": "run $CLAUDE_PLUGIN_DATA/x"}]}],
			"Notification": [{"hooks": [{"type": "command", "command": "echo $SHELL $HOME"}]}],
			"SessionStart": [{"hooks": [{"type": "command", "command": "slow.sh", "timeout": 3600}]}]
		}}`)

		Convey("When the plugin is approved", func() {
			report, err := f.engine.ApprovePluginHooks("acme/tool")
			So(err, ShouldBeNil)

			Convey("Then host placeholders are refused and shell variables stay verbatim", func() {
				canon, err := hooks.Load(f.vault.HooksPath())
				So(err, ShouldBeNil)
				So(canon, ShouldHaveLength, 2)
				So(canon["tool--notification-1"].Command, ShouldEqual, "echo $SHELL $HOME")

				So(a36HasText(report.Warnings, `unsupported variable "CLAUDE_PROJECT_DIR"`), ShouldBeTrue)
				So(a36HasText(report.Warnings, `unsupported variable "user_config.foo"`), ShouldBeTrue)
				So(a36HasText(report.Warnings, `unsupported variable "$CLAUDE_PLUGIN_DATA"`), ShouldBeTrue)
			})

			Convey("Then the oversized timeout is clamped with a warning", func() {
				canon, err := hooks.Load(f.vault.HooksPath())
				So(err, ShouldBeNil)
				So(canon["tool--session-start-1"].Timeout, ShouldEqual, hooks.MaxTimeout)
				So(a36HasText(report.Warnings, "exceeds the canon maximum 600s; clamped"), ShouldBeTrue)
			})
		})
	})
}

func TestPluginHooksCounters(t *testing.T) {
	Convey("Given a plugin with a grouped unsupported event and an empty command", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		a31InstallPlugin(t, f, "acme", "tool", `{"hooks": {
			"UserPromptSubmit": [{"hooks": [
				{"type": "command", "command": "a.sh"},
				{"type": "command", "command": "b.sh"}
			]}],
			"SessionStart": [{"hooks": [{"type": "command", "command": "   "}]}]
		}}`)

		Convey("When the plugin is approved", func() {
			report, err := f.engine.ApprovePluginHooks("acme/tool")
			So(err, ShouldBeNil)

			Convey("Then skipped handlers are counted individually and the empty command warns", func() {
				So(a36HasText(report.Warnings, "skipped 2 hook(s) on unsupported events"), ShouldBeTrue)
				So(a36HasText(report.Warnings, "empty command; not rendered"), ShouldBeTrue)
				So(a36HasText(report.Notes, "0 approved, 0 refreshed, 0 removed, 3 skipped"), ShouldBeTrue)
			})
		})
	})
}

func TestPluginHooksGhostInstall(t *testing.T) {
	Convey("Given an installed plugin whose cache directory is gone but its pivot resolves", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		install := a31InstallPlugin(t, f, "acme", "tool", a31Hooks)

		older := filepath.Join(filepath.Dir(install), "0.9.0")
		So(os.MkdirAll(older, 0o700), ShouldBeNil)
		So(os.RemoveAll(install), ShouldBeNil)

		pivot := filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current")
		So(os.Remove(pivot), ShouldBeNil)
		So(os.Symlink(older, pivot), ShouldBeNil)

		Convey("Then approving refuses instead of reporting zero hooks", func() {
			_, err := f.engine.ApprovePluginHooks("acme/tool")
			So(err, ShouldBeError)
			So(err.Error(), ShouldContainSubstring, "install path")
			So(err.Error(), ShouldContainSubstring, "is missing")
		})
	})
}

func TestPluginHooksPivotRequired(t *testing.T) {
	Convey("Given an installed plugin without a pivot", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		a31InstallPlugin(t, f, "acme", "tool", a31Hooks)

		pivot := filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current")
		So(os.Remove(pivot), ShouldBeNil)

		Convey("Then approving refuses until the pivot exists", func() {
			_, err := f.engine.ApprovePluginHooks("acme/tool")
			So(err, ShouldBeError)
			So(err.Error(), ShouldContainSubstring, "run `beadle sync` first")
		})

		Convey("Then doctor warns about the missing pivot", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)
			So(a31HookIssues(issues, "pivot"), ShouldHaveLength, 1)
		})
	})
}

func TestPluginHooksDoctor(t *testing.T) {
	Convey("Given an installed plugin with unapproved hooks", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		a31InstallPlugin(t, f, "acme", "tool", a31Hooks)

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the plugin waits as an approval Info", func() {
				So(hasIssue(issues, engine.SeverityInfo, "beadle hooks approve --plugin acme/tool"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityInfo, "3 skipped"), ShouldBeTrue)
			})
		})
	})

	Convey("Given an approved plugin that disappeared", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		a31InstallPlugin(t, f, "acme", "tool", a31Hooks)

		if _, err := f.engine.ApprovePluginHooks("acme/tool"); err != nil {
			t.Fatalf("approve: %v", err)
		}

		write(t, filepath.Join(f.home, ".claude", "plugins", "installed_plugins.json"), `{"plugins": {}}`)

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the orphaned hooks warn without being removed", func() {
				So(a31HookIssues(issues, "which is not installed"), ShouldHaveLength, 1)

				canon, err := hooks.Load(f.vault.HooksPath())
				So(err, ShouldBeNil)
				So(canon, ShouldHaveLength, 2)
			})
		})
	})

	Convey("Given a hooks canon holding a secret-like command", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, f.vault.HooksPath(), `{"leaky": {"event": "stop", "command": "curl -H 'Authorization: Bearer sk-proj-abcdefghijklmnopqrstuvwxyz' https://example.com"}}`)

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the secret gate warns without blocking", func() {
				So(hasIssue(issues, engine.SeverityWarn, "secret-like line"), ShouldBeTrue)
			})
		})
	})
}

func TestPluginHooksDelivery(t *testing.T) {
	Convey("Given an approved plugin hook", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.enableAgent(t, agent.CursorID)
		f.enableAgent(t, agent.CodexID)
		cursorHome(t, f)

		write(t, filepath.Join(f.home, ".codex", "config.toml"), "")

		a31InstallPlugin(t, f, "acme", "tool",
			`{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "bash \"${CLAUDE_PLUGIN_ROOT}/start.sh\""}]}]}}`)

		if _, err := f.engine.ApprovePluginHooks("acme/tool"); err != nil {
			t.Fatalf("approve: %v", err)
		}

		f.sync(t)

		Convey("Then the Cursor and Codex hooks files carry the expanded command", func() {
			expanded := a31PivotCommand(f, "acme", "tool", "start.sh")

			So(a36Events(t, a36CursorHooks(f))["sessionStart"], ShouldResemble, []any{
				map[string]any{"command": expanded},
			})
			So(a36Events(t, a36CodexHooks(f))["SessionStart"], ShouldResemble, []any{
				map[string]any{"hooks": []any{map[string]any{"type": "command", "command": expanded}}},
			})
		})

		Convey("Then Claude excludes the plugin hook and the other bundles include it", func() {
			claude, err := engine.BundleRequestHooksForTest(f.engine, "claude")
			So(err, ShouldBeNil)
			So(claude, ShouldNotContainKey, "tool--session-start-1")

			gemini, err := engine.BundleRequestHooksForTest(f.engine, "gemini")
			So(err, ShouldBeNil)
			So(gemini, ShouldContainKey, "tool--session-start-1")

			agy, err := engine.BundleRequestHooksForTest(f.engine, "antigravity")
			So(err, ShouldBeNil)
			So(agy, ShouldContainKey, "tool--session-start-1")
		})
	})
}

func TestPluginHooksClaudeBundleExclusion(t *testing.T) {
	Convey("Given a canon with an authored and a plugin-sourced hook", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		canon := map[string]hooks.Hook{
			"authored": {Event: "stop", Command: "authored.sh"},
			"from-plugin": {
				Event: "stop", Command: "plugin.sh",
				Source: hooks.SourcePluginPrefix + "acme/tool",
			},
		}

		Convey("Then only the authored hook reaches the Claude bundle", func() {
			claude := engine.RenderableHooksForTest(f.engine, "claude", canon)
			So(claude, ShouldContainKey, "authored")
			So(claude, ShouldNotContainKey, "from-plugin")
		})

		Convey("Then every other host gets both", func() {
			gemini := engine.RenderableHooksForTest(f.engine, "gemini", canon)
			So(gemini, ShouldContainKey, "authored")
			So(gemini, ShouldContainKey, "from-plugin")

			agy := engine.RenderableHooksForTest(f.engine, "antigravity", canon)
			So(agy, ShouldContainKey, "from-plugin")
		})
	})
}
