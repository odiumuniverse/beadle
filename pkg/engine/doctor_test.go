package engine_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
)

func TestDoctorReportsBrokenPluginReferences(t *testing.T) {
	Convey("Given Gemini and Cursor configs referencing missing plugin paths", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.config.Enable(agent.GeminiCLIID)
		f.config.Enable(agent.CursorID)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		keep := filepath.Join(f.home, ".claude", "plugins", "cache", "acme", "tool", "1.0.0", "keep.sh")
		write(t, keep, "x")

		missing := filepath.Join(f.home, ".claude", "plugins", "marketplaces", "thedotmack", "plugin", "scripts", "worker-service.cjs")
		missingTilde := "~/.claude/plugins/marketplaces/thedotmack/plugin/scripts/tilde.cjs"
		missingCursor := filepath.Join(f.home, ".claude", "plugins", "marketplaces", "thedotmack", "plugin", "scripts", "mcp-server.cjs")

		write(t, f.geminiSettings(), fmt.Sprintf(`{
  "hooks": {
    "SessionStart": [{"hooks": [{"command": "\"bun\" \"%s\" hook"}]}],
    "BeforeAgent": [{"hooks": [{"command": "sh %s"}]}],
    "AfterAgent": [{"hooks": [{"command": "${CLAUDE_PLUGIN_ROOT}/scripts/x.cjs"}]}],
    "BeforeTool": [{"hooks": [{"command": "cat %s"}]}]
  },
  "mcpServers": {}
}`, missing, keep, missingTilde))

		write(t, filepath.Join(f.home, ".cursor", "mcp.json"),
			fmt.Sprintf(`{"mcpServers":{"claude-mem":{"command":"node","args":[%q]}}}`, missingCursor))

		write(t, f.claudeRules(), "see "+missing+" for details\n")

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			var broken []engine.Issue

			for _, issue := range issues {
				if strings.Contains(issue.Message, "broken plugin reference") {
					broken = append(broken, issue)
				}
			}

			So(broken, ShouldHaveLength, 3)

			var gemini, cursor []engine.Issue

			for _, issue := range broken {
				switch issue.Agent {
				case agent.GeminiCLIID:
					gemini = append(gemini, issue)
				case agent.CursorID:
					cursor = append(cursor, issue)
				}
			}

			Convey("Then only the unresolved references are reported per agent", func() {
				So(gemini, ShouldHaveLength, 2)
				So(cursor, ShouldHaveLength, 1)

				for _, issue := range broken {
					So(issue.Severity, ShouldEqual, engine.SeverityError)
					So(issue.Message, ShouldNotContainSubstring, "keep.sh")
					So(issue.Message, ShouldNotContainSubstring, "CLAUDE_PLUGIN_ROOT")
				}

				So(gemini[0].Message, ShouldContainSubstring, "worker-service.cjs")
				So(gemini[0].Message, ShouldContainSubstring, "/hooks/SessionStart/0/hooks/0/command")
				So(strings.HasPrefix(gemini[0].Message, "broken plugin reference: ~/.claude/plugins/marketplaces/thedotmack/plugin/scripts/worker-service.cjs ("), ShouldBeTrue)
				So(strings.HasSuffix(gemini[0].Message, "/hooks/SessionStart/0/hooks/0/command)"), ShouldBeTrue)
				So(gemini[1].Message, ShouldContainSubstring, "tilde.cjs")
				So(gemini[1].Message, ShouldContainSubstring, "/hooks/BeforeTool/0/hooks/0/command")
				So(cursor[0].Message, ShouldContainSubstring, "mcp-server.cjs")
				So(cursor[0].Message, ShouldContainSubstring, "/mcpServers/claude-mem/args/0")
			})
		})
	})
}

func TestDoctorPluginReferencesResolve(t *testing.T) {
	Convey("Given plugin references that exist", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.config.Enable(agent.GeminiCLIID)
		f.config.Enable(agent.CursorID)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		keep := filepath.Join(f.home, ".claude", "plugins", "cache", "acme", "tool", "1.0.0", "keep.sh")
		write(t, keep, "x")

		write(t, f.geminiSettings(), fmt.Sprintf(`{
  "hooks": {"SessionStart": [{"hooks": [{"command": "sh %s"}]}]},
  "mcpServers": {}
}`, keep))

		write(t, filepath.Join(f.home, ".cursor", "mcp.json"),
			fmt.Sprintf(`{"mcpServers":{"claude-mem":{"command":"node","args":[%q]}}}`, keep))

		write(t, f.claudeRules(), "# r\n")

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then no broken reference is reported", func() {
				for _, issue := range issues {
					So(issue.Message, ShouldNotContainSubstring, "broken plugin reference")
				}
			})
		})
	})
}
