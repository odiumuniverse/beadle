package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// autoBundleEngine rebuilds the fixture engine with the unattended bundle
// attempt on: the plain engine API stays explicit, the CLI opts in.
func autoBundleEngine(t *testing.T, f *fixture) *engine.Engine {
	t.Helper()

	e, err := engine.New(f.vault, f.config, agent.All(f.home, t.TempDir()), engine.WithHome(f.home), engine.WithBundleAutoEnable())
	if err != nil {
		t.Fatalf("new auto engine: %v", err)
	}

	return e
}

// runOn syncs through an explicitly built engine (the fixture engine has no
// auto-bundle option) and fails the test on sync errors.
func (f *fixture) runOn(t *testing.T, e *engine.Engine, opts engine.SyncOptions) *engine.Report {
	t.Helper()

	report, err := e.Sync(t.Context(), opts)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if errs := report.Errors(); len(errs) != 0 {
		t.Fatalf("sync errors: %v", errs)
	}

	return report
}

func bundleResult(t *testing.T, report *engine.Report, host string) engine.BundleResult {
	t.Helper()

	for _, result := range report.Bundles {
		if result.Host == host {
			return result
		}
	}

	t.Fatalf("no bundle result for %s", host)

	return engine.BundleResult{}
}

func TestSyncAutoEnablesUntouchedHost(t *testing.T) {
	Convey("Given a detected claude with its CLI and an untouched bundle state", t, func() {
		f := bundleFixture(t)

		cli, _ := claudeBundleCLI(t, f)
		fakeRunner(t, cli)
		foundCLI(t)

		e := autoBundleEngine(t, f)

		Convey("When the first full sync runs", func() {
			report := f.runOn(t, e, engine.SyncOptions{})

			Convey("Then the bundle is attempted once, registered and verified", func() {
				result := bundleResult(t, report, "claude")
				So(result.Auto, ShouldBeTrue)
				So(result.Tier, ShouldEqual, state.VerifyExecuted)
				So(result.Registered, ShouldBeTrue)

				entry := loadState(t, f).Bundles["claude"]
				So(entry.Enabled, ShouldBeTrue)
				So(entry.Registered, ShouldBeTrue)
				So(entry.AutoAttempt, ShouldNotBeNil)
				So(entry.AutoAttempt.Tier, ShouldEqual, state.VerifyExecuted)
			})

			Convey("And the file kinds flip off for the same sync", func() {
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync), ShouldEqual, config.ModeOff)
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.MCP, config.ModeSync), ShouldEqual, config.ModeOff)
			})

			Convey("And the next sync neither re-attempts nor re-registers", func() {
				calls := len(cli.calls)

				second := f.runOn(t, e, engine.SyncOptions{})
				So(second.Bundles, ShouldBeEmpty)
				So(len(cli.calls), ShouldEqual, calls)
			})
		})
	})
}

func TestSyncAutoEnableProbeFailureKeepsModes(t *testing.T) {
	Convey("Given a host that installs but never lists the plugin", t, func() {
		f := bundleFixture(t)

		host := &claudeHost{}
		cli := &fakeCLI{}
		cli.respond = host.respond

		fakeRunner(t, cli)
		foundCLI(t)

		e := autoBundleEngine(t, f)

		Convey("When the first full sync runs", func() {
			report := f.runOn(t, e, engine.SyncOptions{})

			Convey("Then the probe fails, the modes stay on and the retry is explicit", func() {
				result := bundleResult(t, report, "claude")
				So(result.Auto, ShouldBeTrue)
				So(result.Tier, ShouldEqual, state.VerifyFailed)
				So(result.Action, ShouldEqual, "failed")
				So(result.Note, ShouldContainSubstring, "beadle bundles enable claude")

				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync), ShouldEqual, config.ModeSync)
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.MCP, config.ModeSync), ShouldEqual, config.ModeSync)

				entry := loadState(t, f).Bundles["claude"]
				So(entry.AutoAttempt, ShouldNotBeNil)
				So(entry.AutoAttempt.Tier, ShouldEqual, state.VerifyFailed)
			})

			Convey("And one settling pass converges the rendered version", func() {
				before := loadState(t, f).Bundles["claude"].Version

				second := f.runOn(t, e, engine.SyncOptions{})
				So(bundleResult(t, second, "claude").Tier, ShouldEqual, state.VerifyFailed)

				settled := loadState(t, f).Bundles["claude"]
				So(settled.Version, ShouldNotEqual, before)

				calls := len(cli.calls)

				Convey("And further syncs do not probe again", func() {
					third := f.runOn(t, e, engine.SyncOptions{})
					So(third.Bundles, ShouldBeEmpty)
					So(len(cli.calls), ShouldEqual, calls)
				})
			})
		})
	})
}

func TestSyncAutoEnableWithoutCLI(t *testing.T) {
	Convey("Given a detected claude without its CLI", t, func() {
		f := bundleFixture(t)

		cli := &fakeCLI{}

		fakeRunner(t, cli)
		missingCLI(t)

		e := autoBundleEngine(t, f)

		Convey("When the first full sync runs", func() {
			report := f.runOn(t, e, engine.SyncOptions{})

			Convey("Then the bundle is rendered with the instruction and stays unverifiable", func() {
				result := bundleResult(t, report, "claude")
				So(result.Auto, ShouldBeTrue)
				So(result.Action, ShouldEqual, "generated")
				So(result.Tier, ShouldEqual, state.VerifyUnverifiable)
				So(result.Note, ShouldContainSubstring, "run: claude plugin marketplace add")

				So(cli.calls, ShouldBeEmpty)

				entry := loadState(t, f).Bundles["claude"]
				So(entry.Enabled, ShouldBeTrue)
				So(entry.Registered, ShouldBeFalse)
				So(entry.AutoAttempt, ShouldNotBeNil)
				So(entry.AutoAttempt.Tier, ShouldEqual, state.VerifyUnverifiable)
			})

			Convey("And the file kinds stay on", func() {
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync), ShouldEqual, config.ModeSync)
				So(f.config.ModeFor(agent.ClaudeCodeID, kind.MCP, config.ModeSync), ShouldEqual, config.ModeSync)
			})

			Convey("And the next sync does not attempt again", func() {
				calls := len(cli.calls)

				second := f.runOn(t, e, engine.SyncOptions{})
				So(second.Bundles, ShouldBeEmpty)
				So(len(cli.calls), ShouldEqual, calls)
				So(cli.calls, ShouldBeEmpty)
			})
		})
	})
}

func TestSyncAutoEnableValidateFailureIsRecorded(t *testing.T) {
	Convey("Given a bundle that fails validation", t, func() {
		f := bundleFixture(t)

		host := &claudeHost{Validate: []string{"broken hook"}}
		cli := &fakeCLI{}
		cli.respond = host.respond

		fakeRunner(t, cli)
		foundCLI(t)

		e := autoBundleEngine(t, f)

		Convey("When the first full sync runs", func() {
			report := f.runOn(t, e, engine.SyncOptions{})

			Convey("Then the failed attempt is recorded without enabling anything", func() {
				result := bundleResult(t, report, "claude")
				So(result.Auto, ShouldBeTrue)
				So(result.Action, ShouldEqual, "failed")
				So(result.Note, ShouldContainSubstring, "broken hook")

				entry := loadState(t, f).Bundles["claude"]
				So(entry.Enabled, ShouldBeFalse)
				So(entry.Registered, ShouldBeFalse)
				So(entry.VerifyTier, ShouldEqual, state.VerifyFailed)
				So(entry.AutoAttempt, ShouldNotBeNil)
				So(entry.AutoAttempt.Tier, ShouldEqual, state.VerifyFailed)
			})

			Convey("And the next sync neither renders nor validates again", func() {
				calls := len(cli.calls)

				second := f.runOn(t, e, engine.SyncOptions{})
				So(second.Bundles, ShouldBeEmpty)
				So(len(cli.calls), ShouldEqual, calls)
			})

			Convey("And the doctor points at the explicit retry", func() {
				issues, err := e.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityInfo, "did not verify after the automatic attempt"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityInfo, "beadle bundles enable claude"), ShouldBeTrue)
			})
		})
	})
}

func TestSyncAutoEnableFailedRetriesOnNewVersion(t *testing.T) {
	Convey("Given an auto attempt whose probe failed on a registered host", t, func() {
		f := bundleFixture(t)

		cli, host := claudeBundleCLI(t, f)
		host.Listed = true
		host.Disabled = true

		fakeRunner(t, cli)
		foundCLI(t)

		e := autoBundleEngine(t, f)

		first := f.runOn(t, e, engine.SyncOptions{})
		So(bundleResult(t, first, "claude").Tier, ShouldEqual, state.VerifyFailed)

		t.Logf("first sync bundles=%+v calls=%d", first.Bundles, len(cli.calls))

		entry := loadState(t, f).Bundles["claude"]
		So(entry.Registered, ShouldBeTrue)
		So(entry.AutoAttempt, ShouldNotBeNil)
		So(entry.AutoAttempt.Tier, ShouldEqual, state.VerifyFailed)

		Convey("When the canon changes and a full sync runs", func() {
			write(t, filepath.Join(f.vault.SkillsDir(), "beta", "SKILL.md"), "# beta\n")

			host.Disabled = false

			calls := len(cli.calls)
			report := f.runOn(t, e, engine.SyncOptions{})

			t.Logf("auto version=%s registered=%s bundles=%+v warnings=%v", entry.AutoAttempt.Version, entry.Version, report.Bundles, report.Warnings)

			Convey("Then the new version is registered and verified", func() {
				result := bundleResult(t, report, "claude")
				So(result.Tier, ShouldEqual, state.VerifyExecuted)
				So(len(cli.calls) > calls, ShouldBeTrue)

				updated := loadState(t, f).Bundles["claude"]
				So(updated.Version, ShouldEqual, result.Version)
				So(updated.Version, ShouldNotEqual, entry.Version)
				So(updated.VerifyTier, ShouldEqual, state.VerifyExecuted)
			})
		})
	})
}

func TestSyncAutoEnableWarnsAboutKeptCopies(t *testing.T) {
	Convey("Given a canon skill delivered to claude as a symlink", t, func() {
		f := bundleFixture(t)

		f.config.Enable(agent.SharedID)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		f.sync(t)

		claudeSkill := filepath.Dir(f.claudeSkill("alpha"))
		So(os.RemoveAll(claudeSkill), ShouldBeNil)
		So(os.Symlink(filepath.Dir(f.sharedSkill("alpha")), claudeSkill), ShouldBeNil)

		cli, _ := claudeBundleCLI(t, f)
		fakeRunner(t, cli)
		foundCLI(t)

		e := autoBundleEngine(t, f)

		Convey("When the first full sync auto-enables the bundle", func() {
			report := f.runOn(t, e, engine.SyncOptions{})

			Convey("Then the kept read-only copy is summarized without a restore hint", func() {
				result := bundleResult(t, report, "claude")
				So(result.Kept, ShouldContain, "skills alpha (read-only)")

				warnings := strings.Join(report.Warnings, "\n")
				So(warnings, ShouldContainSubstring, "kept 1 read-only")
				So(warnings, ShouldNotContainSubstring, "beadle pull")
			})
		})
	})
}

func TestKeptCopiesSummary(t *testing.T) {
	Convey("Given kept copies with mixed reasons", t, func() {
		warnings := engine.WarnKeptCopiesForTest("claude", []string{
			"skills alpha (read-only)",
			"skills beta (unmanaged)",
			"skills gamma (undelivered)",
			"mcp plug (modified)",
			"skills delta (modified)",
		})

		Convey("Then the summary counts every reason and hints only at the edited ones", func() {
			So(warnings, ShouldHaveLength, 1)

			line := warnings[0]
			So(line, ShouldContainSubstring, "kept 2 modified, 1 read-only, 1 undelivered, 1 unmanaged")
			So(line, ShouldContainSubstring, "edited: mcp plug, skills delta")
			So(line, ShouldContainSubstring, "beadle bundles disable claude")
			So(line, ShouldContainSubstring, "beadle pull")
		})
	})

	Convey("Given only read-only kept copies", t, func() {
		warnings := engine.WarnKeptCopiesForTest("claude", []string{"skills alpha (read-only)"})

		Convey("Then there is no restore hint at all", func() {
			So(warnings, ShouldHaveLength, 1)
			So(warnings[0], ShouldContainSubstring, "kept 1 read-only")
			So(warnings[0], ShouldNotContainSubstring, "beadle pull")
		})
	})

	Convey("Given an empty kept list", t, func() {
		Convey("Then nothing is reported", func() {
			So(engine.WarnKeptCopiesForTest("claude", nil), ShouldBeEmpty)
		})
	})
}

func TestSyncAutoEnableRefreshDoesNotRetryFailedProbe(t *testing.T) {
	Convey("Given an auto-verified bundle whose host later breaks", t, func() {
		f := bundleFixture(t)

		cli, host := claudeBundleCLI(t, f)
		fakeRunner(t, cli)
		foundCLI(t)

		e := autoBundleEngine(t, f)

		first := f.runOn(t, e, engine.SyncOptions{})
		So(bundleResult(t, first, "claude").Tier, ShouldEqual, state.VerifyExecuted)

		host.Disabled = true

		write(t, filepath.Join(f.vault.SkillsDir(), "beta", "SKILL.md"), "# beta\n")

		second := f.runOn(t, e, engine.SyncOptions{})
		So(bundleResult(t, second, "claude").Tier, ShouldEqual, state.VerifyFailed)

		Convey("When the next syncs run", func() {
			settle := f.runOn(t, e, engine.SyncOptions{})
			So(bundleResult(t, settle, "claude").Tier, ShouldEqual, state.VerifyFailed)

			calls := len(cli.calls)

			Convey("Then the failed probe settles and is not retried", func() {
				third := f.runOn(t, e, engine.SyncOptions{})
				So(third.Bundles, ShouldBeEmpty)
				So(len(cli.calls), ShouldEqual, calls)
			})
		})
	})
}

func TestSyncAutoEnableValidateFailureWaitsForExplicitRetry(t *testing.T) {
	Convey("Given an auto attempt whose bundle failed validation", t, func() {
		f := bundleFixture(t)

		cli, host := claudeBundleCLI(t, f)
		host.Validate = []string{"broken hook"}

		fakeRunner(t, cli)
		foundCLI(t)

		e := autoBundleEngine(t, f)

		first := f.runOn(t, e, engine.SyncOptions{})
		So(bundleResult(t, first, "claude").Action, ShouldEqual, "failed")

		entry := loadState(t, f).Bundles["claude"]
		So(entry.Enabled, ShouldBeFalse)
		So(entry.AutoAttempt, ShouldNotBeNil)

		Convey("When the canon changes", func() {
			write(t, filepath.Join(f.vault.SkillsDir(), "beta", "SKILL.md"), "# beta\n")

			calls := len(cli.calls)
			second := f.runOn(t, e, engine.SyncOptions{})

			Convey("Then nothing is retried automatically", func() {
				So(second.Bundles, ShouldBeEmpty)
				So(len(cli.calls), ShouldEqual, calls)
			})

			Convey("And the explicit retry verifies the fixed bundle", func() {
				host.Validate = nil

				report, err := e.BundlesEnable(t.Context(), "claude")
				So(err, ShouldBeNil)
				So(bundleResult(t, &report, "claude").Tier, ShouldEqual, state.VerifyExecuted)

				updated := loadState(t, f).Bundles["claude"]
				So(updated.Enabled, ShouldBeTrue)
				So(updated.VerifyTier, ShouldEqual, state.VerifyExecuted)
			})
		})

		Convey("And an unchanged canon is not retried either", func() {
			calls := len(cli.calls)

			second := f.runOn(t, e, engine.SyncOptions{})
			So(second.Bundles, ShouldBeEmpty)
			So(len(cli.calls), ShouldEqual, calls)
		})
	})
}

func TestSyncAutoEnableRespectsExplicitDisable(t *testing.T) {
	Convey("Given a host whose bundle the user disabled", t, func() {
		f := bundleFixture(t)

		cli, _ := claudeBundleCLI(t, f)
		fakeRunner(t, cli)
		foundCLI(t)

		e := autoBundleEngine(t, f)

		first := f.runOn(t, e, engine.SyncOptions{})
		So(bundleResult(t, first, "claude").Tier, ShouldEqual, state.VerifyExecuted)

		_, err := e.BundlesDisable(t.Context(), "claude")
		So(err, ShouldBeNil)

		st := loadState(t, f)
		So(st.Bundles, ShouldBeEmpty)
		So(st.BundleOptedOut("claude"), ShouldBeTrue)

		Convey("When a full sync runs", func() {
			calls := len(cli.calls)

			report := f.runOn(t, e, engine.SyncOptions{})

			Convey("Then the explicit disable wins", func() {
				So(report.Bundles, ShouldBeEmpty)
				So(len(cli.calls), ShouldEqual, calls)
				So(loadState(t, f).Bundles, ShouldBeEmpty)
			})
		})

		Convey("When the user enables it again", func() {
			_, err := e.BundlesEnable(t.Context(), "claude")
			So(err, ShouldBeNil)
			So(loadState(t, f).BundleOptedOut("claude"), ShouldBeFalse)
		})
	})
}

func TestSyncAutoEnableExplicitRetryRefreshesAttempt(t *testing.T) {
	Convey("Given an auto attempt that could not verify without the CLI", t, func() {
		f := bundleFixture(t)

		cli, _ := claudeBundleCLI(t, f)
		fakeRunner(t, cli)
		missingCLI(t)

		e := autoBundleEngine(t, f)

		first := f.runOn(t, e, engine.SyncOptions{})
		So(bundleResult(t, first, "claude").Tier, ShouldEqual, state.VerifyUnverifiable)

		entry := loadState(t, f).Bundles["claude"]
		So(entry.AutoAttempt.Tier, ShouldEqual, state.VerifyUnverifiable)

		Convey("When the CLI appears and the user enables the host by hand", func() {
			foundCLI(t)

			report, err := e.BundlesEnable(t.Context(), "claude")
			So(err, ShouldBeNil)
			So(bundleResult(t, &report, "claude").Tier, ShouldEqual, state.VerifyExecuted)

			Convey("Then the attempt record follows the verified tier", func() {
				updated := loadState(t, f).Bundles["claude"]
				So(updated.VerifyTier, ShouldEqual, state.VerifyExecuted)
				So(updated.AutoAttempt.Tier, ShouldEqual, state.VerifyExecuted)

				issues, err := e.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityInfo, "did not verify after the automatic attempt"), ShouldBeFalse)
			})
		})
	})
}

func TestDoctorSilentAfterExplicitDisable(t *testing.T) {
	Convey("Given an auto attempt that could not verify and a manual disable", t, func() {
		f := bundleFixture(t)

		cli, _ := claudeBundleCLI(t, f)
		fakeRunner(t, cli)
		missingCLI(t)

		e := autoBundleEngine(t, f)

		first := f.runOn(t, e, engine.SyncOptions{})
		So(bundleResult(t, first, "claude").Tier, ShouldEqual, state.VerifyUnverifiable)

		_, err := e.BundlesDisable(t.Context(), "claude")
		So(err, ShouldBeNil)
		So(loadState(t, f).BundleOptedOut("claude"), ShouldBeTrue)

		Convey("When the doctor runs", func() {
			issues, err := e.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then it does not nag about the unattended attempt", func() {
				So(hasIssue(issues, engine.SeverityInfo, "did not verify after the automatic attempt"), ShouldBeFalse)
			})
		})
	})
}

func TestSyncAutoEnableGates(t *testing.T) {
	Convey("Given a detected claude with its CLI", t, func() {
		f := bundleFixture(t)

		cli, _ := claudeBundleCLI(t, f)
		fakeRunner(t, cli)
		foundCLI(t)

		e := autoBundleEngine(t, f)

		Convey("When a dry run runs", func() {
			f.runOn(t, e, engine.SyncOptions{DryRun: true})

			Convey("Then nothing is registered or attempted", func() {
				So(loadState(t, f).Bundles, ShouldBeEmpty)
				So(cli.calls, ShouldBeEmpty)
			})
		})

		Convey("When a pull sync runs", func() {
			f.runOn(t, e, engine.SyncOptions{Direction: config.ModePull})

			Convey("Then nothing is registered or attempted", func() {
				So(loadState(t, f).Bundles, ShouldBeEmpty)
				So(cli.calls, ShouldBeEmpty)
			})
		})

		Convey("When a kind-scoped sync runs", func() {
			f.runOn(t, e, engine.SyncOptions{Kinds: []kind.ID{kind.Rules}})

			Convey("Then nothing is registered or attempted", func() {
				So(loadState(t, f).Bundles, ShouldBeEmpty)
				So(cli.calls, ShouldBeEmpty)
			})
		})

		Convey("When the engine was built without the auto option", func() {
			f.run(t, engine.SyncOptions{})

			Convey("Then the plain engine API stays explicit", func() {
				So(loadState(t, f).Bundles, ShouldBeEmpty)
				So(cli.calls, ShouldBeEmpty)
			})
		})
	})

	Convey("Given a host the user disabled before", t, func() {
		f := bundleFixture(t)

		cli, _ := claudeBundleCLI(t, f)
		fakeRunner(t, cli)
		foundCLI(t)

		st := loadState(t, f)
		st.Bundles["claude"] = state.BundleState{Enabled: false}
		So(st.Save(f.vault.StatePath()), ShouldBeNil)

		e := autoBundleEngine(t, f)

		Convey("When a full sync runs", func() {
			report := f.runOn(t, e, engine.SyncOptions{})

			Convey("Then the explicit disable wins", func() {
				So(report.Bundles, ShouldBeEmpty)
				So(cli.calls, ShouldBeEmpty)
				So(loadState(t, f).Bundles["claude"].AutoAttempt, ShouldBeNil)
			})
		})
	})

	Convey("Given a claude agent that is not enabled", t, func() {
		f := bundleFixture(t)

		cli, _ := claudeBundleCLI(t, f)
		fakeRunner(t, cli)
		foundCLI(t)

		f.config.Disable(agent.ClaudeCodeID)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		e := autoBundleEngine(t, f)

		Convey("When a full sync runs", func() {
			f.runOn(t, e, engine.SyncOptions{})

			Convey("Then the disabled agent is left alone", func() {
				So(loadState(t, f).Bundles, ShouldBeEmpty)
				So(cli.calls, ShouldBeEmpty)
			})
		})
	})
}

func TestAutoEnableBundlesEntryPoint(t *testing.T) {
	Convey("Given a detected claude with its CLI", t, func() {
		f := bundleFixture(t)

		cli, _ := claudeBundleCLI(t, f)
		fakeRunner(t, cli)
		foundCLI(t)

		e := autoBundleEngine(t, f)

		Convey("When the init entry point runs", func() {
			report, err := e.AutoEnableBundles(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the bundle registers and the modes flip", func() {
				result := bundleResult(t, &report, "claude")
				So(result.Auto, ShouldBeTrue)
				So(result.Tier, ShouldEqual, state.VerifyExecuted)

				So(f.config.ModeFor(agent.ClaudeCodeID, kind.Skills, config.ModeSync), ShouldEqual, config.ModeOff)
			})

			Convey("And a second call is a no-op", func() {
				calls := len(cli.calls)

				again, err := e.AutoEnableBundles(t.Context())
				So(err, ShouldBeNil)
				So(again.Bundles, ShouldBeEmpty)
				So(len(cli.calls), ShouldEqual, calls)
			})
		})
	})
}
