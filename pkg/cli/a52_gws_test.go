package cli

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/hooks"
)

func TestA52RevokePluginHookModules(t *testing.T) {
	Convey("Given a vault with an approved omp hook module", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		configPath := filepath.Join(home, ".beadle", "config.json")

		cfg, err := config.Load(configPath)
		So(err, ShouldBeNil)

		moduleKey := hooks.HookModuleKey("acme/tool", "pre", "a.ts", "digest")
		cfg.ApproveHook(moduleKey)
		So(cfg.Save(configPath), ShouldBeNil)

		Convey("When the plugin's modules are revoked", func() {
			out, err := runCLI(t, "hooks", "revoke", "--plugin", "acme/tool")
			So(err, ShouldBeNil)
			So(out, ShouldContainSubstring, "revoked 0 hook(s) and 1 hook module approval(s) of plugin acme/tool")

			Convey("Then the approval is gone", func() {
				reloaded, err := config.Load(configPath)
				So(err, ShouldBeNil)
				So(reloaded.HookApproved(moduleKey), ShouldBeFalse)
				So(reloaded.ApprovedHooks, ShouldBeEmpty)
			})
		})
	})
}

func TestA52RevokePluginHooks(t *testing.T) {
	Convey("Given a vault with an approved plugin hook", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		hooksPath := filepath.Join(home, ".beadle", "hooks", "hooks.json")

		So(os.MkdirAll(filepath.Dir(hooksPath), 0o700), ShouldBeNil)
		So(os.WriteFile(hooksPath,
			[]byte(`{"tool-hook":{"event":"post-tool","matcher":"Bash","command":"echo hi","source":"plugin:acme/tool"}}`),
			0o600), ShouldBeNil)

		_, err = runCLI(t, "hooks", "approve", "tool-hook")
		So(err, ShouldBeNil)

		Convey("When the plugin's hooks are revoked", func() {
			out, err := runCLI(t, "hooks", "revoke", "--plugin", "acme/tool")
			So(err, ShouldBeNil)
			So(out, ShouldContainSubstring, "revoked 1 hook(s) and 0 hook module approval(s) of plugin acme/tool")

			Convey("Then the canon entry and the approval are gone, and a repeat revoke is quiet", func() {
				out, err := runCLI(t, "hooks", "list")
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "no hooks")

				out, err = runCLI(t, "hooks", "revoke", "--plugin", "acme/tool")
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "no hooks of plugin acme/tool")
			})
		})

		Convey("When a name and --plugin are both given", func() {
			_, err := runCLI(t, "hooks", "revoke", "tool-hook", "--plugin", "acme/tool")
			So(err, ShouldBeError)
			So(err.Error(), ShouldContainSubstring, "choose either")
		})

		Convey("When no name and no --plugin are given", func() {
			_, err := runCLI(t, "hooks", "revoke")
			So(err, ShouldBeError)
			So(err.Error(), ShouldContainSubstring, "a hook name is required")
		})
	})
}
