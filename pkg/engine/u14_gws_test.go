package engine_test

import (
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
)

// TestSyncReportsConfigMigration pins the one-time v2→v3 defaults report: the
// first real sync prints every flip once, later syncs stay silent.
// TestSyncApprovesCanonHooksOnMigration pins the one-time trust lift: hooks
// already in the vault canon when the config migrates are approved, later
// syncs stay silent, and the approval is persisted.
func TestSyncApprovesCanonHooksOnMigration(t *testing.T) {
	Convey("Given a v2 config and a hook already in the vault canon", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, f.vault.HooksPath(), `{"notify": {"event": "session-start", "command": "echo hi"}}`)
		write(t, f.vault.ConfigPath(), `{
  "version": 2,
  "permissions": "off",
  "history": "git",
  "secrets": "literal",
  "agents": {"claude-code": {"enabled": true}}
}
`)

		cfg, err := config.Load(f.vault.ConfigPath())
		So(err, ShouldBeNil)

		e, err := engine.New(f.vault, cfg, agent.All(f.home, t.TempDir()), engine.WithHome(f.home))
		So(err, ShouldBeNil)

		Convey("When the first real sync runs", func() {
			report, err := e.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then the canon hook is approved with a note", func() {
				notes := strings.Join(report.Notes, "\n")
				So(notes, ShouldContainSubstring, "approved 1 canon hook(s)")
				So(notes, ShouldContainSubstring, "notify")
				So(cfg.HookApproved("notify"), ShouldBeTrue)

				reloaded, err := config.Load(f.vault.ConfigPath())
				So(err, ShouldBeNil)
				So(reloaded.HookApproved("notify"), ShouldBeTrue)
			})

			Convey("And a second sync neither notes nor re-approves", func() {
				second, err := e.Sync(t.Context(), engine.SyncOptions{})
				So(err, ShouldBeNil)
				So(second.Notes, ShouldBeEmpty)
			})
		})

		Convey("When the config is already current", func() {
			So(cfg.Save(f.vault.ConfigPath()), ShouldBeNil)

			current, err := config.Load(f.vault.ConfigPath())
			So(err, ShouldBeNil)

			report := engine.Report{}
			e.ApproveCanonHooks(&report)

			Convey("Then nothing is approved", func() {
				So(report.Notes, ShouldBeEmpty)
				So(current.HookApproved("notify"), ShouldBeFalse)
			})
		})
	})
}

func TestSyncReportsConfigMigration(t *testing.T) {
	Convey("Given a v2 config with the legacy permission default", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		const legacy = `{
  "version": 2,
  "permissions": "off",
  "history": "git",
  "secrets": "literal",
  "agents": {"claude-code": {"enabled": true}}
}
`
		write(t, f.vault.ConfigPath(), legacy)

		cfg, err := config.Load(f.vault.ConfigPath())
		So(err, ShouldBeNil)

		migrated, err := engine.New(f.vault, cfg, agent.All(f.home, t.TempDir()), engine.WithHome(f.home))
		So(err, ShouldBeNil)

		Convey("When a dry run runs first", func() {
			dry, err := migrated.Sync(t.Context(), engine.SyncOptions{DryRun: true})
			So(err, ShouldBeNil)
			So(strings.Join(dry.Warnings, "\n"), ShouldNotContainSubstring, "config: ")
			So(read(t, f.vault.ConfigPath()), ShouldNotContainSubstring, `"version": 3`)

			Convey("When the first real sync runs", func() {
				report, err := migrated.Sync(t.Context(), engine.SyncOptions{})
				So(err, ShouldBeNil)
				So(report.Errors(), ShouldBeEmpty)

				Convey("Then every migration flip is reported once and the config is persisted", func() {
					log := strings.Join(report.Warnings, "\n")
					So(log, ShouldContainSubstring, "config: permissions are synchronized by default now")
					So(log, ShouldContainSubstring, "config: the shared skills surface")
					So(read(t, f.vault.ConfigPath()), ShouldContainSubstring, `"version": 3`)

					Convey("And the second sync is silent", func() {
						second, err := migrated.Sync(t.Context(), engine.SyncOptions{})
						So(err, ShouldBeNil)
						So(strings.Join(second.Warnings, "\n"), ShouldNotContainSubstring, "config: ")
					})
				})
			})
		})
	})
}
