package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
)

type fakeCLI struct {
	calls   [][]string
	failOn  map[string]error
	respond func(name string, args []string) ([]byte, bool)
	after   func(name string, args []string)
	output  string
}

func (f *fakeCLI) Run(name string, args []string, _ []byte) ([]byte, int, error) {
	f.calls = append(f.calls, append([]string{name}, args...))

	if f.respond != nil {
		if data, ok := f.respond(name, args); ok {
			return data, 0, nil
		}
	}

	if err := f.failOn[strings.Join(args, " ")]; err != nil {
		return []byte(f.output), 1, err
	}

	if f.after != nil {
		f.after(name, args)
	}

	return []byte(f.output), 0, nil
}

// claudeHost mimics the parts of Claude Code the probe reads: the installed
// plugin listing and the validate report.
type claudeHost struct {
	Version  string
	Listed   bool
	Disabled bool
	Errors   []string
	Validate []string
}

func (h *claudeHost) respond(name string, args []string) ([]byte, bool) {
	if name != "claude" || len(args) < 2 || args[0] != "plugin" {
		return nil, false
	}

	switch args[1] {
	case "validate":
		return h.validateReport()
	case "list":
		return h.listReport(args)
	default:
		return nil, false
	}
}

func (h *claudeHost) validateReport() ([]byte, bool) {
	if len(h.Validate) == 0 {
		return []byte(`{"success": true}`), true
	}

	messages := make([]map[string]string, 0, len(h.Validate))

	for _, message := range h.Validate {
		messages = append(messages, map[string]string{"message": message})
	}

	data, err := json.Marshal(map[string]any{
		"success":  false,
		"contents": []map[string]any{{"type": "hooks", "errors": messages}},
	})
	if err != nil {
		return nil, false
	}

	return data, true
}

func (h *claudeHost) listReport(args []string) ([]byte, bool) {
	if len(args) != 3 || args[2] != "--json" {
		return nil, false
	}

	if !h.Listed {
		return []byte("[]"), true
	}

	entry := map[string]any{"id": "beadle-canon@beadle", "version": h.Version, "enabled": !h.Disabled}
	if len(h.Errors) > 0 {
		entry["errors"] = h.Errors
	}

	data, err := json.Marshal([]map[string]any{entry})
	if err != nil {
		return nil, false
	}

	return data, true
}

func claudeBundleCLI(t *testing.T, f *fixture) (*fakeCLI, *claudeHost) {
	t.Helper()

	host := &claudeHost{}

	cli := &fakeCLI{}
	cli.respond = host.respond
	cli.after = func(name string, args []string) {
		if name != "claude" || len(args) < 2 {
			return
		}

		if args[1] == "install" || args[1] == "update" {
			host.Listed = true
			host.Version = renderedClaudeVersion(t, f)
		}
	}

	return cli, host
}

func renderedClaudeVersion(t *testing.T, f *fixture) string {
	t.Helper()

	path := filepath.Join(f.vault.BundlesDir(), "claude", "plugins", "beadle-canon", ".claude-plugin", "plugin.json")

	var doc struct {
		Version string `json:"version"`
	}

	if err := json.Unmarshal([]byte(read(t, path)), &doc); err != nil {
		t.Fatalf("unmarshal the rendered manifest: %v", err)
	}

	return doc.Version
}

func geminiExtensionsResponder(output string) func(string, []string) ([]byte, bool) {
	return func(name string, args []string) ([]byte, bool) {
		if name != "gemini" || len(args) != 4 || args[0] != "extensions" || args[1] != "list" || args[2] != "--output-format" || args[3] != "json" {
			return nil, false
		}

		return []byte(output), true
	}
}

func foundCLI(t *testing.T) {
	t.Helper()

	restore := engine.SetBundlesLookPathForTest(func(name string) (string, error) { return "/usr/bin/" + name, nil })
	t.Cleanup(restore)
}

func missingCLI(t *testing.T) {
	t.Helper()

	restore := engine.SetBundlesLookPathForTest(func(name string) (string, error) { return "", fmt.Errorf("%s: not found", name) })
	t.Cleanup(restore)
}

func fakeRunner(t *testing.T, cli *fakeCLI) {
	t.Helper()

	restore := engine.SetBundlesRunnerForTest(cli)
	t.Cleanup(restore)
}

func bundleFixture(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha\n")
	write(t, f.vault.ServersPath(), `{"plug": {"transport": "stdio", "command": ["node", "srv.js"]}}`)
	write(t, f.vault.HooksPath(), `{"notify": {"event": "session-start", "command": "echo hi", "timeout": 5}}`)

	f.config.ApproveHook("notify")

	if err := f.config.Save(f.vault.ConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	return f
}

func loadState(t *testing.T, f *fixture) *state.State {
	t.Helper()

	st, err := state.Load(f.vault.StatePath())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}

	return st
}

func enableClaude(t *testing.T, f *fixture) (*fakeCLI, *claudeHost, engine.Report) {
	t.Helper()

	cli, host := claudeBundleCLI(t, f)
	fakeRunner(t, cli)
	foundCLI(t)

	report, err := f.engine.BundlesEnable(t.Context(), "claude")
	if err != nil {
		t.Fatalf("enable claude: %v", err)
	}

	return cli, host, report
}

func TestBundlesEnableRegistersClaude(t *testing.T) {
	Convey("Given an approved hook canon and a claude CLI", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		cli, _, report := enableClaude(t, f)

		dir := filepath.Join(f.vault.BundlesDir(), "claude")
		pluginDir := filepath.Join(dir, "plugins", "beadle-canon")

		st := loadState(t, f)
		entry := st.Bundles["claude"]

		Convey("When it is enabled", func() {
			Convey("Then validate runs on the plugin directory and the marketplace root, then the CLI registers", func() {
				So(report.Bundles, ShouldHaveLength, 1)
				So(report.Bundles[0].Action, ShouldEqual, "enabled")
				So(report.Bundles[0].Registered, ShouldBeTrue)
				So(report.Bundles[0].Tier, ShouldEqual, state.VerifyExecuted)

				matched, matchErr := regexp.MatchString(`^0\.0\.0-[0-9a-f]{12}$`, report.Bundles[0].Version)
				So(matchErr, ShouldBeNil)
				So(matched, ShouldBeTrue)

				So(cli.calls, ShouldResemble, [][]string{
					{"claude", "plugin", "validate", "--json", "--strict", pluginDir},
					{"claude", "plugin", "validate", "--json", "--strict", dir},
					{"claude", "plugin", "marketplace", "add", dir},
					{"claude", "plugin", "install", "beadle-canon@beadle"},
					{"claude", "plugin", "list", "--json"},
				})

				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync), ShouldEqual, config.ModeOff)
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.MCP, config.ModeSync), ShouldEqual, config.ModeOff)

				So(entry.Enabled, ShouldBeTrue)
				So(entry.Registered, ShouldBeTrue)
				So(entry.VerifyTier, ShouldEqual, state.VerifyExecuted)
				So(entry.ProbeNote, ShouldBeEmpty)
				So(entry.SavedModes, ShouldResemble, map[kind.ID]config.Mode{kind.Skills: config.ModeSync, kind.MCP: config.ModeSync})

				marketplace := read(t, filepath.Join(dir, ".claude-plugin", "marketplace.json"))
				So(marketplace, ShouldContainSubstring, `"name": "beadle"`)
				So(marketplace, ShouldContainSubstring, entry.Version)
				So(read(t, filepath.Join(pluginDir, "skills", "alpha", "SKILL.md")), ShouldContainSubstring, "# alpha")
				So(read(t, filepath.Join(pluginDir, "hooks", "hooks.json")), ShouldContainSubstring, "echo hi")
			})
		})
	})
}

func TestBundlesEnableValidateFailureSkipsRegistration(t *testing.T) {
	Convey("Given a bundle that fails validation", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		cli, host := claudeBundleCLI(t, f)
		fakeRunner(t, cli)
		foundCLI(t)

		host.Validate = []string{"hooks.json must have `hooks` (the hook matchers) or `modules` (hooks modules), or both"}

		report, err := f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

		st := loadState(t, f)

		Convey("When it is enabled", func() {
			Convey("Then nothing registers, Enabled is not saved and the raw CLI text is reported", func() {
				So(report.Bundles, ShouldHaveLength, 1)
				So(report.Bundles[0].Action, ShouldEqual, "failed")
				So(report.Bundles[0].Note, ShouldContainSubstring, "hooks.json must have `hooks`")
				So(report.Bundles[0].Note, ShouldContainSubstring, "beadle bundles enable claude")

				_, ok := st.Bundles["claude"]
				So(ok, ShouldBeFalse)

				for _, call := range cli.calls {
					So(call[1], ShouldEqual, "plugin")
					So(call[2], ShouldEqual, "validate")
				}

				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync), ShouldEqual, config.ModeSync)
			})
		})
	})
}

func TestBundlesEnableWithoutCLI(t *testing.T) {
	Convey("Given no claude CLI", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		missingCLI(t)

		cli := &fakeCLI{}
		fakeRunner(t, cli)

		report, err := f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

		st := loadState(t, f)

		Convey("When it is enabled", func() {
			Convey("Then it only generates files, stays unverifiable and keeps file sync on", func() {
				So(report.Bundles, ShouldHaveLength, 1)
				So(report.Bundles[0].Action, ShouldEqual, "generated")
				So(report.Bundles[0].Registered, ShouldBeFalse)
				So(report.Bundles[0].Tier, ShouldEqual, state.VerifyUnverifiable)
				So(report.Bundles[0].Note, ShouldContainSubstring, "claude plugin marketplace add")
				So(strings.Join(report.Warnings, " "), ShouldContainSubstring, "claude CLI not found")
				So(cli.calls, ShouldBeEmpty)

				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync), ShouldEqual, config.ModeSync)

				So(st.Bundles["claude"].Enabled, ShouldBeTrue)
				So(st.Bundles["claude"].Registered, ShouldBeFalse)
				So(st.Bundles["claude"].VerifyTier, ShouldEqual, state.VerifyUnverifiable)
			})
		})
	})
}

func TestBundlesEnableCLIFailureShowsOutput(t *testing.T) {
	Convey("Given an install failure", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		cli, _ := claudeBundleCLI(t, f)
		fakeRunner(t, cli)
		foundCLI(t)

		cli.failOn = map[string]error{"plugin install beadle-canon@beadle": errors.New("boom")}
		cli.output = "raw install failure"

		report, err := f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

		st := loadState(t, f)

		Convey("When it is enabled", func() {
			Convey("Then the raw output is surfaced, the tier is failed and file sync stays on", func() {
				So(report.Bundles[0].Action, ShouldEqual, "failed")
				So(report.Bundles[0].Registered, ShouldBeFalse)
				So(report.Bundles[0].Tier, ShouldEqual, state.VerifyFailed)
				So(report.Bundles[0].Note, ShouldContainSubstring, "raw install failure")
				So(strings.Join(report.Warnings, " "), ShouldContainSubstring, "raw install failure")
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync), ShouldEqual, config.ModeSync)

				So(st.Bundles["claude"].Enabled, ShouldBeTrue)
				So(st.Bundles["claude"].VerifyTier, ShouldEqual, state.VerifyFailed)
			})
		})
	})
}

func TestBundlesEnableProbeFailureKeepsModes(t *testing.T) {
	Convey("Given a host that rejects the plugin", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		cli, host := claudeBundleCLI(t, f)
		fakeRunner(t, cli)
		foundCLI(t)

		host.Errors = []string{"Hook load failed: hooks.json is broken"}

		report, err := f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

		st := loadState(t, f)

		Convey("When it is enabled", func() {
			Convey("Then the modes stay as they were and the state records the failure", func() {
				So(report.Bundles[0].Action, ShouldEqual, "failed")
				So(report.Bundles[0].Tier, ShouldEqual, state.VerifyFailed)
				So(report.Bundles[0].Note, ShouldContainSubstring, "Hook load failed")

				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync), ShouldEqual, config.ModeSync)
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.MCP, config.ModeSync), ShouldEqual, config.ModeSync)

				So(st.Bundles["claude"].Registered, ShouldBeTrue)
				So(st.Bundles["claude"].VerifyTier, ShouldEqual, state.VerifyFailed)
				So(st.Bundles["claude"].ProbeNote, ShouldContainSubstring, "Hook load failed")
			})
		})
	})
}

func TestBundlesEnableIdempotent(t *testing.T) {
	Convey("Given an already enabled bundle", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		cli, _, _ := enableClaude(t, f)

		Convey("When it is enabled again", func() {
			report, err := f.engine.BundlesEnable(t.Context(), "claude")
			So(err, ShouldBeNil)

			Convey("Then the register call is skipped and the action is noop", func() {
				So(report.Bundles[0].Action, ShouldEqual, "noop")
				So(report.Bundles[0].Tier, ShouldEqual, state.VerifyExecuted)

				installs := 0

				for _, call := range cli.calls {
					if len(call) > 2 && call[1] == "plugin" && call[2] == "install" {
						installs++
					}
				}

				So(installs, ShouldEqual, 1)
			})
		})
	})
}

func TestBundlesEnableWithdrawsManagedElements(t *testing.T) {
	Convey("Given canon elements already synced into the host files", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		f.config.Enable(agent.SharedID)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		f.sync(t)

		So(read(t, f.claudeSkill("alpha")), ShouldContainSubstring, "# alpha")
		So(read(t, f.sharedSkill("alpha")), ShouldContainSubstring, "# alpha")
		So(read(t, f.claudeConfig()), ShouldContainSubstring, "plug")

		_, _, report := enableClaude(t, f)

		Convey("When the verified bundle is enabled", func() {
			Convey("Then the owned canon elements leave the file surface and the shared copy stays", func() {
				So(report.Bundles[0].Action, ShouldEqual, "enabled")
				So(report.Bundles[0].Withdrawn, ShouldContain, "skills alpha")
				So(report.Bundles[0].Withdrawn, ShouldContain, "mcp plug")
				So(report.Bundles[0].Kept, ShouldBeEmpty)

				_, err := os.Stat(filepath.Dir(f.claudeSkill("alpha")))
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)

				So(read(t, f.sharedSkill("alpha")), ShouldContainSubstring, "# alpha")
				So(read(t, f.claudeConfig()), ShouldNotContainSubstring, "plug")

				entry := loadState(t, f).Bundles["claude"]
				So(entry.PendingWithdrawal, ShouldBeFalse)
				So(entry.Withdrawn, ShouldHaveLength, 2)
			})

			Convey("Then a sync in the withdrawal window keeps the canon and opens no conflicts", func() {
				f.sync(t)

				So(f.conflicts(t, kind.Skills, agent.ClaudeCodeID), ShouldBeEmpty)
				So(f.conflicts(t, kind.MCP, agent.ClaudeCodeID), ShouldBeEmpty)
				So(read(t, f.vaultSkill("alpha")), ShouldContainSubstring, "# alpha")
				_, hasPlug := f.servers(t)["plug"]
				So(hasPlug, ShouldBeTrue)
			})

			Convey("Then disabling restores the files, the modes and the state", func() {
				before := f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync)
				So(before, ShouldEqual, config.ModeOff)

				report, err := f.engine.BundlesDisable(t.Context(), "claude")
				So(err, ShouldBeNil)

				So(report.Bundles[0].Action, ShouldEqual, "disabled")
				So(report.Bundles[0].Note, ShouldContainSubstring, "restored: skills alpha")

				So(read(t, f.claudeSkill("alpha")), ShouldContainSubstring, "# alpha")
				So(read(t, f.claudeConfig()), ShouldContainSubstring, "plug")
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeOff), ShouldEqual, config.ModeSync)
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.MCP, config.ModeOff), ShouldEqual, config.ModeSync)

				st := loadState(t, f)
				_, ok := st.Bundles["claude"]
				So(ok, ShouldBeFalse)

				Convey("And the next sync finds nothing to change", func() {
					f.sync(t)

					So(f.conflicts(t, kind.Skills, agent.ClaudeCodeID), ShouldBeEmpty)
					So(read(t, f.claudeSkill("alpha")), ShouldContainSubstring, "# alpha")
				})
			})
		})
	})
}

func TestBundlesEnableSkipsSecretServerAndKeepsHost(t *testing.T) {
	Convey("Given a canon server holding a literal secret", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)

		write(t, f.vault.ServersPath(), `{"api": {
			"transport": "http",
			"url": "https://example.com/mcp",
			"headers": {"Authorization": "Bearer tok-1"}
		}}`)

		f.sync(t)

		So(read(t, f.vault.ServersPath()), ShouldContainSubstring, "{secret:")
		So(read(t, f.claudeConfig()), ShouldContainSubstring, "Bearer tok-1")

		_, _, report := enableClaude(t, f)

		Convey("When the verified bundle is enabled", func() {
			Convey("Then the secret server keeps its host copy instead of entering the bundle", func() {
				So(report.Bundles[0].Withdrawn, ShouldNotContain, "mcp api")
				So(containsWarning(report.Notes, "mcp api carries secrets; delivered via the host config, not the claude bundle"), ShouldBeTrue)
				So(containsWarning(report.Warnings, "unresolved secrets"), ShouldBeFalse)

				cfg := read(t, f.claudeConfig())
				So(cfg, ShouldContainSubstring, "Bearer tok-1")
				So(cfg, ShouldNotContainSubstring, secretRefMarkerForTest)

				bundleMCP := read(t, filepath.Join(f.vault.BundlesDir(), "claude", "plugins", "beadle-canon", ".mcp.json"))
				So(bundleMCP, ShouldNotContainSubstring, "api")
			})
		})
	})
}

func TestBundlesDisableKeepsStateWhenCanonUnresolved(t *testing.T) {
	Convey("Given a withdrawn server whose canon cannot resolve", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)

		// The header is not secret-like, so the canon keeps the literal value
		// and the server is withdrawn into the bundle like any other.
		write(t, f.vault.ServersPath(), `{"api": {
			"transport": "http",
			"url": "https://example.com/mcp",
			"headers": {"X-Context": "ctx-1"}
		}}`)

		f.sync(t)

		So(read(t, f.vault.ServersPath()), ShouldNotContainSubstring, "{secret:")

		_, _, report := enableClaude(t, f)
		So(report.Bundles[0].Withdrawn, ShouldContain, "mcp api")

		// The canon now references a secret that has no value: the element
		// cannot be materialized back.
		write(t, f.vault.ServersPath(), `{"api": {
			"transport": "http",
			"url": "https://example.com/mcp",
			"headers": {"Authorization": "{secret:MISSING_TOKEN}"}
		}}`)

		cli, _ := claudeBundleCLI(t, f)
		fakeRunner(t, cli)
		foundCLI(t)

		disable, err := f.engine.BundlesDisable(t.Context(), "claude")
		So(err, ShouldBeNil)

		st := loadState(t, f)

		Convey("When disable runs", func() {
			Convey("Then it fails, keeps the entry and leaves the modes off", func() {
				So(disable.Bundles[0].Action, ShouldEqual, "failed")
				So(disable.Bundles[0].Note, ShouldContainSubstring, "unresolved secrets")

				entry, ok := st.Bundles["claude"]
				So(ok, ShouldBeTrue)
				So(entry.Withdrawn, ShouldNotBeEmpty)
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.MCP, config.ModeSync), ShouldEqual, config.ModeOff)
				So(read(t, f.claudeConfig()), ShouldNotContainSubstring, "api")
			})
		})
	})
}

func TestBundlesSkipSecretBearingServers(t *testing.T) {
	Convey("Given a canon with a plain and a secret-bearing MCP server", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)

		f.engine.Secrets().Set("API_TOKEN", "tok-live-123")
		So(f.engine.Secrets().Save(), ShouldBeNil)

		write(t, f.vault.ServersPath(), `{
			"plain": {"transport": "stdio", "command": ["node", "plain.js"]},
			"secret": {
				"transport": "http",
				"url": "https://example.com/mcp",
				"headers": {"Authorization": "{secret:API_TOKEN}"}
			}
		}`)

		f.sync(t)

		host := hostMCPServers(t, f.claudeConfig(), "mcpServers")
		So(host, ShouldContainKey, "plain")
		So(host, ShouldContainKey, "secret")

		_, _, report := enableClaude(t, f)

		bundleMCP := read(t, filepath.Join(f.vault.BundlesDir(), "claude", "plugins", "beadle-canon", ".mcp.json"))

		Convey("When the bundle is enabled", func() {
			Convey("Then the secret-bearing server stays out of the bundle", func() {
				So(bundleMCP, ShouldContainSubstring, "plain")
				So(bundleMCP, ShouldNotContainSubstring, "secret")
				So(bundleMCP, ShouldNotContainSubstring, "tok-live-123")

				So(containsWarning(report.Notes, "mcp secret carries secrets; delivered via the host config, not the claude bundle"), ShouldBeTrue)
			})

			Convey("And the host config keeps it with the resolved value", func() {
				host := hostMCPServers(t, f.claudeConfig(), "mcpServers")
				So(host, ShouldContainKey, "secret")
				So(host["secret"]["headers"], ShouldResemble, map[string]any{"Authorization": "tok-live-123"})

				// The plain server is delivered by the bundle now, as before.
				So(host, ShouldNotContainKey, "plain")
			})

			Convey("And doctor reports an Info instead of a validate error", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityError, "claude plugin validate failed"), ShouldBeFalse)
				So(hasIssue(issues, engine.SeverityInfo, "mcp secret carries secrets; delivered via the host config, not the claude bundle"), ShouldBeTrue)

				infos := 0

				for _, issue := range issues {
					if strings.Contains(issue.Message, "mcp secret carries secrets; delivered via the host config, not the claude bundle") {
						infos++
					}
				}

				So(infos, ShouldEqual, 1)
			})

			Convey("When an older bundle had withdrawn the secret server", func() {
				st := loadState(t, f)
				entry := st.Bundles["claude"]
				entry.Withdrawn = append(entry.Withdrawn, state.WithdrawnItem{Kind: kind.MCP, Name: "secret"})
				st.Bundles["claude"] = entry

				So(st.Save(f.vault.StatePath()), ShouldBeNil)

				disable, err := f.engine.BundlesDisable(t.Context(), "claude")
				So(err, ShouldBeNil)
				So(disable.Bundles[0].Action, ShouldEqual, "disabled")

				Convey("Then disabling restores both servers to the host config", func() {
					host := hostMCPServers(t, f.claudeConfig(), "mcpServers")
					So(host, ShouldContainKey, "plain")
					So(host, ShouldContainKey, "secret")
					So(host["secret"]["headers"], ShouldResemble, map[string]any{"Authorization": "tok-live-123"})
					So(read(t, f.claudeConfig()), ShouldNotContainSubstring, secretRefMarkerForTest)
				})
			})
		})
	})
}

// secretRefMarkerForTest mirrors the engine's secret reference syntax.
const secretRefMarkerForTest = "{secret:"

func TestBundlesEnableKeepsModifiedSkill(t *testing.T) {
	Convey("Given a hand-edited canon skill copy", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		f.config.Enable(agent.SharedID)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		f.sync(t)

		write(t, f.claudeSkill("alpha"), "# edited by hand\n")

		_, _, report := enableClaude(t, f)

		Convey("When the verified bundle is enabled", func() {
			Convey("Then the edited skill stays with a warning and the untouched server leaves", func() {
				So(report.Bundles[0].Kept, ShouldContain, "skills alpha (modified)")
				So(report.Bundles[0].Withdrawn, ShouldContain, "mcp plug")

				So(read(t, f.claudeSkill("alpha")), ShouldContainSubstring, "# edited by hand")
				So(strings.Join(report.Warnings, " "), ShouldContainSubstring, "kept skills alpha")
			})
		})
	})
}

func TestBundlesEnableKeepsSymlinkedSkill(t *testing.T) {
	Convey("Given a canon skill delivered to claude as a symlink", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		f.config.Enable(agent.SharedID)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		f.sync(t)

		claudeSkill := filepath.Dir(f.claudeSkill("alpha"))
		So(os.RemoveAll(claudeSkill), ShouldBeNil)
		So(os.Symlink(filepath.Dir(f.sharedSkill("alpha")), claudeSkill), ShouldBeNil)

		_, _, report := enableClaude(t, f)

		Convey("When the verified bundle is enabled", func() {
			Convey("Then the symlink stays and is reported as read-only, not withdrawn", func() {
				So(report.Bundles[0].Withdrawn, ShouldNotContain, "skills alpha")
				So(report.Bundles[0].Kept, ShouldContain, "skills alpha (read-only)")

				info, err := os.Lstat(claudeSkill)
				So(err, ShouldBeNil)
				So(info.Mode()&fs.ModeSymlink != 0, ShouldBeTrue)
			})
		})
	})
}

func TestBundlesEnableKeepsEditedServer(t *testing.T) {
	Convey("Given a hand-edited canon server in the host file", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		f.sync(t)

		edited := strings.Replace(read(t, f.claudeConfig()), `"srv.js"`, `"other.js"`, 1)
		write(t, f.claudeConfig(), edited)

		_, _, report := enableClaude(t, f)

		Convey("When the verified bundle is enabled", func() {
			Convey("Then the edited server stays with a warning", func() {
				So(report.Bundles[0].Kept, ShouldContain, "mcp plug (modified)")
				So(report.Bundles[0].Withdrawn, ShouldNotContain, "mcp plug")
				So(read(t, f.claudeConfig()), ShouldContainSubstring, "other.js")
			})
		})
	})
}

func TestBundlesEnableKeepsUnmanagedElement(t *testing.T) {
	Convey("Given a canon element never written by beadle", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		write(t, f.claudeSkill("alpha"), "# foreign copy\n")

		_, _, report := enableClaude(t, f)

		Convey("When the verified bundle is enabled", func() {
			Convey("Then the unmanaged copy stays", func() {
				So(report.Bundles[0].Kept, ShouldContain, "skills alpha (unmanaged)")
				So(read(t, f.claudeSkill("alpha")), ShouldContainSubstring, "# foreign copy")
				So(report.Bundles[0].Withdrawn, ShouldBeEmpty)
			})
		})
	})
}

func TestBundlesDisableRestoresModes(t *testing.T) {
	Convey("Given an enabled bundle", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		cli, _, _ := enableClaude(t, f)

		cli.calls = nil

		report, err := f.engine.BundlesDisable(t.Context(), "claude")
		So(err, ShouldBeNil)

		st := loadState(t, f)

		Convey("When it is disabled", func() {
			Convey("Then the CLI unregisters and the modes restore", func() {
				So(report.Bundles[0].Action, ShouldEqual, "disabled")
				So(cli.calls, ShouldResemble, [][]string{
					{"claude", "plugin", "uninstall", "beadle-canon@beadle"},
					{"claude", "plugin", "marketplace", "rm", "beadle"},
				})
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeOff), ShouldEqual, config.ModeSync)
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.MCP, config.ModeOff), ShouldEqual, config.ModeSync)

				_, ok := st.Bundles["claude"]
				So(ok, ShouldBeFalse)
			})

			Convey("Then a doctor run does not validate a disabled bundle", func() {
				cli.calls = nil

				_, err = f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(cli.calls, ShouldBeEmpty)
			})
		})
	})
}

func TestBundlesDisableMigratesSavedOffModes(t *testing.T) {
	Convey("Given a bundle whose modes were off before the enable", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		f.config.SetMode(agent.ClaudeCodeID, kind.Skills, config.ModeOff)
		f.config.SetMode(agent.ClaudeCodeID, kind.MCP, config.ModeOff)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		_, _, enableReport := enableClaude(t, f)

		st := loadState(t, f)
		entry := st.Bundles["claude"]

		Convey("When the bundle is enabled and disabled", func() {
			Convey("Then off is not saved as user intent and the surface defaults come back with warnings", func() {
				So(entry.SavedModes, ShouldResemble, map[kind.ID]config.Mode{kind.Skills: config.ModeSync, kind.MCP: config.ModeSync})
				So(strings.Join(enableReport.Warnings, " "), ShouldContainSubstring, "restoring the default skills mode")

				report, err := f.engine.BundlesDisable(t.Context(), "claude")
				So(err, ShouldBeNil)

				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeOff), ShouldEqual, config.ModeSync)
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.MCP, config.ModeOff), ShouldEqual, config.ModeSync)
				So(report.Bundles[0].Action, ShouldEqual, "disabled")
			})
		})
	})
}

func TestBundlesDisableRestoresSeededOffModes(t *testing.T) {
	Convey("Given an existing state with saved_modes off/off", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		_, _, _ = enableClaude(t, f)

		st := loadState(t, f)
		entry := st.Bundles["claude"]
		entry.SavedModes = map[kind.ID]config.Mode{kind.Skills: config.ModeOff, kind.MCP: config.ModeOff}
		st.Bundles["claude"] = entry
		So(st.Save(f.vault.StatePath()), ShouldBeNil)

		report, err := f.engine.BundlesDisable(t.Context(), "claude")
		So(err, ShouldBeNil)

		Convey("When the bundle is disabled", func() {
			Convey("Then off is migrated to the surface defaults with a warning", func() {
				So(strings.Join(report.Warnings, " "), ShouldContainSubstring, "restoring the default skills mode")
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeOff), ShouldEqual, config.ModeSync)
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.MCP, config.ModeOff), ShouldEqual, config.ModeSync)
			})
		})
	})
}

func TestBundlesDisableRetriesAfterFailure(t *testing.T) {
	Convey("Given a bundle whose unregister failed", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		cli, _, _ := enableClaude(t, f)

		cli.failOn = map[string]error{"plugin uninstall beadle-canon@beadle": errors.New("boom")}

		_, err := f.engine.BundlesDisable(t.Context(), "claude")
		So(err, ShouldBeNil)

		cli.failOn = nil
		cli.calls = nil

		Convey("When it is retried", func() {
			report, err := f.engine.BundlesDisable(t.Context(), "claude")
			So(err, ShouldBeNil)

			st := loadState(t, f)

			Convey("Then it succeeds and clears the state", func() {
				So(report.Bundles[0].Action, ShouldEqual, "disabled")
				So(cli.calls, ShouldResemble, [][]string{
					{"claude", "plugin", "uninstall", "beadle-canon@beadle"},
					{"claude", "plugin", "marketplace", "rm", "beadle"},
				})
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeOff), ShouldEqual, config.ModeSync)

				_, ok := st.Bundles["claude"]
				So(ok, ShouldBeFalse)
			})
		})
	})
}

func TestBundlesEnableUpdateFailureKeepsRegisteredVersion(t *testing.T) {
	Convey("Given a registered bundle whose update fails", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		cli, _, first := enableClaude(t, f)

		cli.failOn = map[string]error{"plugin update beadle-canon@beadle": errors.New("boom")}
		cli.output = "raw update failure"

		write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha v2\n")

		Convey("When it is re-enabled", func() {
			report, err := f.engine.BundlesEnable(t.Context(), "claude")
			So(err, ShouldBeNil)

			st := loadState(t, f)

			Convey("Then the registered version is kept, the raw output noted and the tier failed", func() {
				So(report.Bundles[0].Action, ShouldEqual, "failed")
				So(report.Bundles[0].Version, ShouldEqual, first.Bundles[0].Version)
				So(report.Bundles[0].Note, ShouldContainSubstring, "raw update failure")
				So(strings.Join(report.Warnings, " "), ShouldContainSubstring, "raw update failure")

				So(st.Bundles["claude"].Version, ShouldEqual, first.Bundles[0].Version)
				So(st.Bundles["claude"].VerifyTier, ShouldEqual, state.VerifyFailed)
			})
		})
	})
}

func TestBundleIssuesAntigravityLinkStates(t *testing.T) {
	Convey("Given an antigravity bundle", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		missingCLI(t)
		fakeRunner(t, &fakeCLI{})

		_, err := f.engine.BundlesEnable(t.Context(), "antigravity")
		So(err, ShouldBeNil)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When it is not linked then linked then enabled", func() {
			So(hasIssue(issues, engine.SeverityWarn, "is not linked yet"), ShouldBeTrue)

			linked := filepath.Join(f.home, ".gemini", "antigravity-cli", "plugins", "beadle-canon")
			So(os.MkdirAll(filepath.Dir(linked), 0o750), ShouldBeNil)
			So(os.MkdirAll(linked, 0o750), ShouldBeNil)

			issues, err = f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)
			So(hasIssue(issues, engine.SeverityWarn, "is linked but not registered"), ShouldBeTrue)

			_, err = f.engine.BundlesEnable(t.Context(), "antigravity")
			So(err, ShouldBeNil)

			issues, err = f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the link warnings clear once registered", func() {
				for _, issue := range issues {
					So(issue.Message, ShouldNotContainSubstring, "is not linked yet")
					So(issue.Message, ShouldNotContainSubstring, "is linked but not registered")
				}
			})
		})
	})
}

func TestBundlesEnableAntigravityRegistersThroughCLI(t *testing.T) {
	Convey("Given an agy CLI that stages the plugin", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		foundCLI(t)

		linked := filepath.Join(f.home, ".gemini", "antigravity-cli", "plugins", "beadle-canon")

		cli := &fakeCLI{
			respond: func(name string, args []string) ([]byte, bool) {
				if name == "agy" && len(args) == 2 && args[0] == "plugin" && args[1] == "list" {
					return []byte("beadle-canon (enabled)"), true
				}

				return nil, false
			},
			after: func(name string, args []string) {
				switch {
				case name == "agy" && len(args) >= 2 && args[0] == "plugin" && args[1] == "install":
					So(os.MkdirAll(filepath.Dir(linked), 0o750), ShouldBeNil)
					So(os.Symlink(filepath.Join(f.vault.BundlesDir(), "antigravity"), linked), ShouldBeNil)
				case name == "agy" && len(args) >= 2 && args[0] == "plugin" && args[1] == "uninstall":
					So(os.RemoveAll(linked), ShouldBeNil)
				}
			},
		}

		fakeRunner(t, cli)

		report, err := f.engine.BundlesEnable(t.Context(), "antigravity")
		So(err, ShouldBeNil)

		Convey("When it is enabled", func() {
			Convey("Then the CLI installs and enables the plugin, then the structural probe verifies it", func() {
				So(report.Bundles[0].Action, ShouldEqual, "enabled")
				So(report.Bundles[0].Registered, ShouldBeTrue)
				So(report.Bundles[0].Tier, ShouldEqual, state.VerifyExecuted)
				So(f.config.ModeFor(agent.AntigravityCLIID, kind.MCP, config.ModeSync), ShouldEqual, config.ModeOff)

				So(cli.calls, ShouldResemble, [][]string{
					{"agy", "plugin", "install", filepath.Join(f.vault.BundlesDir(), "antigravity")},
					{"agy", "plugin", "enable", "beadle-canon"},
					{"agy", "plugin", "list"},
				})

				note := loadState(t, f).Bundles["antigravity"].ProbeNote
				So(note, ShouldContainSubstring, "structural")
			})

			Convey("Then disabling uninstalls through the CLI", func() {
				cli.calls = nil

				report, err := f.engine.BundlesDisable(t.Context(), "antigravity")
				So(err, ShouldBeNil)
				So(report.Bundles[0].Action, ShouldEqual, "disabled")
				So(cli.calls, ShouldResemble, [][]string{{"agy", "plugin", "uninstall", "beadle-canon"}})
			})
		})
	})
}

func TestBundlesEnableAntigravityListMissKeepsModes(t *testing.T) {
	Convey("Given an agy listing without the plugin", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		foundCLI(t)

		linked := filepath.Join(f.home, ".gemini", "antigravity-cli", "plugins", "beadle-canon")

		cli := &fakeCLI{
			respond: func(name string, args []string) ([]byte, bool) {
				if name == "agy" && len(args) == 2 && args[0] == "plugin" && args[1] == "list" {
					return []byte("caveman (enabled)"), true
				}

				return nil, false
			},
			after: func(name string, args []string) {
				if name == "agy" && len(args) >= 2 && args[0] == "plugin" && args[1] == "install" {
					So(os.MkdirAll(filepath.Dir(linked), 0o750), ShouldBeNil)
					So(os.Symlink(filepath.Join(f.vault.BundlesDir(), "antigravity"), linked), ShouldBeNil)
				}
			},
		}

		fakeRunner(t, cli)

		report, err := f.engine.BundlesEnable(t.Context(), "antigravity")
		So(err, ShouldBeNil)

		Convey("When it is enabled", func() {
			Convey("Then the probe fails and the modes stay on", func() {
				So(report.Bundles[0].Action, ShouldEqual, "failed")
				So(report.Bundles[0].Tier, ShouldEqual, state.VerifyFailed)
				So(f.config.ModeFor(agent.AntigravityCLIID, kind.MCP, config.ModeSync), ShouldEqual, config.ModeSync)
			})
		})
	})
}

func TestBundlesDisableWithoutCLIKeepsRegistrationState(t *testing.T) {
	Convey("Given a registered bundle and a missing CLI", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		_, _, _ = enableClaude(t, f)

		missingCLI(t)

		report, err := f.engine.BundlesDisable(t.Context(), "claude")
		So(err, ShouldBeNil)

		st := loadState(t, f)

		Convey("When it is disabled", func() {
			Convey("Then it fails, notes the unregister command and keeps registration state", func() {
				So(report.Bundles[0].Action, ShouldEqual, "failed")
				So(report.Bundles[0].Note, ShouldContainSubstring, "claude plugin uninstall beadle-canon@beadle")
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync), ShouldEqual, config.ModeOff)

				So(st.Bundles["claude"].Enabled, ShouldBeFalse)
				So(st.Bundles["claude"].Registered, ShouldBeTrue)
			})
		})
	})
}

func TestBundlesDisableAntigravityAfterUnlink(t *testing.T) {
	Convey("Given a linked antigravity bundle", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		missingCLI(t)
		fakeRunner(t, &fakeCLI{})

		linked := filepath.Join(f.home, ".gemini", "antigravity-cli", "plugins", "beadle-canon")
		So(os.MkdirAll(filepath.Dir(linked), 0o750), ShouldBeNil)
		So(os.MkdirAll(linked, 0o750), ShouldBeNil)

		_, err := f.engine.BundlesEnable(t.Context(), "antigravity")
		So(err, ShouldBeNil)

		report, err := f.engine.BundlesDisable(t.Context(), "antigravity")
		So(err, ShouldBeNil)

		Convey("When the link is removed and it is disabled again", func() {
			So(report.Bundles[0].Action, ShouldEqual, "failed")
			So(report.Bundles[0].Note, ShouldContainSubstring, "remove the plugin link first")
			So(f.config.ModeFor(agent.AntigravityCLIID, kind.MCP, config.ModeSync), ShouldEqual, config.ModeSync)

			So(os.RemoveAll(linked), ShouldBeNil)

			report, err = f.engine.BundlesDisable(t.Context(), "antigravity")
			So(err, ShouldBeNil)

			st := loadState(t, f)

			Convey("Then the second attempt succeeds and clears the state", func() {
				So(report.Bundles[0].Action, ShouldEqual, "disabled")
				So(f.config.ModeFor(agent.AntigravityCLIID, kind.MCP, config.ModeOff), ShouldEqual, config.ModeSync)

				_, ok := st.Bundles["antigravity"]
				So(ok, ShouldBeFalse)
			})
		})
	})
}

func TestBundlesDisableWithoutEnable(t *testing.T) {
	Convey("Given no enabled bundle", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		missingCLI(t)
		fakeRunner(t, &fakeCLI{})

		report, err := f.engine.BundlesDisable(t.Context(), "claude")
		So(err, ShouldBeNil)

		Convey("When it is disabled", func() {
			Convey("Then it is a noop", func() {
				So(report.Bundles[0].Action, ShouldEqual, "noop")
				So(report.Bundles[0].Note, ShouldContainSubstring, "no enabled bundle")
			})
		})
	})
}

func TestBundlesSyncUpdatesRegisteredBundle(t *testing.T) {
	Convey("Given an enabled bundle whose canon changes", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		cli, host, first := enableClaude(t, f)

		cli.calls = nil

		write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha v2\n")

		f.sync(t)

		st := loadState(t, f)

		Convey("When sync re-renders the bundle", func() {
			Convey("Then it updates the host and re-probes without flipping modes", func() {
				So(cli.calls, ShouldResemble, [][]string{
					{"claude", "plugin", "validate", "--json", "--strict", filepath.Join(f.vault.BundlesDir(), "claude", "plugins", "beadle-canon")},
					{"claude", "plugin", "validate", "--json", "--strict", filepath.Join(f.vault.BundlesDir(), "claude")},
					{"claude", "plugin", "marketplace", "update", "beadle"},
					{"claude", "plugin", "update", "beadle-canon@beadle"},
					{"claude", "plugin", "list", "--json"},
				})
				So(host.Version, ShouldNotEqual, first.Bundles[0].Version)
				So(st.Bundles["claude"].Version, ShouldEqual, host.Version)
				So(st.Bundles["claude"].Version, ShouldNotEqual, first.Bundles[0].Version)
				So(st.Bundles["claude"].VerifyTier, ShouldEqual, state.VerifyExecuted)
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync), ShouldEqual, config.ModeOff)
			})
		})
	})
}

func TestBundlesSyncUpdateFailureKeepsVersion(t *testing.T) {
	Convey("Given an enabled bundle whose update fails during sync", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		cli, _, first := enableClaude(t, f)

		cli.calls = nil
		cli.failOn = map[string]error{"plugin update beadle-canon@beadle": errors.New("boom")}
		cli.output = "raw sync update failure"

		write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha v2\n")

		report := f.sync(t)

		st := loadState(t, f)

		Convey("When sync runs", func() {
			Convey("Then it warns, keeps the version, marks failed and leaves the modes alone", func() {
				So(strings.Join(report.Warnings, " "), ShouldContainSubstring, "raw sync update failure")
				So(st.Bundles["claude"].Version, ShouldEqual, first.Bundles[0].Version)
				So(st.Bundles["claude"].VerifyTier, ShouldEqual, state.VerifyFailed)
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync), ShouldEqual, config.ModeOff)
			})
		})
	})
}

func TestBundlesSyncDefersRefreshWithoutCLI(t *testing.T) {
	Convey("Given an enabled bundle whose host CLI the syncing process cannot reach", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		cli, _, first := enableClaude(t, f)

		cli.calls = nil

		missingCLI(t)
		write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha v2\n")

		report := f.sync(t)
		rendered := renderedClaudeVersion(t, f)
		entry := loadState(t, f).Bundles["claude"]

		Convey("When sync re-renders the bundle", func() {
			Convey("Then nothing runs, the host keeps its served version and tier, and the new version waits", func() {
				So(cli.calls, ShouldBeEmpty)
				So(entry.Version, ShouldEqual, first.Bundles[0].Version)
				So(entry.VerifyTier, ShouldEqual, state.VerifyExecuted)
				So(entry.Pending, ShouldNotBeNil)
				So(entry.Pending.Version, ShouldEqual, rendered)
				So(countWarnings(report, "claude CLI not found"), ShouldEqual, 1)
				So(report.Bundles, ShouldHaveLength, 1)
				So(report.Bundles[0].Action, ShouldEqual, "pending")
				So(report.Bundles[0].Tier, ShouldEqual, state.VerifyUnverifiable)
			})
		})

		Convey("When sync runs again without the CLI", func() {
			again := f.sync(t)
			pending := loadState(t, f).Bundles["claude"].Pending

			Convey("Then it stays quiet and keeps the first deferral", func() {
				So(cli.calls, ShouldBeEmpty)
				So(countWarnings(again, "claude CLI not found"), ShouldEqual, 0)
				So(pending, ShouldNotBeNil)
				So(pending.Since.Equal(entry.Pending.Since), ShouldBeTrue)
			})
		})

		Convey("When a run that reaches the CLI syncs", func() {
			foundCLI(t)
			f.sync(t)

			after := loadState(t, f).Bundles["claude"]

			Convey("Then it takes the waiting version and clears the deferral", func() {
				So(cli.calls, ShouldHaveLength, 5)
				So(after.Version, ShouldEqual, rendered)
				So(after.VerifyTier, ShouldEqual, state.VerifyExecuted)
				So(after.Pending, ShouldBeNil)
			})
		})
	})
}

func TestBundlesRefreshSkipsDryRunPullPushAndKinds(t *testing.T) {
	Convey("Given an enabled bundle", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		cli, _, _ := enableClaude(t, f)

		manifest := filepath.Join(f.vault.BundlesDir(), "claude", "plugins", "beadle-canon", ".claude-plugin", "plugin.json")
		before := read(t, manifest)

		write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha v2\n")

		Convey("When dry-run, pull, push and a scoped sync run", func() {
			cli.calls = nil

			f.run(t, engine.SyncOptions{DryRun: true})
			So(read(t, manifest), ShouldEqual, before)

			f.run(t, engine.SyncOptions{Direction: config.ModePull})
			So(read(t, manifest), ShouldEqual, before)

			f.run(t, engine.SyncOptions{Direction: config.ModePush})
			So(read(t, manifest), ShouldEqual, before)

			f.run(t, engine.SyncOptions{Kinds: []kind.ID{kind.Rules}})
			So(read(t, manifest), ShouldEqual, before)

			So(cli.calls, ShouldBeEmpty)

			f.sync(t)
			after := read(t, manifest)

			Convey("Then only the full sync re-renders a new version", func() {
				So(after, ShouldNotEqual, before)
				So(cli.calls, ShouldHaveLength, 5)
			})
		})
	})
}

func TestBundleIssuesStaleBeforeSync(t *testing.T) {
	Convey("Given a changed canon that sync has not applied", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		_, _, _ = enableClaude(t, f)

		write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha v2\n")

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then it warns the bundle is stale", func() {
				So(hasIssue(issues, engine.SeverityWarn, "is stale"), ShouldBeTrue)
			})
		})
	})
}

func TestBundleIssuesStaleNamesUnreachableCLI(t *testing.T) {
	Convey("Given a refresh deferred because the syncing process could not reach the host CLI", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		_, _, _ = enableClaude(t, f)

		missingCLI(t)
		write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha v2\n")
		f.sync(t)

		Convey("When doctor runs from a shell that reaches the CLI", func() {
			foundCLI(t)

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the stale warning names the unreachable CLI instead of a bare retry", func() {
				So(hasIssue(issues, engine.SeverityWarn, "is stale"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityWarn, "the syncing process could not reach the claude CLI since"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityWarn, "run beadle sync from a shell that has claude on PATH"), ShouldBeTrue)
			})
		})
	})
}

func TestBundleIssuesDedupConflict(t *testing.T) {
	Convey("Given a bundle whose kind is synced as files again", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		_, _, _ = enableClaude(t, f)

		f.config.SetMode(agent.ClaudeCodeID, kind.Skills, config.ModeSync)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When doctor runs", func() {
			Convey("Then it errors on the double presentation", func() {
				So(hasIssue(issues, engine.SeverityError, "still synced as files"), ShouldBeTrue)
			})
		})
	})
}

func TestBundleDoctorInvariants(t *testing.T) {
	Convey("Given a kind mode turned off without a verified bundle", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		missingCLI(t)
		fakeRunner(t, &fakeCLI{})

		f.config.SetMode(agent.ClaudeCodeID, kind.Skills, config.ModeOff)
		f.config.SetMode(agent.ClaudeCodeID, kind.MCP, config.ModeOff)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("Then zero delivery is an error with a retry hint", func() {
			So(hasIssue(issues, engine.SeverityError, "is off for skills and nothing delivers the canon"), ShouldBeTrue)
			So(hasIssue(issues, engine.SeverityError, "run beadle bundles enable claude"), ShouldBeTrue)
		})
	})

	Convey("Given a registered bundle the probe rejected", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		cli, host := claudeBundleCLI(t, f)
		fakeRunner(t, cli)
		foundCLI(t)

		host.Errors = []string{"Hook load failed"}

		_, err := f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("Then it warns that the modes were left on", func() {
			So(hasIssue(issues, engine.SeverityWarn, "unverified, modes left on"), ShouldBeTrue)
			So(hasIssue(issues, engine.SeverityWarn, "Hook load failed"), ShouldBeTrue)
		})
	})

	Convey("Given a registered bundle whose update failed while the modes are off", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		cli, _, _ := enableClaude(t, f)

		cli.failOn = map[string]error{"plugin update beadle-canon@beadle": errors.New("boom")}
		cli.output = "raw update failure"

		write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha v2\n")

		f.sync(t)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("Then zero delivery is not claimed and the warning names the modes as they are", func() {
			So(hasIssue(issues, engine.SeverityError, "nothing delivers the canon"), ShouldBeFalse)
			So(hasIssue(issues, engine.SeverityWarn, "modes left on"), ShouldBeFalse)
			So(hasIssue(issues, engine.SeverityWarn, "file modes are off, so the host keeps serving its last installed copy"), ShouldBeTrue)
		})
	})

	Convey("Given a verified bundle whose files reappeared", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		_, _, _ = enableClaude(t, f)

		f.config.SetMode(agent.ClaudeCodeID, kind.Skills, config.ModeSync)
		f.config.SetMode(agent.ClaudeCodeID, kind.MCP, config.ModeSync)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		f.sync(t)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("Then it errors on the files and warns about double delivery", func() {
			So(hasIssue(issues, engine.SeverityError, "still synced as files"), ShouldBeTrue)
			So(hasIssue(issues, engine.SeverityWarn, "double delivery"), ShouldBeTrue)
		})
	})
}

func TestBundleDoctorDoubleDeliverySkipsForeignElements(t *testing.T) {
	Convey("Given a read-only copy and an unmanaged canon directory", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		f.config.Enable(agent.SharedID)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		f.sync(t)

		claudeAlpha := filepath.Dir(f.claudeSkill("alpha"))
		So(os.RemoveAll(claudeAlpha), ShouldBeNil)
		So(os.Symlink(filepath.Dir(f.sharedSkill("alpha")), claudeAlpha), ShouldBeNil)

		write(t, filepath.Join(f.vault.SkillsDir(), "beta", "SKILL.md"), "# beta\n")
		write(t, f.claudeSkill("beta"), "# foreign beta\n")

		_, _, report := enableClaude(t, f)

		Convey("When the verified bundle is enabled", func() {
			Convey("Then neither element is reported as double delivery", func() {
				So(report.Bundles[0].Kept, ShouldContain, "skills alpha (read-only)")
				So(report.Bundles[0].Kept, ShouldContain, "skills beta (unmanaged)")

				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)

				for _, issue := range issues {
					So(issue.Message, ShouldNotContainSubstring, "double delivery")
				}
			})
		})
	})
}

func TestBundleIssuesApprovalInfo(t *testing.T) {
	Convey("Given an unapproved hook", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		missingCLI(t)
		fakeRunner(t, &fakeCLI{})

		write(t, f.vault.HooksPath(), `{
			"notify": {"event": "session-start", "command": "echo hi"},
			"secret": {"event": "stop", "command": "rm -rf /"}
		}`)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When doctor runs", func() {
			Convey("Then it informs about the pending approval", func() {
				So(hasIssue(issues, engine.SeverityInfo, "1 hook(s) await approval"), ShouldBeTrue)
			})
		})
	})
}

func TestBundleIssuesValidateFailure(t *testing.T) {
	Convey("Given a bundle whose validate fails", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		cli, host, _ := func() (*fakeCLI, *claudeHost, engine.Report) {
			c, h := claudeBundleCLI(t, f)
			fakeRunner(t, c)
			foundCLI(t)

			report, err := f.engine.BundlesEnable(t.Context(), "claude")
			if err != nil {
				t.Fatalf("enable: %v", err)
			}

			return c, h, report
		}()

		host.Validate = []string{"manifest error"}
		cli.calls = nil

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When doctor validates", func() {
			Convey("Then the failure surfaces as an error on the plugin directory", func() {
				So(hasIssue(issues, engine.SeverityError, "claude plugin validate failed"), ShouldBeTrue)
				So(cli.calls, ShouldHaveLength, 1)
				So(cli.calls[0][2], ShouldEqual, "validate")
				So(cli.calls[0][len(cli.calls[0])-1], ShouldEqual, filepath.Join(f.vault.BundlesDir(), "claude", "plugins", "beadle-canon"))
			})
		})
	})
}

func TestBundlesEnableGeminiOnlyDedupsMCP(t *testing.T) {
	Convey("Given a gemini bundle", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		foundCLI(t)
		fakeRunner(t, &fakeCLI{respond: geminiExtensionsResponder(`[{"name": "beadle-canon"}]`)})

		report, err := f.engine.BundlesEnable(t.Context(), "gemini")
		So(err, ShouldBeNil)

		Convey("When it is enabled", func() {
			Convey("Then only MCP dedups and skills stay a file surface", func() {
				So(report.Bundles[0].Registered, ShouldBeTrue)
				So(report.Bundles[0].Tier, ShouldEqual, state.VerifyExecuted)
				So(report.Bundles[0].Action, ShouldEqual, "enabled")
				So(f.config.ModeFor(agent.GeminiCLIID, kind.MCP, config.ModeSync), ShouldEqual, config.ModeOff)
				So(f.config.ModeFor(agent.GeminiCLIID, kind.Skills, config.ModeSync), ShouldEqual, config.ModeSync)

				_, extErr := os.Stat(filepath.Join(f.vault.BundlesDir(), "gemini", "gemini-extension.json"))
				_, skillErr := os.Stat(filepath.Join(f.vault.BundlesDir(), "gemini", "skills", "alpha", "SKILL.md"))

				So(extErr, ShouldBeNil)
				So(errors.Is(skillErr, os.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestBundlesGeminiSkipsSecretServer(t *testing.T) {
	Convey("Given a gemini bundle and a secret-bearing server", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		f.config.Enable(agent.GeminiCLIID)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)
		geminiHome(t, f)

		f.engine.Secrets().Set("API_TOKEN", "tok-live-123")
		So(f.engine.Secrets().Save(), ShouldBeNil)

		write(t, f.vault.ServersPath(), `{
			"plain": {"transport": "stdio", "command": ["node", "plain.js"]},
			"secret": {
				"transport": "http",
				"url": "https://example.com/mcp",
				"headers": {"Authorization": "{secret:API_TOKEN}"}
			}
		}`)

		f.sync(t)

		foundCLI(t)
		fakeRunner(t, &fakeCLI{respond: geminiExtensionsResponder(`[{"name": "beadle-canon"}]`)})

		report, err := f.engine.BundlesEnable(t.Context(), "gemini")
		So(err, ShouldBeNil)

		Convey("When the bundle is enabled", func() {
			Convey("Then the secret server stays out of the gemini extension", func() {
				var doc struct {
					MCPServers map[string]json.RawMessage `json:"mcpServers"`
				}

				So(json.Unmarshal([]byte(read(t, filepath.Join(f.vault.BundlesDir(), "gemini", "gemini-extension.json"))), &doc), ShouldBeNil)
				So(doc.MCPServers, ShouldHaveLength, 1)
				So(doc.MCPServers, ShouldContainKey, "plain")

				So(containsWarning(report.Notes, "mcp secret carries secrets; delivered via the host config, not the gemini bundle"), ShouldBeTrue)

				Convey("And the host MCP surface keeps the resolved value", func() {
					settings := hostMCPServers(t, filepath.Join(f.home, ".gemini", "settings.json"), "mcpServers")
					So(settings, ShouldContainKey, "secret")
					So(settings["secret"]["headers"], ShouldResemble, map[string]any{"Authorization": "tok-live-123"})
					So(settings, ShouldNotContainKey, "plain")
				})
			})
		})
	})
}

func TestBundlesEnableAntigravityNeedsLink(t *testing.T) {
	Convey("Given an antigravity bundle needing a manual link", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		missingCLI(t)

		cli := &fakeCLI{}
		fakeRunner(t, cli)

		report, err := f.engine.BundlesEnable(t.Context(), "antigravity")
		So(err, ShouldBeNil)

		Convey("When it is enabled before and after the link exists", func() {
			So(report.Bundles[0].Action, ShouldEqual, "generated")
			So(report.Bundles[0].Registered, ShouldBeFalse)
			So(report.Bundles[0].Tier, ShouldEqual, state.VerifyUnverifiable)
			So(cli.calls, ShouldBeEmpty)
			So(report.Bundles[0].Note, ShouldContainSubstring, f.home)
			So(report.Bundles[0].Note, ShouldContainSubstring, "antigravity-cli/plugins")
			So(report.Bundles[0].Note, ShouldContainSubstring, ".gemini/config/plugins")
			So(f.config.ModeFor(agent.AntigravityCLIID, kind.MCP, config.ModeSync), ShouldEqual, config.ModeSync)

			_, skillErr := os.Stat(filepath.Join(f.vault.BundlesDir(), "antigravity", "skills", "alpha", "SKILL.md"))
			So(skillErr, ShouldBeNil)

			linked := filepath.Join(f.home, ".gemini", "antigravity-cli", "plugins", "beadle-canon")
			So(os.MkdirAll(filepath.Dir(linked), 0o750), ShouldBeNil)
			So(os.Symlink(filepath.Join(f.vault.BundlesDir(), "antigravity"), linked), ShouldBeNil)

			report, err = f.engine.BundlesEnable(t.Context(), "antigravity")
			So(err, ShouldBeNil)
			So(report.Bundles[0].Registered, ShouldBeTrue)
			So(report.Bundles[0].Tier, ShouldEqual, state.VerifyUnverifiable)
			So(f.config.ModeFor(agent.AntigravityCLIID, kind.MCP, config.ModeSync), ShouldEqual, config.ModeSync)

			report, err = f.engine.BundlesEnable(t.Context(), "antigravity")

			Convey("Then it becomes and stays a noop", func() {
				So(err, ShouldBeNil)
				So(report.Bundles[0].Action, ShouldEqual, "noop")
			})
		})
	})
}

func TestBundlesEnableUpdatesRegisteredBundle(t *testing.T) {
	Convey("Given a registered bundle that changed", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		cli, _, first := enableClaude(t, f)

		cli.calls = nil

		write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha v2\n")

		second, err := f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

		st := loadState(t, f)

		Convey("When it is re-enabled", func() {
			Convey("Then it updates with the expected CLI calls", func() {
				So(second.Bundles[0].Action, ShouldEqual, "enabled")
				So(second.Bundles[0].Version, ShouldNotEqual, first.Bundles[0].Version)

				So(cli.calls, ShouldResemble, [][]string{
					{"claude", "plugin", "validate", "--json", "--strict", filepath.Join(f.vault.BundlesDir(), "claude", "plugins", "beadle-canon")},
					{"claude", "plugin", "validate", "--json", "--strict", filepath.Join(f.vault.BundlesDir(), "claude")},
					{"claude", "plugin", "marketplace", "update", "beadle"},
					{"claude", "plugin", "update", "beadle-canon@beadle"},
					{"claude", "plugin", "list", "--json"},
				})

				So(st.Bundles["claude"].Version, ShouldEqual, second.Bundles[0].Version)
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync), ShouldEqual, config.ModeOff)
			})

			Convey("Then disabling restores the saved modes", func() {
				report, err := f.engine.BundlesDisable(t.Context(), "claude")
				So(err, ShouldBeNil)

				So(report.Bundles[0].Action, ShouldEqual, "disabled")
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeOff), ShouldEqual, config.ModeSync)
			})
		})
	})
}

func TestBundlesEnableKeepsSavedModes(t *testing.T) {
	Convey("Given a bundle enabled twice", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		cli, _ := claudeBundleCLI(t, f)
		fakeRunner(t, cli)
		foundCLI(t)

		f.config.SetMode(agent.ClaudeCodeID, kind.Skills, config.ModePull)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		_, err := f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

		_, err = f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

		st := loadState(t, f)

		Convey("When it is disabled", func() {
			_, err = f.engine.BundlesDisable(t.Context(), "claude")
			So(err, ShouldBeNil)

			Convey("Then the original modes are restored", func() {
				So(st.Bundles["claude"].SavedModes, ShouldResemble, map[kind.ID]config.Mode{kind.Skills: config.ModePull, kind.MCP: config.ModeSync})
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync), ShouldEqual, config.ModePull)
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.MCP, config.ModeOff), ShouldEqual, config.ModeSync)
			})
		})
	})
}

func TestBundlesDisableGeminiUnlinks(t *testing.T) {
	Convey("Given an enabled gemini bundle", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		foundCLI(t)

		cli := &fakeCLI{respond: geminiExtensionsResponder(`[{"name": "beadle-canon"}]`)}
		fakeRunner(t, cli)

		_, err := f.engine.BundlesEnable(t.Context(), "gemini")
		So(err, ShouldBeNil)

		cli.calls = nil

		report, err := f.engine.BundlesDisable(t.Context(), "gemini")
		So(err, ShouldBeNil)

		Convey("When it is disabled", func() {
			Convey("Then it unlinks and restores MCP", func() {
				So(report.Bundles[0].Action, ShouldEqual, "disabled")
				So(cli.calls, ShouldResemble, [][]string{{"gemini", "extensions", "unlink", filepath.Join(f.vault.BundlesDir(), "gemini")}})
				So(f.config.ModeFor(agent.GeminiCLIID, kind.MCP, config.ModeOff), ShouldEqual, config.ModeSync)
			})
		})
	})
}

func TestBundlesGeminiProbeFailureKeepsModes(t *testing.T) {
	Convey("Given a gemini listing without the extension", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		foundCLI(t)
		fakeRunner(t, &fakeCLI{respond: geminiExtensionsResponder("no extensions")})

		report, err := f.engine.BundlesEnable(t.Context(), "gemini")
		So(err, ShouldBeNil)

		Convey("When it is enabled", func() {
			Convey("Then the MCP mode is left on and the raw listing is reported", func() {
				So(report.Bundles[0].Action, ShouldEqual, "failed")
				So(report.Bundles[0].Tier, ShouldEqual, state.VerifyFailed)
				So(report.Bundles[0].Note, ShouldContainSubstring, "no extensions")
				So(f.config.ModeFor(agent.GeminiCLIID, kind.MCP, config.ModeSync), ShouldEqual, config.ModeSync)
			})
		})
	})
}

func TestBundlesDisableFailureKeepsRegistrationState(t *testing.T) {
	Convey("Given an unregister failure", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		cli, _, _ := enableClaude(t, f)

		cli.failOn = map[string]error{"plugin uninstall beadle-canon@beadle": errors.New("boom")}
		cli.output = "raw uninstall failure"

		report, err := f.engine.BundlesDisable(t.Context(), "claude")
		So(err, ShouldBeNil)

		st := loadState(t, f)

		cli.calls = nil

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then it fails, keeps state, warns and validates", func() {
				So(report.Bundles[0].Action, ShouldEqual, "failed")
				So(report.Bundles[0].Registered, ShouldBeTrue)
				So(strings.Join(report.Warnings, " "), ShouldContainSubstring, "raw uninstall failure")
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync), ShouldEqual, config.ModeOff)

				So(st.Bundles["claude"].Enabled, ShouldBeFalse)
				So(st.Bundles["claude"].Registered, ShouldBeTrue)

				So(hasIssue(issues, engine.SeverityWarn, "is still registered"), ShouldBeTrue)
				So(cli.calls, ShouldHaveLength, 2)
			})
		})
	})
}

func TestBundleIssuesValidateSkippedWithoutCLI(t *testing.T) {
	Convey("Given a registered bundle and a missing CLI", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		cli, _, _ := enableClaude(t, f)

		missingCLI(t)

		cli.calls = nil

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When doctor runs", func() {
			Convey("Then it warns about the CLI and never invokes validate", func() {
				So(hasIssue(issues, engine.SeverityWarn, "claude CLI not found"), ShouldBeTrue)
				So(cli.calls, ShouldBeEmpty)
			})
		})
	})
}

func TestBundlesEnableFlipFailureRemovesNothing(t *testing.T) {
	Convey("Given a verified enable whose mode flip cannot be saved", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		f.config.Enable(agent.SharedID)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		f.sync(t)

		So(os.Remove(f.vault.ConfigPath()), ShouldBeNil)
		So(os.Mkdir(f.vault.ConfigPath(), 0o700), ShouldBeNil)

		_, err := enableClaudeRaw(t, f)

		Convey("When the enable fails", func() {
			Convey("Then no file was withdrawn", func() {
				So(err, ShouldBeError)
				So(read(t, f.claudeSkill("alpha")), ShouldContainSubstring, "# alpha")
				So(read(t, f.claudeConfig()), ShouldContainSubstring, "plug")
			})
		})
	})
}

func enableClaudeRaw(t *testing.T, f *fixture) (engine.Report, error) {
	t.Helper()

	cli, _ := claudeBundleCLI(t, f)
	fakeRunner(t, cli)
	foundCLI(t)

	return f.engine.BundlesEnable(t.Context(), "claude")
}

func TestBundlePluginMCPPresentedWithModeOff(t *testing.T) {
	Convey("Given a verified gemini bundle and plugin-sourced servers", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		write(t, f.geminiSettings(), `{"mcpServers": {}}`)

		f.config.Enable(agent.GeminiCLIID)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		first := pluginTree(t, f.home, "acme", "one", "1.0.0")
		writeMCPServers(t, first, `{"mcpServers": {"plug-one": {"command": "node", "args": ["one.js"]}}}`)

		f.sync(t)
		So(read(t, f.geminiSettings()), ShouldContainSubstring, "plug-one")

		foundCLI(t)
		fakeRunner(t, &fakeCLI{respond: geminiExtensionsResponder(`[{"name": "beadle-canon"}]`)})

		_, err := f.engine.BundlesEnable(t.Context(), "gemini")
		So(err, ShouldBeNil)
		So(f.config.ModeFor(agent.GeminiCLIID, kind.MCP, config.ModeSync), ShouldEqual, config.ModeOff)

		second := pluginTree(t, f.home, "acme", "two", "1.0.0")
		writeMCPServers(t, second, `{"mcpServers": {"plug-two": {"command": "node", "args": ["two.js"]}}}`)

		f.sync(t)

		Convey("When a new plugin appears while the MCP mode is off", func() {
			Convey("Then its server still reaches the host config", func() {
				So(read(t, f.geminiSettings()), ShouldContainSubstring, "plug-two")
				So(read(t, f.geminiSettings()), ShouldContainSubstring, "plug-one")
			})

			Convey("And a removed plugin's server leaves the host config", func() {
				removeFromRegistry(t, f.home, "acme", "two")
				So(os.RemoveAll(second), ShouldBeNil)

				f.sync(t)

				So(read(t, f.geminiSettings()), ShouldNotContainSubstring, "plug-two")
				So(read(t, f.geminiSettings()), ShouldContainSubstring, "plug-one")
			})
		})
	})
}

func TestBundlePluginMCPStaysOutOfClaude(t *testing.T) {
	Convey("Given a verified claude bundle and a plugin server", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		_, _, _ = enableClaude(t, f)

		plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeMCPServers(t, plugin, `{"mcpServers": {"from-plugin": {"command": "node", "args": ["plug.js"]}}}`)

		f.sync(t)

		Convey("When sync runs with the MCP mode off", func() {
			Convey("Then the plugin server stays out of ~/.claude.json", func() {
				So(read(t, f.claudeConfig()), ShouldNotContainSubstring, "from-plugin")
			})
		})
	})
}

func TestBundlesEnableRespectsTheVaultLock(t *testing.T) {
	Convey("Given a cancelled context", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := f.engine.BundlesEnable(ctx, "claude")

		Convey("Then it refuses to touch the vault", func() {
			So(err, ShouldBeError)

			st, loadErr := state.Load(f.vault.StatePath())
			So(loadErr, ShouldBeNil)
			So(st.Bundles, ShouldBeEmpty)
		})
	})
}
