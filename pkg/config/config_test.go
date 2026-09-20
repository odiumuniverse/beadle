package config_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/config"
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
