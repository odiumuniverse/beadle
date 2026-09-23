package plugin_test

import (
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/plugin"
)

func TestReadHooksFile(t *testing.T) {
	Convey("Given a plugin with hooks/hooks.json", t, func() {
		dir := t.TempDir()

		writeFile(t, filepath.Join(dir, "hooks", "hooks.json"), `{
  "$schema": "https://example.com/hooks.schema.json",
  "hooks": {
    "PostToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "run.sh", "timeout": 30}]}],
    "Async": [{"hooks": [{"type": "command", "command": "bg.sh", "async": true}]}],
    "Fields": [{"hooks": [{"type": "command", "command": "x.sh", "if": "Bash(rm *)", "shell": "bash", "statusMessage": "x", "args": []}]}]
  }
}`)

		hooks, warns, err := plugin.ReadHooks(dir)
		So(err, ShouldBeNil)
		So(warns, ShouldBeEmpty)

		Convey("Then the definitions carry matcher, command and timeout", func() {
			So(hooks["PostToolUse"], ShouldHaveLength, 1)
			So(hooks["PostToolUse"][0].Matcher, ShouldEqual, "Bash")
			So(hooks["PostToolUse"][0].Hooks[0].Command, ShouldEqual, "run.sh")
			So(hooks["PostToolUse"][0].Hooks[0].Timeout, ShouldEqual, 30)
			So(hooks["PostToolUse"][0].Hooks[0].Unsupported, ShouldBeEmpty)
		})

		Convey("Then fields the canon cannot express are recorded", func() {
			So(hooks["Async"][0].Hooks[0].Unsupported, ShouldResemble, []string{"async"})
			So(hooks["Fields"][0].Hooks[0].Unsupported, ShouldResemble, []string{"if"})
			So(hooks["Fields"][0].Hooks[0].Command, ShouldEqual, "x.sh")
		})
	})

	Convey("Given a plugin with inline manifest hooks", t, func() {
		dir := t.TempDir()

		writeFile(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{
  "name": "caveman",
  "hooks": {
    "SessionStart": [{"hooks": [{"type": "command", "command": "node \"${CLAUDE_PLUGIN_ROOT}/hook.js\"", "timeout": 5, "statusMessage": "Loading"}]}]
  }
}`)

		Convey("When the reader runs", func() {
			hooks, warns, err := plugin.ReadHooks(dir)
			So(err, ShouldBeNil)
			So(warns, ShouldBeEmpty)

			Convey("Then the inline hooks are read", func() {
				So(hooks["SessionStart"], ShouldHaveLength, 1)
				So(hooks["SessionStart"][0].Hooks[0].Command, ShouldEqual, `node "${CLAUDE_PLUGIN_ROOT}/hook.js"`)
				So(hooks["SessionStart"][0].Hooks[0].Timeout, ShouldEqual, 5)
				So(hooks["SessionStart"][0].Hooks[0].Unsupported, ShouldBeEmpty)
			})
		})
	})

	Convey("Given a plugin whose manifest points at a hooks file", t, func() {
		dir := t.TempDir()

		writeFile(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{"name": "x", "hooks": "./config/hooks.json"}`)
		writeFile(t, filepath.Join(dir, "config", "hooks.json"), `{"PostToolUse": [{"hooks": [{"type": "command", "command": "y.sh"}]}]}`)

		Convey("When the reader runs", func() {
			hooks, _, err := plugin.ReadHooks(dir)
			So(err, ShouldBeNil)
			So(hooks["PostToolUse"][0].Hooks[0].Command, ShouldEqual, "y.sh")
		})
	})

	Convey("Given a plugin with both hook locations", t, func() {
		dir := t.TempDir()

		writeFile(t, filepath.Join(dir, "hooks", "hooks.json"), `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "file.sh"}]}]}}`)
		writeFile(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{"name": "x", "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "inline.sh"}]}]}}`)

		Convey("When the reader runs", func() {
			hooks, warns, err := plugin.ReadHooks(dir)
			So(err, ShouldBeNil)

			Convey("Then the file wins with a warning", func() {
				So(hooks["Stop"][0].Hooks[0].Command, ShouldEqual, "file.sh")
				So(warns, ShouldHaveLength, 1)
				So(warns[0], ShouldContainSubstring, "using hooks/hooks.json")
			})
		})
	})

	Convey("Given a plugin without hooks", t, func() {
		Convey("Then the reader is silent", func() {
			hooks, warns, err := plugin.ReadHooks(t.TempDir())
			So(err, ShouldBeNil)
			So(hooks, ShouldBeEmpty)
			So(warns, ShouldBeEmpty)
		})
	})
}

func TestReadHooksGuards(t *testing.T) {
	Convey("Given a plugin with an empty inline hooks map", t, func() {
		dir := t.TempDir()

		writeFile(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{"name": "x", "hooks": {}}`)

		Convey("When the reader runs", func() {
			hooks, warns, err := plugin.ReadHooks(dir)
			So(err, ShouldBeNil)

			Convey("Then it reports no hooks and no warning", func() {
				So(hooks, ShouldBeEmpty)
				So(warns, ShouldBeEmpty)
			})
		})
	})

	Convey("Given a plugin with a non-group event value", t, func() {
		dir := t.TempDir()

		writeFile(t, filepath.Join(dir, "hooks", "hooks.json"),
			`{"hooks": {"Stop": "not-a-list", "SessionStart": [{"hooks": [{"type": "command", "command": "ok.sh"}]}]}}`)

		Convey("When the reader runs", func() {
			hooks, warns, err := plugin.ReadHooks(dir)
			So(err, ShouldBeNil)

			Convey("Then the bad event warns and the good one is read", func() {
				So(hooks["SessionStart"], ShouldHaveLength, 1)
				So(warns, ShouldHaveLength, 1)
				So(warns[0], ShouldContainSubstring, "hook event Stop is not a list of matcher groups")
			})
		})
	})

	Convey("Given a bare document with meta keys", t, func() {
		dir := t.TempDir()

		writeFile(t, filepath.Join(dir, "hooks", "hooks.json"),
			`{"$schema": "https://example.com/x.json", "description": "x", "Stop": [{"hooks": [{"type": "command", "command": "x.sh"}]}]}`)

		Convey("When the reader runs", func() {
			hooks, warns, err := plugin.ReadHooks(dir)
			So(err, ShouldBeNil)

			Convey("Then the meta keys are ignored silently", func() {
				So(hooks["Stop"], ShouldHaveLength, 1)
				So(warns, ShouldBeEmpty)
			})
		})
	})

	Convey("Given a manifest hooks path escaping the plugin root", t, func() {
		dir := t.TempDir()

		writeFile(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{"name": "x", "hooks": "../../escape.json"}`)

		Convey("When the reader runs", func() {
			hooks, warns, err := plugin.ReadHooks(dir)
			So(err, ShouldBeNil)

			Convey("Then the path is refused with a warning", func() {
				So(hooks, ShouldBeEmpty)
				So(warns, ShouldHaveLength, 1)
				So(warns[0], ShouldContainSubstring, "escapes the plugin root")
			})
		})
	})

	Convey("Given an empty or relative install path", t, func() {
		Convey("Then the reader warns instead of resolving from the working directory", func() {
			hooks, warns, err := plugin.ReadHooks("")
			So(err, ShouldBeNil)
			So(hooks, ShouldBeEmpty)
			So(warns, ShouldHaveLength, 1)
			So(warns[0], ShouldContainSubstring, "empty or not absolute")

			hooks, warns, err = plugin.ReadHooks("relative/path")
			So(err, ShouldBeNil)
			So(hooks, ShouldBeEmpty)
			So(warns, ShouldHaveLength, 1)
		})
	})

	Convey("Given an out-of-range timeout", t, func() {
		dir := t.TempDir()

		writeFile(t, filepath.Join(dir, "hooks", "hooks.json"), `{"Stop": [{"hooks": [{"type": "command", "command": "x.sh", "timeout": 1e300}]}]}`)

		Convey("When the reader runs", func() {
			hooks, warns, err := plugin.ReadHooks(dir)
			So(err, ShouldBeNil)

			Convey("Then the timeout is unsupported instead of overflowing", func() {
				So(hooks["Stop"][0].Hooks[0].Unsupported, ShouldResemble, []string{"timeout"})
				So(warns, ShouldBeEmpty)
			})
		})
	})
}
