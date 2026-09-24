package engine_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/hooks"
)

// a51ReadDir concatenates every regular file under root, so a bundle layout
// change does not invalidate the assertions.
func a51ReadDir(t *testing.T, root string) string {
	t.Helper()

	var out strings.Builder

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			//nolint:nilerr // an unreadable entry cannot hold the text the walk looks for
			return nil
		}

		data, readErr := os.ReadFile(path) //nolint:gosec // the test reads its own temp vault
		if readErr == nil {
			out.Write(data)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	return out.String()
}

// a51CodexAgent enables and detects Codex so its user-level hooks file renders.
func a51CodexAgent(t *testing.T, f *fixture) {
	t.Helper()

	enableAgents(t, f, agent.CodexID)
	write(t, filepath.Join(f.home, ".codex", "AGENTS.md"), "# r\n")
	write(t, filepath.Join(f.home, ".codex", "config.toml"), "")
}

func TestA51CodexPluginHookReachesClaudeBundle(t *testing.T) {
	Convey("Given a Codex plugin hook, an enabled Claude bundle and an active Codex", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		a51CodexAgent(t, f)

		dir := codexPluginTree(t, f.home, "acme", "tool", "1.0.0")
		write(t, filepath.Join(dir, "hooks", "hooks.json"),
			`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"bash \"${PLUGIN_ROOT}/hook.sh\""}]}]}}`)

		f.sync(t)

		_, err := f.engine.ApprovePluginHooks("acme/tool")
		So(err, ShouldBeNil)

		f.sync(t)

		_, _, report := enableClaude(t, f)
		So(report.Errors(), ShouldBeEmpty)

		pivot := filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current")

		Convey("When the bundle and the Codex file render", func() {
			bundleHooks := a51ReadDir(t, filepath.Join(f.vault.BundlesDir(), "claude"))
			codexHooks := read(t, a36CodexHooks(f))

			Convey("Then Claude gets the hook, pointing at the plugin pivot", func() {
				So(bundleHooks, ShouldContainSubstring, pivot)
				So(bundleHooks, ShouldNotContainSubstring, "${PLUGIN_ROOT}")
			})

			Convey("And the Codex file does not duplicate its own plugin's hook", func() {
				So(codexHooks, ShouldContainSubstring, "echo hi")
				So(codexHooks, ShouldNotContainSubstring, pivot)
			})
		})
	})
}

func TestA51ClaudePluginHookStaysOutOfClaudeBundle(t *testing.T) {
	Convey("Given a Claude plugin hook, an enabled Claude bundle and an active Codex", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		a51CodexAgent(t, f)

		dir := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		write(t, filepath.Join(dir, "hooks", "hooks.json"),
			`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"bash \"${CLAUDE_PLUGIN_ROOT}/hook.sh\""}]}]}}`)

		f.sync(t)

		_, err := f.engine.ApprovePluginHooks("acme/tool")
		So(err, ShouldBeNil)

		f.sync(t)

		_, _, report := enableClaude(t, f)
		So(report.Errors(), ShouldBeEmpty)

		pivot := filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current")

		Convey("When the bundle and the Codex file render", func() {
			bundleHooks := a51ReadDir(t, filepath.Join(f.vault.BundlesDir(), "claude"))
			codexHooks := read(t, a36CodexHooks(f))

			Convey("Then Claude runs its own plugin's hook natively, not from the bundle", func() {
				So(bundleHooks, ShouldContainSubstring, "echo hi")
				So(bundleHooks, ShouldNotContainSubstring, pivot)
			})

			Convey("And the other hosts get it from their files", func() {
				So(codexHooks, ShouldContainSubstring, pivot)
			})
		})
	})
}

func TestA51CursorPluginHookStaysOutOfCursorFile(t *testing.T) {
	Convey("Given a Cursor plugin hook and active Cursor and Codex hosts", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		a47CursorHome(t, f)
		a51CodexAgent(t, f)
		enableAgents(t, f, agent.CursorID)

		dir := filepath.Join(f.home, ".cursor", "plugins", "local", "tool")
		write(t, filepath.Join(dir, ".cursor-plugin", "plugin.json"), `{"name":"tool"}`)
		write(t, filepath.Join(dir, "hooks", "hooks.json"),
			`{"version":1,"hooks":{"preToolUse":[{"command":"cursor-hook.sh"}]}}`)

		f.sync(t)

		_, err := f.engine.ApprovePluginHooks("cursor/tool")
		So(err, ShouldBeNil)

		// The sync runs while the canon still holds tool--pre-tool-1, so the
		// negative assertion below is not vacuous.
		f.sync(t)

		Convey("Then the Cursor file does not run the Cursor plugin's own hook", func() {
			data, readErr := os.ReadFile(a36CursorHooks(f))
			So(readErr == nil && strings.Contains(string(data), "cursor-hook.sh"), ShouldBeFalse)

			Convey("And the other hosts get it from their files", func() {
				So(read(t, a36CodexHooks(f)), ShouldContainSubstring, "cursor-hook.sh")
			})
		})

		Convey("And a canon hook still renders into the Cursor file", func() {
			write(t, f.vault.HooksPath(), `{"notify": {"event": "pre-tool", "command": "echo hi", "timeout": 5}}`)
			f.config.ApproveHook("notify")
			So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

			f.sync(t)

			cursorHooks := read(t, a36CursorHooks(f))
			So(cursorHooks, ShouldContainSubstring, "echo hi")
			So(cursorHooks, ShouldNotContainSubstring, "cursor-hook.sh")
		})
	})
}

func TestA51GeminiPluginHookStaysOutOfGeminiBundle(t *testing.T) {
	Convey("Given a Gemini plugin hook and a Codex plugin hook", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		geminiHome(t, f)
		enableAgents(t, f, agent.GeminiCLIID)

		gemini := geminiExtensionTree(t, f.home, "tool")
		write(t, filepath.Join(gemini, "hooks", "hooks.json"),
			`{"BeforeTool":[{"matcher":"*","hooks":[{"type":"command","command":"gemini-hook.sh"}]}]}`)

		codex := codexPluginTree(t, f.home, "acme", "codex-tool", "1.0.0")
		write(t, filepath.Join(codex, "hooks", "hooks.json"),
			`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"codex-hook.sh"}]}]}}`)

		f.sync(t)

		_, err := f.engine.ApprovePluginHooks("gemini-cli/tool")
		So(err, ShouldBeNil)

		_, err = f.engine.ApprovePluginHooks("acme/codex-tool")
		So(err, ShouldBeNil)

		foundCLI(t)
		fakeRunner(t, &fakeCLI{respond: geminiExtensionsResponder(`[{"name": "beadle-canon"}]`)})

		report, err := f.engine.BundlesEnable(t.Context(), "gemini")
		So(err, ShouldBeNil)
		So(report.Errors(), ShouldBeEmpty)

		Convey("When the Gemini bundle renders", func() {
			bundleHooks := a51ReadDir(t, filepath.Join(f.vault.BundlesDir(), "gemini"))

			Convey("Then Gemini keeps its own plugin's hook and gets the other source's", func() {
				So(bundleHooks, ShouldNotContainSubstring, "gemini-hook.sh")
				So(bundleHooks, ShouldContainSubstring, "codex-hook.sh")
			})
		})
	})
}

func TestA51CodexLegacyInlineHooksAreApproved(t *testing.T) {
	Convey("Given a legacy Codex plugin with inline hooks in .codex-plugin/plugin.json", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		dir := codexPluginTree(t, f.home, "acme", "tool", "1.0.0")
		write(t, filepath.Join(dir, ".codex-plugin", "plugin.json"),
			`{"name":"tool","hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"legacy-hook.sh"}]}]}}`)

		f.sync(t)

		Convey("Then doctor shows the hook awaiting approval", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)
			So(hasIssue(issues, engine.SeverityInfo, "beadle hooks approve --plugin acme/tool"), ShouldBeTrue)
		})

		Convey("When the plugin hooks are approved", func() {
			report, err := f.engine.ApprovePluginHooks("acme/tool")
			So(err, ShouldBeNil)

			canon, err := hooks.Load(f.vault.HooksPath())
			So(err, ShouldBeNil)
			So(canon, ShouldHaveLength, 1)

			hook, ok := canon["tool--pre-tool-1"]
			So(ok, ShouldBeTrue)
			So(hook.Command, ShouldEqual, "legacy-hook.sh")
			So(hook.Source, ShouldEqual, "plugin:acme/tool")
			So(a36HasText(report.Notes, "1 approved"), ShouldBeTrue)
		})
	})
}

func TestA51HookPlaceholdersAcrossSources(t *testing.T) {
	Convey("Given Cursor and Gemini plugins whose hooks use their root placeholders", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		a47CursorHome(t, f)
		geminiHome(t, f)
		enableAgents(t, f, agent.CursorID, agent.GeminiCLIID)

		cursor := filepath.Join(f.home, ".cursor", "plugins", "local", "ctool")
		write(t, filepath.Join(cursor, ".cursor-plugin", "plugin.json"), `{"name":"ctool"}`)
		write(t, filepath.Join(cursor, "hooks", "hooks.json"),
			`{"version":1,"hooks":{"preToolUse":[
				{"command":"bash \"${CURSOR_PLUGIN_ROOT}/hook.sh\""},
				{"command":"bad \"${PLUGIN_ROOT}/x.sh\""}
			]}}`)

		gemini := geminiExtensionTree(t, f.home, "gtool")
		write(t, filepath.Join(gemini, "hooks", "hooks.json"),
			`{"BeforeTool":[{"matcher":"*","hooks":[
				{"type":"command","command":"bash \"${extensionPath}/hook.sh\""},
				{"type":"command","command":"bad \"${workspacePath}/x.sh\""}
			]}]}`)

		f.sync(t)

		Convey("When both plugins' hooks are approved", func() {
			_, err := f.engine.ApprovePluginHooks("cursor/ctool")
			So(err, ShouldBeNil)

			report, err := f.engine.ApprovePluginHooks("gemini-cli/gtool")
			So(err, ShouldBeNil)

			canon, err := hooks.Load(f.vault.HooksPath())
			So(err, ShouldBeNil)

			Convey("Then the roots resolve to the pivots and the workspace path is refused", func() {
				cursorPivot := filepath.Join(f.vault.PluginsDir(), "cursor", "ctool", "current")
				geminiPivot := filepath.Join(f.vault.PluginsDir(), "gemini-cli", "gtool", "current")

				So(canon["ctool--pre-tool-1"].Command, ShouldContainSubstring, cursorPivot)
				So(canon["gtool--pre-tool-1"].Command, ShouldContainSubstring, geminiPivot)
				So(canon, ShouldHaveLength, 2)
				So(a36HasText(report.Warnings, "unsupported fields"), ShouldBeTrue)

				Convey("And a foreign root is refused too", func() {
					So(canon, ShouldNotContainKey, "ctool--pre-tool-2")
				})
			})
		})
	})
}

func TestA51DuplicateSourcesKeepNativeHooks(t *testing.T) {
	Convey("Given the same plugin key installed by Claude Code and Codex", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		a51CodexAgent(t, f)

		claude := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		write(t, filepath.Join(claude, "hooks", "hooks.json"),
			`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"shared-hook.sh"}]}]}}`)

		codex := codexPluginTree(t, f.home, "acme", "tool", "1.0.0")
		write(t, filepath.Join(codex, "hooks", "hooks.json"),
			`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"shared-hook.sh"}]}]}}`)

		write(t, f.vault.HooksPath(), `{"notify": {"event": "pre-tool", "command": "echo hi", "timeout": 5}}`)
		f.config.ApproveHook("notify")
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		f.sync(t)

		_, err := f.engine.ApprovePluginHooks("acme/tool")
		So(err, ShouldBeNil)

		f.sync(t)

		Convey("Then the Codex file keeps the canon hook and skips the plugin's own copy", func() {
			codexHooks := read(t, a36CodexHooks(f))
			So(codexHooks, ShouldContainSubstring, "echo hi")
			So(codexHooks, ShouldNotContainSubstring, "shared-hook.sh")
		})
	})
}

func TestA51AgyPluginHookStaysOutOfAgyBundle(t *testing.T) {
	Convey("Given an Antigravity plugin hook and an enabled Agy bundle", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		enableAgents(t, f, agent.AntigravityCLIID)

		dir := filepath.Join(f.home, ".gemini", "antigravity-cli", "plugins", "tool")
		write(t, filepath.Join(dir, "plugin.json"), `{"name":"tool"}`)
		write(t, filepath.Join(dir, "hooks.json"),
			`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"agy-hook.sh"}]}]}}`)

		f.sync(t)

		_, err := f.engine.ApprovePluginHooks("antigravity-cli/tool")
		So(err, ShouldBeNil)

		canon, err := hooks.Load(f.vault.HooksPath())
		So(err, ShouldBeNil)
		So(canon, ShouldHaveLength, 1)

		foundCLI(t)
		fakeRunner(t, &fakeCLI{})

		report, err := f.engine.BundlesEnable(t.Context(), "antigravity")
		So(err, ShouldBeNil)
		So(report.Errors(), ShouldBeEmpty)

		Convey("Then the Agy bundle keeps its own plugin's hook out", func() {
			bundleHooks := a51ReadDir(t, filepath.Join(f.vault.BundlesDir(), "antigravity"))
			So(bundleHooks, ShouldNotContainSubstring, "agy-hook.sh")
		})
	})
}
