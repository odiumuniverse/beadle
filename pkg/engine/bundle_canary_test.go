package engine_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/state"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

// TestBundleCanaryRealClaude installs the rendered bundle with the real
// Claude Code binary into a disposable HOME and CLAUDE_CONFIG_DIR. It never
// touches the machine's own configuration.
func TestBundleCanaryRealClaude(t *testing.T) {
	if os.Getenv("BEADLE_BUNDLE_CANARY") != "1" {
		t.Skip("set BEADLE_BUNDLE_CANARY=1 to install the bundle with the real claude CLI")
	}

	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not found")
	}

	Convey("Given a disposable HOME and config dir", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
		t.Setenv("XDG_CONFIG_HOME", "")

		v := vault.New(filepath.Join(t.TempDir(), "vault"))
		So(v.Init(), ShouldBeNil)

		cfg, err := config.Load(v.ConfigPath())
		So(err, ShouldBeNil)

		cfg.Enable(agent.ClaudeCodeID)
		cfg.ApproveHook("notify")
		So(cfg.Save(v.ConfigPath()), ShouldBeNil)

		e, err := engine.New(v, cfg, agent.All(home, t.TempDir()), engine.WithHome(home))
		So(err, ShouldBeNil)

		write(t, filepath.Join(v.SkillsDir(), "alpha", "SKILL.md"), "---\nname: alpha\ndescription: canary skill\n---\n\n# alpha\n")
		write(t, v.ServersPath(), `{"plug": {"transport": "stdio", "command": ["node", "srv.js"]}}`)
		write(t, v.HooksPath(), `{"notify": {"event": "session-start", "command": "echo hi", "timeout": 5}}`)

		report, err := e.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

		Convey("When the real CLI installs the bundle", func() {
			Convey("Then the probe is executed and the host lists no errors", func() {
				So(report.Bundles, ShouldHaveLength, 1)
				So(report.Bundles[0].Registered, ShouldBeTrue)
				So(report.Bundles[0].Tier, ShouldEqual, state.VerifyExecuted)

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
