package agent_test

import (
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
)

func TestInboxPath(t *testing.T) {
	Convey("Given a home directory", t, func() {
		home := t.TempDir()

		t.Setenv("XDG_CONFIG_HOME", "")

		Convey("When inbox paths are resolved", func() {
			Convey("Then each agent gets its own inbox or none", func() {
				So(agent.InboxPath(home, agent.OpenCodeID), ShouldEqual, filepath.Join(home, ".config", "opencode", "inbox.md"))
				So(agent.InboxPath(home, agent.GeminiCLIID), ShouldEqual, filepath.Join(home, ".gemini", "inbox.md"))
				So(agent.InboxPath(home, agent.CursorID), ShouldEqual, filepath.Join(home, ".cursor", "inbox.md"))
				So(agent.InboxPath(home, agent.ClaudeCodeID), ShouldBeEmpty)
				So(agent.InboxPath(home, agent.SharedID), ShouldBeEmpty)
			})
		})

		Convey("When XDG_CONFIG_HOME is set", func() {
			sandbox := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", sandbox)

			Convey("Then only OpenCode follows it", func() {
				So(agent.InboxPath(home, agent.OpenCodeID), ShouldEqual, filepath.Join(sandbox, "opencode", "inbox.md"))
				So(agent.InboxPath(home, agent.GeminiCLIID), ShouldEqual, filepath.Join(home, ".gemini", "inbox.md"))
			})
		})
	})
}
