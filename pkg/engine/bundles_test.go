package engine_test

import (
	"errors"
	"fmt"
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
	calls  [][]string
	failOn map[string]error
	output string
}

func (f *fakeCLI) Run(name string, args []string, _ []byte) ([]byte, int, error) {
	f.calls = append(f.calls, append([]string{name}, args...))

	if err := f.failOn[strings.Join(args, " ")]; err != nil {
		return []byte(f.output), 1, err
	}

	return []byte(f.output), 0, nil
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

func TestBundlesEnableRegistersClaude(t *testing.T) {
	Convey("Given an approved hook canon and a claude CLI", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		foundCLI(t)

		cli := &fakeCLI{}
		fakeRunner(t, cli)

		report, err := f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

		dir := filepath.Join(f.vault.BundlesDir(), "claude")

		st := loadState(t, f)
		entry := st.Bundles["claude"]

		Convey("When it is enabled", func() {
			Convey("Then the CLI registers the bundle, kinds dedup and the plugin is rendered", func() {
				So(report.Bundles, ShouldHaveLength, 1)
				So(report.Bundles[0].Action, ShouldEqual, "enabled")
				So(report.Bundles[0].Registered, ShouldBeTrue)

				matched, matchErr := regexp.MatchString(`^0\.0\.0-[0-9a-f]{12}$`, report.Bundles[0].Version)
				So(matchErr, ShouldBeNil)
				So(matched, ShouldBeTrue)

				So(cli.calls, ShouldResemble, [][]string{
					{"claude", "plugin", "marketplace", "add", dir},
					{"claude", "plugin", "install", "beadle-canon@beadle"},
				})

				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync), ShouldEqual, config.ModeOff)
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.MCP, config.ModeSync), ShouldEqual, config.ModeOff)

				So(entry.Enabled, ShouldBeTrue)
				So(entry.Registered, ShouldBeTrue)
				So(entry.SavedModes, ShouldResemble, map[kind.ID]config.Mode{kind.Skills: config.ModeSync, kind.MCP: config.ModeSync})

				marketplace := read(t, filepath.Join(dir, ".claude-plugin", "marketplace.json"))
				So(marketplace, ShouldContainSubstring, `"name": "beadle"`)
				So(marketplace, ShouldContainSubstring, entry.Version)
				So(read(t, filepath.Join(dir, "plugins", "beadle-canon", "skills", "alpha", "SKILL.md")), ShouldContainSubstring, "# alpha")
				So(read(t, filepath.Join(dir, "plugins", "beadle-canon", "hooks", "hooks.json")), ShouldContainSubstring, "echo hi")
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
			Convey("Then it only generates files and keeps file sync on", func() {
				So(report.Bundles, ShouldHaveLength, 1)
				So(report.Bundles[0].Action, ShouldEqual, "generated")
				So(report.Bundles[0].Registered, ShouldBeFalse)
				So(report.Bundles[0].Note, ShouldContainSubstring, "claude plugin marketplace add")
				So(strings.Join(report.Warnings, " "), ShouldContainSubstring, "claude CLI not found")
				So(cli.calls, ShouldBeEmpty)

				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync), ShouldEqual, config.ModeSync)

				So(st.Bundles["claude"].Enabled, ShouldBeTrue)
				So(st.Bundles["claude"].Registered, ShouldBeFalse)
			})
		})
	})
}

func TestBundlesEnableCLIFailureShowsOutput(t *testing.T) {
	Convey("Given an install failure", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		foundCLI(t)

		cli := &fakeCLI{failOn: map[string]error{"plugin install beadle-canon@beadle": errors.New("boom")}, output: "raw install failure"}
		fakeRunner(t, cli)

		report, err := f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

		Convey("When it is enabled", func() {
			Convey("Then the raw output is surfaced and file sync stays on", func() {
				So(report.Bundles[0].Action, ShouldEqual, "generated")
				So(report.Bundles[0].Registered, ShouldBeFalse)
				So(report.Bundles[0].Note, ShouldContainSubstring, "raw install failure")
				So(strings.Join(report.Warnings, " "), ShouldContainSubstring, "raw install failure")
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync), ShouldEqual, config.ModeSync)
			})
		})
	})
}

func TestBundlesEnableIdempotent(t *testing.T) {
	Convey("Given an already enabled bundle", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		foundCLI(t)

		cli := &fakeCLI{}
		fakeRunner(t, cli)

		_, err := f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

		Convey("When it is enabled again", func() {
			report, err := f.engine.BundlesEnable(t.Context(), "claude")
			So(err, ShouldBeNil)

			Convey("Then no CLI call and the action is noop", func() {
				So(cli.calls, ShouldHaveLength, 2)
				So(report.Bundles[0].Action, ShouldEqual, "noop")
				So(cli.calls, ShouldHaveLength, 2)
			})
		})
	})
}

func TestBundlesDisableRestoresModes(t *testing.T) {
	Convey("Given an enabled bundle", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		foundCLI(t)

		cli := &fakeCLI{}
		fakeRunner(t, cli)

		_, err := f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

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

func TestBundlesDisableRetriesAfterFailure(t *testing.T) {
	Convey("Given a bundle whose unregister failed", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		foundCLI(t)

		cli := &fakeCLI{}
		fakeRunner(t, cli)

		_, err := f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

		cli.failOn = map[string]error{"plugin uninstall beadle-canon@beadle": errors.New("boom")}

		_, err = f.engine.BundlesDisable(t.Context(), "claude")
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
		foundCLI(t)

		cli := &fakeCLI{}
		fakeRunner(t, cli)

		first, err := f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

		cli.failOn = map[string]error{"plugin update beadle-canon@beadle": errors.New("boom")}
		cli.output = "raw update failure"

		write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha v2\n")

		Convey("When it is re-enabled", func() {
			report, err := f.engine.BundlesEnable(t.Context(), "claude")
			So(err, ShouldBeNil)

			st := loadState(t, f)

			Convey("Then the registered version is reported and the raw output noted", func() {
				So(report.Bundles[0].Action, ShouldEqual, "enabled")
				So(report.Bundles[0].Version, ShouldEqual, first.Bundles[0].Version)
				So(report.Bundles[0].Note, ShouldContainSubstring, "raw update failure")
				So(strings.Join(report.Warnings, " "), ShouldContainSubstring, "raw update failure")

				So(st.Bundles["claude"].Version, ShouldEqual, first.Bundles[0].Version)
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

func TestBundlesDisableWithoutCLIKeepsRegistrationState(t *testing.T) {
	Convey("Given a registered bundle and a missing CLI", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		foundCLI(t)
		fakeRunner(t, &fakeCLI{})

		_, err := f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

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
		So(f.config.ModeFor(agent.AntigravityCLIID, kind.MCP, config.ModeSync), ShouldEqual, config.ModeOff)

		report, err := f.engine.BundlesDisable(t.Context(), "antigravity")
		So(err, ShouldBeNil)

		Convey("When the link is removed and it is disabled again", func() {
			So(report.Bundles[0].Action, ShouldEqual, "failed")
			So(report.Bundles[0].Note, ShouldContainSubstring, "remove the plugin link first")
			So(f.config.ModeFor(agent.AntigravityCLIID, kind.MCP, config.ModeSync), ShouldEqual, config.ModeOff)

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

func TestBundlesSyncRerendersAndDoctorWarnsStale(t *testing.T) {
	Convey("Given an enabled bundle whose canon changes", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		foundCLI(t)

		cli := &fakeCLI{}
		fakeRunner(t, cli)

		report, err := f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

		registered := report.Bundles[0].Version
		cli.calls = nil

		write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha v2\n")

		f.sync(t)

		manifest := read(t, filepath.Join(f.vault.BundlesDir(), "claude", "plugins", "beadle-canon", ".claude-plugin", "plugin.json"))

		Convey("When sync re-renders the bundle", func() {
			So(cli.calls, ShouldBeEmpty)
			So(manifest, ShouldNotContainSubstring, registered)
			So(manifest, ShouldContainSubstring, "0.0.0-")

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then doctor warns the bundle is stale", func() {
				So(hasIssue(issues, engine.SeverityWarn, "is stale"), ShouldBeTrue)
			})
		})
	})
}

func TestBundleIssuesDedupConflict(t *testing.T) {
	Convey("Given a bundle whose kind is synced as files again", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		foundCLI(t)
		fakeRunner(t, &fakeCLI{})

		_, err := f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

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
		foundCLI(t)

		cli := &fakeCLI{}
		fakeRunner(t, cli)

		_, err := f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

		cli.failOn = map[string]error{"plugin validate " + filepath.Join(f.vault.BundlesDir(), "claude"): errors.New("bad manifest")}
		cli.output = "manifest error"

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When doctor validates", func() {
			Convey("Then the failure surfaces as an error", func() {
				So(hasIssue(issues, engine.SeverityError, "claude plugin validate failed"), ShouldBeTrue)
			})
		})
	})
}

func TestBundlesEnableGeminiOnlyDedupsMCP(t *testing.T) {
	Convey("Given a gemini bundle", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		foundCLI(t)

		cli := &fakeCLI{}
		fakeRunner(t, cli)

		report, err := f.engine.BundlesEnable(t.Context(), "gemini")
		So(err, ShouldBeNil)

		Convey("When it is enabled", func() {
			Convey("Then only MCP dedups and skills stay a file surface", func() {
				So(report.Bundles[0].Registered, ShouldBeTrue)
				So(cli.calls, ShouldResemble, [][]string{{"gemini", "extensions", "link", filepath.Join(f.vault.BundlesDir(), "gemini")}})
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

func TestBundlesEnableAntigravityNeedsLink(t *testing.T) {
	Convey("Given an antigravity bundle needing a manual link", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		foundCLI(t)

		cli := &fakeCLI{}
		fakeRunner(t, cli)

		report, err := f.engine.BundlesEnable(t.Context(), "antigravity")
		So(err, ShouldBeNil)

		Convey("When it is enabled before and after the link exists", func() {
			So(report.Bundles[0].Action, ShouldEqual, "generated")
			So(report.Bundles[0].Registered, ShouldBeFalse)
			So(cli.calls, ShouldBeEmpty)
			So(report.Bundles[0].Note, ShouldContainSubstring, f.home)
			So(f.config.ModeFor(agent.AntigravityCLIID, kind.MCP, config.ModeSync), ShouldEqual, config.ModeSync)

			_, skillErr := os.Stat(filepath.Join(f.vault.BundlesDir(), "antigravity", "skills", "alpha", "SKILL.md"))
			So(skillErr, ShouldBeNil)

			linked := filepath.Join(f.home, ".gemini", "antigravity-cli", "plugins", "beadle-canon")
			So(os.MkdirAll(filepath.Dir(linked), 0o750), ShouldBeNil)
			So(os.MkdirAll(linked, 0o750), ShouldBeNil)

			report, err = f.engine.BundlesEnable(t.Context(), "antigravity")
			So(err, ShouldBeNil)
			So(report.Bundles[0].Registered, ShouldBeTrue)
			So(f.config.ModeFor(agent.AntigravityCLIID, kind.MCP, config.ModeSync), ShouldEqual, config.ModeOff)

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
		foundCLI(t)

		cli := &fakeCLI{}
		fakeRunner(t, cli)

		first, err := f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

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
					{"claude", "plugin", "marketplace", "update", "beadle"},
					{"claude", "plugin", "update", "beadle-canon@beadle"},
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
		foundCLI(t)
		fakeRunner(t, &fakeCLI{})

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

		cli := &fakeCLI{}
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

func TestBundlesDisableFailureKeepsRegistrationState(t *testing.T) {
	Convey("Given an unregister failure", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		foundCLI(t)

		cli := &fakeCLI{}
		fakeRunner(t, cli)

		_, err := f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

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
				So(cli.calls, ShouldResemble, [][]string{{"claude", "plugin", "validate", filepath.Join(f.vault.BundlesDir(), "claude")}})
			})
		})
	})
}

func TestBundlesRefreshSkipsDryRunAndPull(t *testing.T) {
	Convey("Given an enabled bundle", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		foundCLI(t)
		fakeRunner(t, &fakeCLI{})

		report, err := f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

		manifest := filepath.Join(f.vault.BundlesDir(), "claude", "plugins", "beadle-canon", ".claude-plugin", "plugin.json")
		before := read(t, manifest)

		write(t, filepath.Join(f.vault.SkillsDir(), "alpha", "SKILL.md"), "# alpha v2\n")

		Convey("When dry-run, pull and sync run", func() {
			f.run(t, engine.SyncOptions{DryRun: true})
			So(read(t, manifest), ShouldEqual, before)

			f.run(t, engine.SyncOptions{Direction: config.ModePull})
			So(read(t, manifest), ShouldEqual, before)

			f.sync(t)
			after := read(t, manifest)

			Convey("Then only the real sync re-renders a new version", func() {
				So(after, ShouldNotEqual, before)
				So(after, ShouldNotContainSubstring, report.Bundles[0].Version)
			})
		})
	})
}

func TestBundleIssuesValidateSkippedWithoutCLI(t *testing.T) {
	Convey("Given a registered bundle and a missing CLI", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := bundleFixture(t)
		foundCLI(t)

		cli := &fakeCLI{}
		fakeRunner(t, cli)

		_, err := f.engine.BundlesEnable(t.Context(), "claude")
		So(err, ShouldBeNil)

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
