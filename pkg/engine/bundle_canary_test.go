package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/state"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

// canaryGuard skips a canary test unless the real-CLI run is explicitly
// enabled and the host binary is on PATH: the ordinary test run never
// launches a real host CLI.
func canaryGuard(t *testing.T, binary string) {
	t.Helper()

	if os.Getenv("BEADLE_BUNDLE_CANARY") != "1" {
		t.Skip("set BEADLE_BUNDLE_CANARY=1 to install the bundle with the real " + binary + " CLI")
	}

	if _, err := exec.LookPath(binary); err != nil {
		t.Skip(binary + " CLI not found")
	}
}

// canaryHome points HOME and every XDG directory at a disposable tree, so
// the real host CLI never reads or writes the machine's own configuration.
func canaryHome(t *testing.T) string {
	t.Helper()

	home := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	return home
}

// canaryEngine builds a disposable vault with one skill, one server and one
// approved hook for the given hosts.
func canaryEngine(t *testing.T, home string, enable ...string) (*engine.Engine, *vault.Vault) {
	t.Helper()

	v := vault.New(filepath.Join(t.TempDir(), "vault"))
	if err := v.Init(); err != nil {
		t.Fatalf("init vault: %v", err)
	}

	cfg, err := config.Load(v.ConfigPath())
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	for _, id := range enable {
		cfg.Enable(id)
	}

	cfg.ApproveHook("notify")

	if err := cfg.Save(v.ConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	e, err := engine.New(v, cfg, agent.All(home, t.TempDir()), engine.WithHome(home))
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	write(t, filepath.Join(v.SkillsDir(), "alpha", "SKILL.md"), "---\nname: alpha\ndescription: canary skill\n---\n\n# alpha\n")
	write(t, v.ServersPath(), `{"plug": {"transport": "stdio", "command": ["node", "srv.js"]}}`)
	write(t, v.HooksPath(), `{"notify": {"event": "session-start", "command": "echo hi", "timeout": 5}}`)

	return e, v
}

// canaryTeardown unregisters whatever the canary registered; it runs even
// when an assertion failed, so a disposable HOME never keeps a host
// registration behind. A teardown that cannot unregister fails the test.
func canaryTeardown(t *testing.T, e *engine.Engine, host string) {
	t.Helper()

	t.Cleanup(func() {
		report, err := e.BundlesDisable(context.Background(), host)
		if err != nil {
			t.Errorf("canary teardown for %s: %v", host, err)

			return
		}

		for _, result := range report.Bundles {
			if result.Action == "failed" {
				t.Errorf("canary teardown for %s did not unregister: %s", host, result.Note)
			}
		}

		for _, warning := range report.Warnings {
			t.Errorf("canary teardown for %s: %s", host, warning)
		}
	})
}

// canaryState returns the stored bundle record of one host.
func canaryState(t *testing.T, v *vault.Vault, host string) state.BundleState {
	t.Helper()

	st, err := state.Load(v.StatePath())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}

	return st.Bundles[host]
}

// TestBundleCanaryRealClaude installs the rendered bundle with the real
// Claude Code binary into a disposable HOME and CLAUDE_CONFIG_DIR. It never
// touches the machine's own configuration.
func TestBundleCanaryRealClaude(t *testing.T) {
	canaryGuard(t, "claude")

	Convey("Given a disposable HOME and config dir", t, func() {
		home := canaryHome(t)

		t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))

		e, v := canaryEngine(t, home, agent.ClaudeCodeID)

		canaryTeardown(t, e, "claude")

		report, err := e.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

		Convey("When the real CLI installs the bundle", func() {
			Convey("Then the probe is executed and the host lists no errors", func() {
				So(report.Bundles, ShouldHaveLength, 1)
				So(report.Bundles[0].Registered, ShouldBeTrue)
				So(report.Bundles[0].Tier, ShouldEqual, state.VerifyExecuted)
				So(canaryState(t, v, "claude").VerifyTier, ShouldEqual, state.VerifyExecuted)

				out, err := exec.CommandContext(t.Context(), "claude", "plugin", "list", "--json").Output()
				So(err, ShouldBeNil)

				var entries []struct {
					ID      string   `json:"id"`
					Enabled bool     `json:"enabled"`
					Errors  []string `json:"errors"`
				}

				So(json.Unmarshal(out, &entries), ShouldBeNil)

				found := false

				for _, entry := range entries {
					if entry.ID != "beadle-canon@beadle" {
						continue
					}

					found = true

					So(entry.Enabled, ShouldBeTrue)
					So(entry.Errors, ShouldBeEmpty)
				}

				So(found, ShouldBeTrue)
				So(os.Getenv("HOME"), ShouldEqual, home)
			})
		})
	})
}

// TestBundleCanaryRealGemini links the rendered extension with the real
// Gemini CLI into a disposable HOME and lists it back.
func TestBundleCanaryRealGemini(t *testing.T) {
	canaryGuard(t, "gemini")

	Convey("Given a disposable HOME and the gemini CLI", t, func() {
		home := canaryHome(t)

		e, v := canaryEngine(t, home, agent.GeminiCLIID)

		canaryTeardown(t, e, "gemini")

		report, err := e.BundlesEnable(t.Context(), "gemini")
		So(err, ShouldBeNil)

		Convey("When the real CLI links the extension", func() {
			Convey("Then the probe is executed and the host lists the extension", func() {
				So(report.Bundles, ShouldHaveLength, 1)
				So(report.Bundles[0].Action, ShouldEqual, "enabled")
				So(report.Bundles[0].Registered, ShouldBeTrue)
				So(report.Bundles[0].Tier, ShouldEqual, state.VerifyExecuted)
				So(canaryState(t, v, "gemini").VerifyTier, ShouldEqual, state.VerifyExecuted)

				out, err := exec.CommandContext(t.Context(), "gemini", "extensions", "list", "--output-format", "json").Output()
				So(err, ShouldBeNil)
				So(string(out), ShouldContainSubstring, bundle.PluginName)

				Convey("And disabling unlinks the extension", func() {
					disable, err := e.BundlesDisable(t.Context(), "gemini")
					So(err, ShouldBeNil)
					So(disable.Bundles, ShouldHaveLength, 1)
					So(disable.Bundles[0].Action, ShouldEqual, "disabled")

					after, err := exec.CommandContext(t.Context(), "gemini", "extensions", "list", "--output-format", "json").Output()
					So(err, ShouldBeNil)
					So(string(after), ShouldNotContainSubstring, bundle.PluginName)
				})
			})
		})
	})
}

// TestBundleCanaryRealAntigravity installs the rendered plugin with the real
// agy CLI into a disposable HOME and checks the linked files.
func TestBundleCanaryRealAntigravity(t *testing.T) {
	canaryGuard(t, "agy")

	Convey("Given a disposable HOME and the agy CLI", t, func() {
		home := canaryHome(t)

		e, v := canaryEngine(t, home, agent.AntigravityCLIID)

		canaryTeardown(t, e, "antigravity")

		report, err := e.BundlesEnable(t.Context(), "antigravity")
		So(err, ShouldBeNil)

		Convey("When the real CLI installs the plugin", func() {
			Convey("Then the probe is executed and the linked files parse", func() {
				So(report.Bundles, ShouldHaveLength, 1)
				So(report.Bundles[0].Action, ShouldEqual, "enabled")
				So(report.Bundles[0].Registered, ShouldBeTrue)
				So(report.Bundles[0].Tier, ShouldEqual, state.VerifyExecuted)

				entry := canaryState(t, v, "antigravity")
				So(entry.VerifyTier, ShouldEqual, state.VerifyExecuted)
				So(entry.ProbeNote, ShouldContainSubstring, "structural")

				out, err := exec.CommandContext(t.Context(), "agy", "plugin", "list").Output()
				So(err, ShouldBeNil)
				So(string(out), ShouldContainSubstring, bundle.PluginName)

				linked := ""

				for _, root := range []string{
					filepath.Join(home, ".gemini", "antigravity-cli", "plugins", bundle.PluginName),
					filepath.Join(home, ".gemini", "config", "plugins", bundle.PluginName),
				} {
					if info, err := os.Stat(root); err == nil && info.IsDir() {
						linked = root
					}
				}

				Convey("And the plugin is linked into a customization root", func() {
					So(linked, ShouldNotBeEmpty)
					So(json.Valid([]byte(read(t, filepath.Join(linked, "plugin.json")))), ShouldBeTrue)

					for _, name := range []string{"hooks.json", "mcp_config.json"} {
						path := filepath.Join(linked, name)

						data, err := os.ReadFile(path) //nolint:gosec // the path is under the disposable home
						if os.IsNotExist(err) {
							continue
						}

						So(err, ShouldBeNil)
						So(json.Valid(data), ShouldBeTrue)
					}
				})

				Convey("And disabling uninstalls the plugin", func() {
					disable, err := e.BundlesDisable(t.Context(), "antigravity")
					So(err, ShouldBeNil)
					So(disable.Bundles, ShouldHaveLength, 1)
					So(disable.Bundles[0].Action, ShouldEqual, "disabled")
				})
			})
		})
	})
}
