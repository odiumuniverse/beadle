package engine_test

import (
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/hooks"
)

func TestA47HooksApproveFromCodexSource(t *testing.T) {
	Convey("Given a Codex plugin with hooks", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		dir := codexPluginTree(t, f.home, "acme", "tool", "1.0.0")
		write(t, filepath.Join(dir, "hooks", "hooks.json"), `{"hooks":{
			"PreToolUse": [{"matcher":"Bash","hooks":[{"type":"command","command":"bash \"${PLUGIN_ROOT}/hook.sh\"","timeout":30}]}],
			"UserPromptSubmit": [{"hooks":[{"type":"command","command":"ignored.sh"}]}]
		}}`)

		f.sync(t)

		Convey("When the plugin hooks are approved", func() {
			report, err := f.engine.ApprovePluginHooks("acme/tool")
			So(err, ShouldBeNil)

			canon, err := hooks.Load(f.vault.HooksPath())
			So(err, ShouldBeNil)
			So(canon, ShouldHaveLength, 1)

			hook, ok := canon["tool--pre-tool-1"]
			So(ok, ShouldBeTrue)
			So(hook.Event, ShouldEqual, "pre-tool")
			So(hook.Matcher, ShouldEqual, "Bash")
			So(hook.Source, ShouldEqual, "plugin:acme/tool")
			So(hook.Command, ShouldEqual, "bash \""+filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current", "hook.sh")+"\"")

			So(a36HasText(report.Warnings, "skipped 1 hook(s) on unsupported events"), ShouldBeTrue)
			So(a36HasText(report.Notes, "1 approved, 0 refreshed, 0 removed, 1 skipped"), ShouldBeTrue)
			So(f.config.HookApproved("tool--pre-tool-1"), ShouldBeTrue)
		})

		Convey("When a foreign plugin collides with an approved hook", func() {
			_, err := f.engine.ApprovePluginHooks("acme/tool")
			So(err, ShouldBeNil)

			other := geminiExtensionTree(t, f.home, "tool")
			write(t, filepath.Join(other, "hooks", "hooks.json"),
				`{"BeforeTool":[{"matcher":"*","hooks":[{"type":"command","command":"other.sh"}]}]}`)
			write(t, filepath.Join(f.home, ".gemini", "settings.json"), "{}")

			f.sync(t)

			report, err := f.engine.ApprovePluginHooks("gemini-cli/tool")
			So(err, ShouldBeNil)

			canon, err := hooks.Load(f.vault.HooksPath())
			So(err, ShouldBeNil)

			Convey("Then the canon keeps the approved hook and the collision is warned", func() {
				So(canon, ShouldHaveLength, 1)
				So(canon["tool--pre-tool-1"].Source, ShouldEqual, "plugin:acme/tool")
				So(a36HasText(report.Warnings, "already exists in the canon"), ShouldBeTrue)
				So(f.config.HookApproved("tool--pre-tool-1"), ShouldBeTrue)
			})
		})
	})
}

func TestA47HookDriftInForeignSource(t *testing.T) {
	Convey("Given an approved Codex plugin whose hooks changed", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		dir := codexPluginTree(t, f.home, "acme", "tool", "1.0.0")
		write(t, filepath.Join(dir, "hooks", "hooks.json"), `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"first.sh"}]}]}}`)

		f.sync(t)

		_, err := f.engine.ApprovePluginHooks("acme/tool")
		So(err, ShouldBeNil)

		write(t, filepath.Join(dir, "hooks", "hooks.json"), `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"second.sh"}]}]}}`)

		f.sync(t)

		Convey("When the hook definition changes", func() {
			Convey("Then the sync does not silently refresh it", func() {
				canon, err := hooks.Load(f.vault.HooksPath())
				So(err, ShouldBeNil)
				So(canon["tool--stop-1"].Command, ShouldEqual, "first.sh")

				approveReport, err := f.engine.ApprovePluginHooks("acme/tool")
				So(err, ShouldBeNil)
				So(a36HasText(approveReport.Notes, "0 approved, 1 refreshed"), ShouldBeTrue)

				canon, err = hooks.Load(f.vault.HooksPath())
				So(err, ShouldBeNil)
				So(canon["tool--stop-1"].Command, ShouldEqual, "second.sh")
			})
		})
	})
}
