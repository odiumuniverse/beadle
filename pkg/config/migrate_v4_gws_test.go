package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// v3Vault is a config written by the build that still used the historical
// agent ids, with every id the rename touches in the agents map.
const v3Vault = `{
  "version": 3,
  "permissions": "sync",
  "history": "git",
  "secrets": "literal",
  "kinds": {"permissions": "sync", "skills": "sync"},
  "agents": {
    "claude-code": {"enabled": true, "modes": {"mcp": "sync"}, "plugin_pins": {"acme/tool": "1.0.0"}},
    "gemini-cli": {"enabled": true, "modes": {"skills": "off"}},
    "antigravity-cli": {"enabled": false},
    "deepseek-harness": {"enabled": true},
    "opencode": {"enabled": true},
    "cursor": {"enabled": true},
    "codex": {"enabled": true},
    "pi": {"enabled": true},
    "kilo": {"enabled": true},
    "omp": {"enabled": true},
    "shared": {"enabled": true}
  }
}
`

func TestLoadMigratesConfigV3AgentIDs(t *testing.T) {
	Convey("Given a v3 config keyed by the historical agent ids", t, func() {
		path := filepath.Join(t.TempDir(), config.FileName)
		So(os.WriteFile(path, []byte(v3Vault), 0o600), ShouldBeNil)

		Convey("When it is loaded", func() {
			cfg, err := config.Load(path)
			So(err, ShouldBeNil)

			Convey("Then every renamed agent carries its entry under the canonical id", func() {
				So(cfg.Agents["claude"].Enabled, ShouldBeTrue)
				So(cfg.Agents["claude"].Modes[kind.MCP], ShouldEqual, config.ModeSync)
				So(cfg.Agents["claude"].PluginPins["acme/tool"], ShouldEqual, "1.0.0")
				So(cfg.Agents["gemini"].Modes[kind.Skills], ShouldEqual, config.ModeOff)
				So(cfg.Agents["agy"].Enabled, ShouldBeFalse)
				So(cfg.Agents["dsh"].Enabled, ShouldBeTrue)
			})

			Convey("And the historical ids are gone", func() {
				for _, old := range []string{"claude-code", "gemini-cli", "antigravity-cli", "deepseek-harness"} {
					_, ok := cfg.Agents[old]
					So(ok, ShouldBeFalse)
				}
			})

			Convey("And the ids the rename does not touch keep their entries", func() {
				for _, id := range []string{"opencode", "cursor", "codex", "pi", "kilo", "omp", "shared"} {
					_, ok := cfg.Agents[id]
					So(ok, ShouldBeTrue)
				}
			})

			Convey("And one note is reported per renamed id", func() {
				notes := strings.Join(cfg.MigrationNotes(), "\n")
				So(cfg.MigrationNotes(), ShouldHaveLength, 4)
				So(notes, ShouldContainSubstring, "claude-code")
				So(notes, ShouldContainSubstring, "claude")
				So(notes, ShouldContainSubstring, "gemini-cli")
				So(notes, ShouldContainSubstring, "gemini")
				So(notes, ShouldContainSubstring, "antigravity-cli")
				So(notes, ShouldContainSubstring, "agy")
				So(notes, ShouldContainSubstring, "deepseek-harness")
				So(notes, ShouldContainSubstring, "dsh")
			})

			Convey("And the read alone writes nothing", func() {
				data, readErr := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
				So(readErr, ShouldBeNil)
				So(string(data), ShouldEqual, v3Vault)
			})

			Convey("When the command saves it, the file carries the canonical ids", func() {
				So(cfg.Save(path), ShouldBeNil)

				data, readErr := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
				So(readErr, ShouldBeNil)
				So(string(data), ShouldContainSubstring, `"version": 4`)
				So(string(data), ShouldContainSubstring, `"claude": {`)
				So(string(data), ShouldNotContainSubstring, `"claude-code"`)

				Convey("And a second load is a no-op without notes or writes", func() {
					info, statErr := os.Stat(path)
					So(statErr, ShouldBeNil)

					beforeTime := info.ModTime()

					again, err := config.Load(path)
					So(err, ShouldBeNil)
					So(again.MigrationNotes(), ShouldBeEmpty)
					So(again.Migrated(), ShouldBeFalse)

					after, readErr := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
					So(readErr, ShouldBeNil)
					So(string(after), ShouldEqual, string(data))

					afterInfo, statErr := os.Stat(path)
					So(statErr, ShouldBeNil)
					So(afterInfo.ModTime().Equal(beforeTime), ShouldBeTrue)
				})
			})
		})
	})
}

func TestLoadMigratesConfigV3Idempotently(t *testing.T) {
	Convey("Given a v3 config that was already migrated once in memory", t, func() {
		path := filepath.Join(t.TempDir(), config.FileName)
		So(os.WriteFile(path, []byte(v3Vault), 0o600), ShouldBeNil)

		cfg, err := config.Load(path)
		So(err, ShouldBeNil)
		So(cfg.Save(path), ShouldBeNil)

		Convey("When it is loaded twice more", func() {
			second, err := config.Load(path)
			So(err, ShouldBeNil)
			So(second.MigrationNotes(), ShouldBeEmpty)

			So(second.Save(path), ShouldBeNil)

			third, err := config.Load(path)
			So(err, ShouldBeNil)

			Convey("Then the agents map is stable", func() {
				So(third.Agents, ShouldResemble, second.Agents)
				So(third.MigrationNotes(), ShouldBeEmpty)
			})
		})
	})
}

func TestLoadKeepsV4ConfigUntouched(t *testing.T) {
	Convey("Given a v4 config already keyed by the canonical ids", t, func() {
		path := filepath.Join(t.TempDir(), config.FileName)
		So(os.WriteFile(path, []byte(`{"version":4,"permissions":"sync","history":"git","secrets":"literal","agents":{"claude":{"enabled":true}}}`), 0o600), ShouldBeNil)

		Convey("When it is loaded", func() {
			cfg, err := config.Load(path)
			So(err, ShouldBeNil)

			Convey("Then nothing is migrated and nothing is reported", func() {
				So(cfg.Migrated(), ShouldBeFalse)
				So(cfg.MigrationNotes(), ShouldBeEmpty)
				So(cfg.Agents["claude"].Enabled, ShouldBeTrue)
			})
		})
	})
}

func TestLoadRejectsConfigNewerThanCurrent(t *testing.T) {
	Convey("Given a config written by a newer beadle", t, func() {
		path := filepath.Join(t.TempDir(), config.FileName)
		So(os.WriteFile(path, []byte(`{"version":5,"agents":{}}`), 0o600), ShouldBeNil)

		Convey("When it is loaded", func() {
			cfg, err := config.Load(path)

			Convey("Then it is refused instead of misread", func() {
				So(cfg, ShouldBeNil)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "version 5")
			})
		})
	})
}
