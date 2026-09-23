package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestHooksApprovePlugin(t *testing.T) {
	Convey("Given an initialized vault and an installed plugin with hooks", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		install := filepath.Join(home, ".claude", "plugins", "cache", "acme", "tool", "1.0.0")
		writeFile(t, filepath.Join(install, "hooks", "hooks.json"),
			`{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "bash \"${CLAUDE_PLUGIN_ROOT}/start.sh\"", "timeout": 5}]}]}}`)
		writeFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"),
			fmt.Sprintf(`{"plugins":{"tool@acme":[{"scope":"user","installPath":%q,"version":"1.0.0"}]}}`, install))

		pivot := filepath.Join(home, ".beadle", "plugins", "acme", "tool", "current")
		So(os.MkdirAll(filepath.Dir(pivot), 0o700), ShouldBeNil)
		So(os.Symlink(install, pivot), ShouldBeNil)

		Convey("When the plugin is approved by key", func() {
			out, err := runCLI(t, "hooks", "approve", "--plugin", "acme/tool")
			So(err, ShouldBeNil)
			So(out, ShouldContainSubstring, "acme/tool: 1 approved, 0 refreshed, 0 removed, 0 skipped")

			Convey("Then the canon lists the hook with its source and approval", func() {
				out, err := runCLI(t, "hooks", "list")
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "tool--session-start-1")
				So(out, ShouldContainSubstring, "approved")
				So(out, ShouldContainSubstring, "plugin:acme/tool")
			})

			Convey("Then revoking leaves the hook pending until it is removed", func() {
				_, err := runCLI(t, "hooks", "revoke", "tool--session-start-1")
				So(err, ShouldBeNil)

				out, err := runCLI(t, "hooks", "list")
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "tool--session-start-1")
				So(out, ShouldContainSubstring, "pending")

				_, err = runCLI(t, "hooks", "rm", "tool--session-start-1")
				So(err, ShouldBeNil)

				out, err = runCLI(t, "hooks", "list")
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "no hooks")
			})
		})

		Convey("When the plugin key is unknown", func() {
			_, err := runCLI(t, "hooks", "approve", "--plugin", "ghost/plugin")
			So(err, ShouldBeError)
			So(err.Error(), ShouldContainSubstring, "not installed")
		})

		Convey("When a name and --plugin are combined", func() {
			_, err := runCLI(t, "hooks", "approve", "notify", "--plugin", "acme/tool")
			So(err, ShouldBeError)
			So(err.Error(), ShouldContainSubstring, "either a hook name or --plugin")
		})
	})
}
