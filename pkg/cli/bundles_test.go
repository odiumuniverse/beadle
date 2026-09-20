package cli

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestBundlesCLIStubEndToEnd(t *testing.T) {
	Convey("Given a stub claude CLI on PATH", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))

		binDir := filepath.Join(t.TempDir(), "bin")
		So(os.MkdirAll(binDir, 0o750), ShouldBeNil)

		logPath := filepath.Join(t.TempDir(), "claude.log")
		script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\nexit 0\n"
		So(os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o700), ShouldBeNil) //nolint:gosec // G306: the stub must stay executable

		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		Convey("When the bundle is enabled", func() {
			out, err := runCLI(t, "bundles", "enable", "--host", "claude")
			So(err, ShouldBeNil)

			calls, err := os.ReadFile(logPath) //nolint:gosec // G304: test reads its own temp file
			So(err, ShouldBeNil)

			config, err := os.ReadFile(filepath.Join(home, ".beadle", "config.json")) //nolint:gosec // G304: test reads its own temp file
			So(err, ShouldBeNil)

			Convey("Then it registers through the CLI and disables kinds", func() {
				So(out, ShouldContainSubstring, "enabled")
				So(out, ShouldContainSubstring, "(registered)")

				So(string(calls), ShouldContainSubstring, "plugin marketplace add "+filepath.Join(home, ".beadle", "bundles", "claude"))
				So(string(calls), ShouldContainSubstring, "plugin install beadle-canon@beadle")
				So(string(config), ShouldContainSubstring, `"off"`)

				Convey("And disabling unregisters and restores modes", func() {
					out, err := runCLI(t, "bundles", "disable", "--host", "claude")
					So(err, ShouldBeNil)

					So(out, ShouldContainSubstring, "disabled")

					calls, err := os.ReadFile(logPath) //nolint:gosec // G304: test reads its own temp file
					So(err, ShouldBeNil)
					So(string(calls), ShouldContainSubstring, "plugin uninstall beadle-canon@beadle")
					So(string(calls), ShouldContainSubstring, "plugin marketplace rm beadle")

					config, err := os.ReadFile(filepath.Join(home, ".beadle", "config.json")) //nolint:gosec // G304: test reads its own temp file
					So(err, ShouldBeNil)
					So(string(config), ShouldNotContainSubstring, `"off"`)
				})
			})
		})
	})
}

func TestBundlesCommands(t *testing.T) {
	Convey("Given an initialized vault", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		out, err := runCLI(t, "bundles", "status")
		So(err, ShouldBeNil)

		Convey("When the status and antigravity enable are requested", func() {
			So(out, ShouldContainSubstring, "host")
			So(out, ShouldContainSubstring, "claude")
			So(out, ShouldContainSubstring, "gemini")
			So(out, ShouldContainSubstring, "antigravity")

			out, err = runCLI(t, "bundles", "enable", "--host", "antigravity")
			So(err, ShouldBeNil)
			So(out, ShouldContainSubstring, "generated")
			So(out, ShouldContainSubstring, "ln -s")

			out, err = runCLI(t, "bundles", "status")
			So(err, ShouldBeNil)
			So(out, ShouldContainSubstring, "yes")

			_, err = runCLI(t, "bundles", "enable")
			So(err, ShouldBeError)
			So(err.Error(), ShouldContainSubstring, "--host is required")

			_, err = runCLI(t, "bundles", "enable", "--host", "cursor")
			So(err, ShouldBeError)
			So(err.Error(), ShouldContainSubstring, "unknown bundle host")

			Convey("When antigravity is disabled", func() {
				out, err := runCLI(t, "bundles", "disable", "--host", "antigravity")
				So(err, ShouldBeNil)

				Convey("Then it disables and keeps the generated plugin", func() {
					So(out, ShouldContainSubstring, "disabled")

					_, statErr := os.Stat(filepath.Join(home, ".beadle", "bundles", "antigravity", "plugin.json"))
					So(statErr, ShouldBeNil)
				})
			})
		})
	})
}
