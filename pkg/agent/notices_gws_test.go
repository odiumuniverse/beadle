package agent_test

import (
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/subagent"
)

func noticesFor(a *agent.Agent, key string, value []byte) []agent.Notice {
	return a.Notices(kind.Subagents, key, value)
}

func noticeText(notices []agent.Notice) string {
	var b strings.Builder

	for _, notice := range notices {
		b.WriteString(notice.Message)
		b.WriteString("\n")
	}

	return b.String()
}

func TestSubagentNotices(t *testing.T) {
	home := t.TempDir()

	Convey("Given a canon with an unmappable tool and a Claude model", t, func() {
		value := subagent.Render(subagent.Document{
			Name: "x", Description: "d", Tools: []string{"TodoWrite"}, Model: "haiku", Body: "b\n",
		})

		Convey("Then Claude reports nothing and the tool hosts report their losses", func() {
			So(noticesFor(agent.ClaudeCode(home, home), "x.md", value), ShouldBeEmpty)

			opencode := noticeText(noticesFor(agent.OpenCode(home, home), "x.md", value))
			So(opencode, ShouldContainSubstring, `unmappable tool "TodoWrite" for opencode`)
			So(opencode, ShouldContainSubstring, "does not map to opencode")

			So(noticeText(noticesFor(agent.Kilo(home, home), "x.md", value)), ShouldContainSubstring, "for kilo")
			So(noticeText(noticesFor(agent.GeminiCLI(home, home), "x.md", value)), ShouldContainSubstring, `unmappable tool "TodoWrite" for gemini`)
			So(noticeText(noticesFor(agent.AntigravityCLI(home, home), "x.md", value)), ShouldContainSubstring, `unmappable tool "TodoWrite" for antigravity`)
			So(noticeText(noticesFor(agent.Cursor(home, home), "x.md", value)), ShouldContainSubstring, "not expressible for cursor")
			So(noticeText(noticesFor(agent.Codex(home, home), "x.md", value)), ShouldContainSubstring, "not expressible for codex")
		})
	})

	Convey("Given a canon whose allowlist has no equivalent on the tool hosts", t, func() {
		value := subagent.Render(subagent.Document{
			Name: "x", Description: "d", Tools: []string{"TodoWrite"}, Body: "b\n",
		})

		Convey("Then the hosts that cannot express it report the fail-open widening", func() {
			So(noticeText(noticesFor(agent.GeminiCLI(home, home), "x.md", value)), ShouldContainSubstring, "the host keeps all tools (fail-open)")
			So(noticeText(noticesFor(agent.AntigravityCLI(home, home), "x.md", value)), ShouldContainSubstring, "the host keeps all tools (fail-open)")
		})
	})

	Convey("Given a canon with only a name", t, func() {
		value := subagent.Render(subagent.Document{Name: "x"})

		Convey("Then Codex reports both required keys it cannot fill", func() {
			text := noticeText(noticesFor(agent.Codex(home, home), "x.md", value))
			So(text, ShouldContainSubstring, "requires a description")
			So(text, ShouldContainSubstring, "requires developer_instructions")
		})
	})

	Convey("Given a canon with an MCP tool whose server has an underscore", t, func() {
		value := subagent.Render(subagent.Document{
			Name: "x", Description: "d", Tools: []string{"mcp__my_server__post"}, Body: "b\n",
		})

		Convey("Then the hosts with an FQN convention report the unexpressible server", func() {
			So(noticeText(noticesFor(agent.OpenCode(home, home), "x.md", value)), ShouldContainSubstring, "server names cannot contain underscores")
			So(noticeText(noticesFor(agent.GeminiCLI(home, home), "x.md", value)), ShouldContainSubstring, "server names cannot contain underscores")
			So(noticeText(noticesFor(agent.AntigravityCLI(home, home), "x.md", value)), ShouldContainSubstring, `unmappable tool "mcp__my_server__post" for antigravity`)
		})
	})

	Convey("Given a canon with readonly and a permission mode", t, func() {
		value := subagent.Render(subagent.Document{
			Name: "x", Description: "d", DisallowedTools: subagent.WriteClass(),
			PermissionMode: "plan", Body: "b\n",
		})

		Convey("Then the hosts that cannot express the deny without an allowlist report it", func() {
			So(noticeText(noticesFor(agent.GeminiCLI(home, home), "x.md", value)), ShouldContainSubstring, "readonly without a tool allowlist")
			So(noticeText(noticesFor(agent.AntigravityCLI(home, home), "x.md", value)), ShouldContainSubstring, "readonly without a tool allowlist")
			So(noticeText(noticesFor(agent.Codex(home, home), "x.md", value)), ShouldContainSubstring, "permissionMode is not expressible for codex")

			// Cursor expresses the read-only state itself; no note is due.
			So(noticesFor(agent.Cursor(home, home), "x.md", value), ShouldBeEmpty)
		})
	})
}
