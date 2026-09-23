package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/permission"
	"github.com/odiumuniverse/beadle/pkg/secret"
)

func TestPluginPinRoundTrip(t *testing.T) {
	Convey("Given a config with per-agent plugin pins", t, func() {
		path := filepath.Join(t.TempDir(), config.FileName)

		cfg := config.Default()
		So(cfg.SetPluginPin("claude-code", "acme/tool", "1.0.0"), ShouldBeNil)
		So(cfg.SetPluginPin("opencode", "acme/tool", "2.1.0"), ShouldBeNil)
		So(cfg.Save(path), ShouldBeNil)

		loaded, err := config.Load(path)
		So(err, ShouldBeNil)

		Convey("When pins are read back", func() {
			version, ok := loaded.PluginPin("claude-code", "acme/tool")
			So(ok, ShouldBeTrue)
			So(version, ShouldEqual, "1.0.0")

			version, ok = loaded.PluginPin("opencode", "acme/tool")
			So(ok, ShouldBeTrue)
			So(version, ShouldEqual, "2.1.0")

			_, ok = loaded.PluginPin("gemini-cli", "acme/tool")
			So(ok, ShouldBeFalse)
		})

		Convey("When a pin is unset and the config is saved again", func() {
			loaded.UnsetPluginPin("claude-code", "acme/tool")

			_, ok := loaded.PluginPin("claude-code", "acme/tool")
			So(ok, ShouldBeFalse)

			So(loaded.Save(path), ShouldBeNil)

			reloaded, err := config.Load(path)

			Convey("Then only the remaining pin survives", func() {
				So(err, ShouldBeNil)
				So(reloaded.Agents["opencode"].PluginPins, ShouldResemble, map[string]string{"acme/tool": "2.1.0"})
				So(reloaded.Agents["claude-code"].PluginPins, ShouldBeNil)
			})
		})
	})
}

func TestPluginPinValidation(t *testing.T) {
	Convey("Given a table of invalid plugin keys", t, func() {
		bad := map[string]string{
			"missing marketplace": "",
			"no slash":            "acme",
			"empty marketplace":   "/tool",
			"empty name":          "acme/",
			"nested name":         "acme/tool/extra",
			"absolute":            "/acme/tool",
			"dotdot marketplace":  "../acme/tool",
			"dotdot name":         "acme/../tool",
		}

		for name, key := range bad {
			Convey("When the key is "+name, func() {
				Convey("Then it is rejected", func() {
					So(config.ValidatePluginPin(key, "1.0.0"), ShouldBeError)
				})
			})
		}
	})

	Convey("Given a table of invalid versions", t, func() {
		badVersions := map[string]string{
			"empty":     "",
			"slash":     "1.0/0",
			"traversal": "..",
			"space":     "1 0",
			"unicode":   "версия",
		}

		for name, version := range badVersions {
			Convey("When the version is "+name, func() {
				Convey("Then it is rejected", func() {
					So(config.ValidatePluginPin("acme/tool", version), ShouldBeError)
				})
			})
		}
	})

	Convey("Given a valid key and a mixed version", t, func() {
		Convey("When they are validated", func() {
			Convey("Then the valid pair passes and the bad ones fail", func() {
				So(config.ValidatePluginPin("acme/tool", "v1.2.3-beta_4"), ShouldBeNil)

				cfg := config.Default()
				So(cfg.SetPluginPin("claude-code", "acme", "1.0.0"), ShouldBeError)
				So(cfg.SetPluginPin("claude-code", "acme/tool", "../1.0.0"), ShouldBeError)
			})
		})
	})
}

func TestDefaultMaterializesScalars(t *testing.T) {
	Convey("Given the default config", t, func() {
		cfg := config.Default()

		Convey("When its scalars are read", func() {
			Convey("Then every optional setting is explicit", func() {
				So(cfg.Version, ShouldEqual, config.CurrentVersion)
				So(cfg.Permissions, ShouldEqual, permission.ModeSync)
				So(cfg.History, ShouldEqual, config.HistoryGit)
				So(cfg.Secrets, ShouldEqual, secret.ModeLiteral)
				So(cfg.Agents, ShouldNotBeNil)
			})
		})

		Convey("When it is saved and loaded", func() {
			path := filepath.Join(t.TempDir(), config.FileName)
			So(cfg.Save(path), ShouldBeNil)

			data, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
			So(err, ShouldBeNil)

			loaded, err := config.Load(path)

			Convey("Then the file shows the scalars and they round-trip", func() {
				So(err, ShouldBeNil)

				for _, want := range []string{`"permissions": "sync"`, `"history": "git"`, `"secrets": "literal"`} {
					So(string(data), ShouldContainSubstring, want)
				}

				So(loaded.Permissions, ShouldEqual, permission.ModeSync)
				So(loaded.History, ShouldEqual, config.HistoryGit)
				So(loaded.Secrets, ShouldEqual, secret.ModeLiteral)
			})
		})
	})
}

func TestNormalizeFillsEmptyScalars(t *testing.T) {
	Convey("Given a legacy config without scalar values", t, func() {
		path := filepath.Join(t.TempDir(), config.FileName)
		So(os.WriteFile(path, []byte(`{"version":1,"agents":{}}`), 0o600), ShouldBeNil)

		Convey("When it is loaded", func() {
			cfg, err := config.Load(path)
			So(err, ShouldBeNil)

			Convey("Then the defaults are materialized in memory and the config is migrated", func() {
				So(cfg.Version, ShouldEqual, config.CurrentVersion)
				So(cfg.Permissions, ShouldEqual, permission.ModeSync)
				So(cfg.History, ShouldEqual, config.HistoryGit)
				So(cfg.Secrets, ShouldEqual, secret.ModeLiteral)

				Convey("And a save writes them out", func() {
					So(cfg.Save(path), ShouldBeNil)

					data, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
					So(err, ShouldBeNil)
					So(string(data), ShouldContainSubstring, `"history": "git"`)
					So(string(data), ShouldContainSubstring, `"permissions": "sync"`)
					So(string(data), ShouldContainSubstring, `"secrets": "literal"`)
				})
			})
		})

		Convey("When explicit empty scalars are loaded", func() {
			So(os.WriteFile(path, []byte(`{"version":2,"permissions":"","history":"","secrets":""}`), 0o600), ShouldBeNil)

			cfg, err := config.Load(path)

			Convey("Then they fall back to the defaults too", func() {
				So(err, ShouldBeNil)
				So(cfg.Permissions, ShouldEqual, permission.ModeSync)
				So(cfg.History, ShouldEqual, config.HistoryGit)
				So(cfg.Secrets, ShouldEqual, secret.ModeLiteral)
			})
		})

		Convey("When non-default scalars are loaded", func() {
			So(os.WriteFile(path, []byte(`{"version":2,"permissions":"sync","history":"off","secrets":"env"}`), 0o600), ShouldBeNil)

			cfg, err := config.Load(path)

			Convey("Then they are preserved exactly", func() {
				So(err, ShouldBeNil)
				So(cfg.Permissions, ShouldEqual, permission.ModeSync)
				So(cfg.History, ShouldEqual, config.HistoryOff)
				So(cfg.Secrets, ShouldEqual, secret.ModeEnv)
			})
		})

		Convey("When Normalize runs on a zero config", func() {
			cfg := &config.Config{}
			cfg.Normalize()

			Convey("Then every scalar gets its default", func() {
				So(cfg.Version, ShouldEqual, config.CurrentVersion)
				So(cfg.Permissions, ShouldEqual, permission.ModeSync)
				So(cfg.History, ShouldEqual, config.HistoryGit)
				So(cfg.Secrets, ShouldEqual, secret.ModeLiteral)
				So(cfg.Agents, ShouldNotBeNil)
			})
		})
	})
}

func TestLoadMigratesConfigV2(t *testing.T) {
	Convey("Given a v2 config with the legacy permission scalar off", t, func() {
		path := filepath.Join(t.TempDir(), config.FileName)

		const legacy = `{
  "version": 2,
  "permissions": "off",
  "history": "git",
  "secrets": "literal",
  "agents": {"claude-code": {"enabled": true}}
}
`
		So(os.WriteFile(path, []byte(legacy), 0o600), ShouldBeNil)

		Convey("When it is loaded", func() {
			cfg, err := config.Load(path)
			So(err, ShouldBeNil)

			Convey("Then the v3 defaults are enabled in memory and reported", func() {
				So(cfg.Version, ShouldEqual, config.CurrentVersion)
				So(cfg.KindEnabled(kind.Permissions), ShouldBeTrue)
				So(cfg.Kinds[kind.Permissions], ShouldEqual, config.ModeSync)
				So(cfg.Permissions, ShouldEqual, permission.ModeSync)
				So(cfg.Agents[config.SharedAgentID].Enabled, ShouldBeTrue)

				notes := strings.Join(cfg.MigrationNotes(), "\n")
				So(cfg.MigrationNotes(), ShouldHaveLength, 2)
				So(notes, ShouldContainSubstring, "permissions")
				So(notes, ShouldContainSubstring, "shared skills surface")
			})

			Convey("And the read alone writes nothing", func() {
				data, readErr := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
				So(readErr, ShouldBeNil)
				So(string(data), ShouldEqual, legacy)
			})

			Convey("When the command saves it, the file carries the new version and the flips", func() {
				So(cfg.Save(path), ShouldBeNil)

				data, readErr := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
				So(readErr, ShouldBeNil)
				So(string(data), ShouldContainSubstring, `"version": 3`)
				So(string(data), ShouldContainSubstring, `"permissions": "sync"`)
				So(string(data), ShouldContainSubstring, `"kinds"`)
				So(string(data), ShouldContainSubstring, `"shared"`)

				Convey("And a second load is a no-op without notes or writes", func() {
					info, statErr := os.Stat(path)
					So(statErr, ShouldBeNil)

					beforeTime := info.ModTime()

					again, err := config.Load(path)
					So(err, ShouldBeNil)
					So(again.MigrationNotes(), ShouldBeEmpty)

					after, readErr := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
					So(readErr, ShouldBeNil)
					So(string(after), ShouldEqual, string(data))

					afterInfo, statErr := os.Stat(path)
					So(statErr, ShouldBeNil)
					So(afterInfo.ModTime().Equal(beforeTime), ShouldBeTrue)
				})
			})

			Convey("And taking the notes reports every flip once", func() {
				So(cfg.TakeMigrationNotes(), ShouldHaveLength, 2)
				So(cfg.MigrationNotes(), ShouldBeEmpty)
			})
		})
	})
}

func TestMigrateMissingVersion(t *testing.T) {
	Convey("Given a legacy config without a version field", t, func() {
		path := filepath.Join(t.TempDir(), config.FileName)
		So(os.WriteFile(path, []byte(`{"agents":{}}`), 0o600), ShouldBeNil)

		Convey("When it is loaded", func() {
			cfg, err := config.Load(path)
			So(err, ShouldBeNil)

			Convey("Then it is treated as older and migrated", func() {
				So(cfg.Version, ShouldEqual, config.CurrentVersion)
				So(cfg.Kinds[kind.Permissions], ShouldEqual, config.ModeSync)
				So(cfg.Agents[config.SharedAgentID].Enabled, ShouldBeTrue)
				So(cfg.MigrationNotes(), ShouldHaveLength, 2)
			})
		})
	})
}

func TestMigrationKeepsExplicitOff(t *testing.T) {
	Convey("Given a v2 config with explicit opt-outs", t, func() {
		path := filepath.Join(t.TempDir(), config.FileName)

		const legacy = `{
  "version": 2,
  "permissions": "off",
  "history": "git",
  "secrets": "literal",
  "kinds": {"permissions": "off"},
  "agents": {"shared": {"enabled": false}, "claude-code": {"enabled": true}}
}
`
		So(os.WriteFile(path, []byte(legacy), 0o600), ShouldBeNil)

		Convey("When it is loaded", func() {
			cfg, err := config.Load(path)
			So(err, ShouldBeNil)

			Convey("Then the explicit choices survive the in-memory migration", func() {
				So(cfg.Version, ShouldEqual, config.CurrentVersion)
				So(cfg.KindEnabled(kind.Permissions), ShouldBeFalse)
				So(cfg.Kinds[kind.Permissions], ShouldEqual, config.ModeOff)
				So(cfg.Permissions, ShouldEqual, permission.ModeOff)
				So(cfg.Agents[config.SharedAgentID].Enabled, ShouldBeFalse)
				So(cfg.MigrationNotes(), ShouldBeEmpty)

				data, readErr := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
				So(readErr, ShouldBeNil)
				So(string(data), ShouldEqual, legacy)
			})
		})
	})
}

func TestLoadRejectsNewerConfigVersion(t *testing.T) {
	Convey("Given a config written by a newer beadle", t, func() {
		path := filepath.Join(t.TempDir(), config.FileName)
		So(os.WriteFile(path, []byte(`{"version": 4}`), 0o600), ShouldBeNil)

		Convey("When it is loaded", func() {
			_, err := config.Load(path)

			Convey("Then loading fails instead of guessing", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "version 4")
			})
		})
	})
}

func TestLoadRejectsInvalidPin(t *testing.T) {
	Convey("Given a config file with an invalid pinned version", t, func() {
		path := filepath.Join(t.TempDir(), config.FileName)
		So(os.WriteFile(path, []byte(`{
  "version": 2,
  "agents": {"claude-code": {"enabled": true, "plugin_pins": {"acme/tool": "../evil"}}}
}`), 0o600), ShouldBeNil)

		Convey("When it is loaded", func() {
			_, err := config.Load(path)

			Convey("Then loading fails", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "invalid version")
			})
		})
	})
}
