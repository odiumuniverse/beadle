package engine_test

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func syncWithOpenCodeMCPServer(t *testing.T) (*fixture, *engine.Report) {
	t.Helper()

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	write(t, f.openCodeConfig(), `{"mcp": {"alpha": {"type": "local", "command": ["a"]}}}`)

	return f, f.sync(t)
}

func TestReloadHintOnlyOnWrite(t *testing.T) {
	Convey("Given a first sync that pushes a new MCP server", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f, report := syncWithOpenCodeMCPServer(t)

		claude, ok := report.Kind(kind.MCP).Agent(agent.ClaudeCodeID)
		So(ok, ShouldBeTrue)

		Convey("When the same sync runs again", func() {
			report = f.sync(t)

			claude, ok = report.Kind(kind.MCP).Agent(agent.ClaudeCodeID)
			So(ok, ShouldBeTrue)

			Convey("Then the hint appears on the write and nowhere on the noop", func() {
				So(claude.Action, ShouldEqual, engine.ActionNoop)
				So(claude.ReloadHint, ShouldBeEmpty)
			})
		})

		Convey("Then the push carries the reload hint", func() {
			So(claude.Action, ShouldEqual, engine.ActionPushed)
			So(claude.ReloadHint, ShouldEqual, "new Claude Code sessions load MCP changes")
		})
	})
}

func TestReloadHintAbsentOnDryRun(t *testing.T) {
	Convey("Given a dry-run sync", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, f.claudeConfig(), `{"mcpServers": {}}`)
		write(t, f.openCodeConfig(), `{"mcp": {"alpha": {"type": "local", "command": ["a"]}}}`)

		report := f.run(t, engine.SyncOptions{DryRun: true})

		claude, ok := report.Kind(kind.MCP).Agent(agent.ClaudeCodeID)
		So(ok, ShouldBeTrue)

		Convey("Then it would push but carries no hint", func() {
			So(claude.Action, ShouldEqual, engine.ActionWouldPush)
			So(claude.ReloadHint, ShouldBeEmpty)
		})
	})
}

func TestReloadHintAbsentOnSkipped(t *testing.T) {
	Convey("Given a server whose secret is missing", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, f.vault.ServersPath(), `{"alpha": {"command": ["a"], "env": {"TOKEN": "{secret:GONE_MISSING}"}}}`)

		report := f.sync(t)

		claude, ok := report.Kind(kind.MCP).Agent(agent.ClaudeCodeID)
		So(ok, ShouldBeTrue)

		Convey("Then the write is skipped and carries no hint", func() {
			So(claude.Action, ShouldEqual, engine.ActionSkipped)
			So(claude.ReloadHint, ShouldBeEmpty)
		})
	})
}

func TestReloadHintAbsentForRules(t *testing.T) {
	Convey("Given a changed rules canon", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		f.sync(t)

		write(t, f.vault.RulesPath(), "# canon rules v2\n")

		report := f.sync(t)

		claude, ok := report.Kind(kind.Rules).Agent(agent.ClaudeCodeID)
		So(ok, ShouldBeTrue)

		Convey("Then rules push without a reload hint by design", func() {
			So(claude.Action, ShouldEqual, engine.ActionPushed)
			So(claude.ReloadHint, ShouldBeEmpty)
		})
	})
}
