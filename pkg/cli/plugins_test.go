package cli

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestPluginsPinFromForeignSource(t *testing.T) {
	Convey("Given a pinned Codex plugin", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		codex := filepath.Join(home, ".codex", "plugins", "cache", "acme", "tool", "1.0.0")
		writeFile(t, filepath.Join(codex, "plugin.json"), `{"name":"tool","version":"1.0.0"}`)

		_, err = runCLI(t, "plugins", "pin", "acme/tool", "1.0.0", "--agent", "opencode")
		So(err, ShouldBeNil)

		out, err := runCLI(t, "plugins", "pins")

		Convey("Then the pin is reported as having no effect", func() {
			So(err, ShouldBeNil)
			So(out, ShouldContainSubstring, "acme/tool")
			So(out, ShouldContainSubstring, "no-effect")
		})
	})
}

func TestPluginsPinCommands(t *testing.T) {
	Convey("Given an initialized vault", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		Convey("When a plugin is pinned", func() {
			out, err := runCLI(t, "plugins", "pin", "acme/tool", "1.0.0", "--agent", "opencode")

			Convey("Then it is listed as unknown until the cache exists", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "opencode: acme/tool pinned to 1.0.0")

				out, err := runCLI(t, "plugins", "pins")
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "opencode")
				So(out, ShouldContainSubstring, "acme/tool")
				So(out, ShouldContainSubstring, "unknown-plugin")

				Convey("And once the cache is installed it is ok", func() {
					cache := filepath.Join(home, ".claude", "plugins", "cache", "acme", "tool", "1.0.0")
					So(os.MkdirAll(cache, 0o750), ShouldBeNil)

					writeFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), `{
  "plugins": {"tool@acme": [{"scope": "user", "installPath": "`+cache+`", "version": "1.0.0"}]}
}`)

					out, err := runCLI(t, "plugins", "pins")
					So(err, ShouldBeNil)
					So(out, ShouldContainSubstring, "ok")

					out, err = runCLI(t, "plugins", "pins", "--agent", "claude-code")
					So(err, ShouldBeNil)
					So(out, ShouldContainSubstring, "no plugin pins")

					_, err = runCLI(t, "plugins", "pin", "acme/tool", "../bad", "--agent", "opencode")
					So(err, ShouldBeError)
					So(err.Error(), ShouldContainSubstring, "invalid version")

					_, err = runCLI(t, "plugins", "pin", "acme/tool", "1.0.0", "--agent", "ghost")
					So(err, ShouldBeError)
					So(err.Error(), ShouldContainSubstring, "unknown agent")

					_, err = runCLI(t, "plugins", "pin", "acme/tool", "1.0.0")
					So(err, ShouldBeError)
					So(err.Error(), ShouldContainSubstring, "--agent is required")

					out, err = runCLI(t, "plugins", "unpin", "acme/tool", "--agent", "opencode")
					So(err, ShouldBeNil)
					So(out, ShouldContainSubstring, "opencode: acme/tool unpinned")

					out, err = runCLI(t, "plugins", "pins")
					So(err, ShouldBeNil)
					So(out, ShouldContainSubstring, "no plugin pins")
				})
			})
		})
	})
}
