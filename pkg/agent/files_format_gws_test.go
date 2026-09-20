package agent_test

import (
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

const prettyClaudeConfig = `{
  "numStartups": 3,
  "mcpServers": {
    "old": {
      "type": "stdio",
      "command": "old-mcp"
    }
  }
}
`

const prettyClaudeConfigWithNewServer = `{
  "numStartups": 3,
  "mcpServers": {
    "old": {
      "type": "stdio",
      "command": "old-mcp"
    },
    "gh": {
      "command": "gh-mcp",
      "env": {
        "A": "1"
      },
      "type": "stdio"
    }
  }
}
`

const tabClaudeConfig = "{\n\t\"mcpServers\": {\n\t\t\"old\": {\n\t\t\t\"type\": \"stdio\",\n\t\t\t\"command\": \"old-mcp\"\n\t\t}\n\t}\n}\n"

func claudeServers() kind.Items {
	return kind.Items{
		"old": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"old-mcp"}}),
		"gh":  mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"gh-mcp"}, Env: map[string]string{"A": "1"}}),
	}
}

func TestMCPWriteFormatsNewServerPretty(t *testing.T) {
	Convey("Given a two-space pretty Claude config", t, func() {
		home := t.TempDir()
		path := filepath.Join(home, ".claude.json")
		writeFile(t, path, prettyClaudeConfig)

		Convey("When a new server is written", func() {
			So(surfaceOf(t, agent.ClaudeCode(home, t.TempDir()), kind.MCP).Write(t.Context(), claudeServers()), ShouldBeNil)

			Convey("Then only the new fragment is pretty-printed", func() {
				So(readFile(t, path), ShouldEqual, prettyClaudeConfigWithNewServer)
			})
		})

		Convey("When the only old member is replaced by new servers", func() {
			So(surfaceOf(t, agent.ClaudeCode(home, t.TempDir()), kind.MCP).Write(t.Context(), kind.Items{"gh": claudeServers()["gh"]}), ShouldBeNil)

			Convey("Then the new fragment is still pretty-printed", func() {
				So(readFile(t, path), ShouldContainSubstring, "\n    \"gh\": {\n      \"command\": \"gh-mcp\"")
				So(readFile(t, path), ShouldNotContainSubstring, "\"gh\":{")
			})
		})
	})
}

func TestMCPWriteFormatsNewServerIndent(t *testing.T) {
	Convey("Given pretty Claude configs with different indents", t, func() {
		cases := []struct {
			name   string
			indent string
		}{
			{name: "four spaces", indent: "    "},
			{name: "tab", indent: "\t"},
		}

		for _, tc := range cases {
			Convey("When a server is added to a "+tc.name+" file", func() {
				home := t.TempDir()
				path := filepath.Join(home, ".claude.json")
				writeFile(t, path, strings.ReplaceAll(tabClaudeConfig, "\t", tc.indent))

				So(surfaceOf(t, agent.ClaudeCode(home, t.TempDir()), kind.MCP).Write(t.Context(), claudeServers()), ShouldBeNil)

				text := readFile(t, path)

				Convey("Then the fragment follows the detected indent", func() {
					So(text, ShouldContainSubstring, "\n"+tc.indent+tc.indent+`"gh": {`)
					So(text, ShouldContainSubstring, "\n"+tc.indent+tc.indent+tc.indent+`"command": "gh-mcp"`)
					So(text, ShouldContainSubstring, "\n"+tc.indent+tc.indent+tc.indent+`"env": {`)
					So(text, ShouldContainSubstring, "\n"+tc.indent+tc.indent+`"old": {`)
					So(text, ShouldNotContainSubstring, `"gh":{`)
				})
			})
		}
	})
}

func TestPermissionWriteKeepsInlineObjectInline(t *testing.T) {
	Convey("Given an OpenCode config with a single inline bash rule", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, "{\n  \"permission\": {\n    \"bash\": {\"*\": \"allow\"}\n  }\n}\n")

		desired := kind.Items{"bash:*": []byte("allow"), "bash:npm test": []byte("allow")}

		Convey("When another rule is added", func() {
			So(surfaceOf(t, agent.OpenCode(home, t.TempDir()), kind.Permissions).Write(t.Context(), desired), ShouldBeNil)

			Convey("Then the inline object stays inline", func() {
				So(readFile(t, path), ShouldContainSubstring, `"bash": {"*": "allow","npm test":"allow"}`)
			})
		})
	})
}

func TestMCPWriteKeepsComments(t *testing.T) {
	Convey("Given pretty Claude configs with comments", t, func() {
		cases := []struct {
			name    string
			fixture string
			comment string
		}{
			{
				name:    "comment before the closing brace",
				fixture: "{\n  \"mcpServers\": {\n    \"old\": {\n      \"command\": \"old-mcp\",\n      \"type\": \"stdio\"\n    }\n    // keep me\n  }\n}\n",
				comment: "// keep me",
			},
			{
				name:    "comment instead of members",
				fixture: "{\n  \"mcpServers\": {\n    /* none yet */\n  }\n}\n",
				comment: "/* none yet */",
			},
			{
				name:    "inline comment before the closing brace",
				fixture: "{\n  \"mcpServers\": { /* none yet */ }\n}\n",
				comment: "/* none yet */",
			},
		}

		for _, tc := range cases {
			Convey("When a server is added to the "+tc.name+" fixture", func() {
				home := t.TempDir()
				path := filepath.Join(home, ".claude.json")
				writeFile(t, path, tc.fixture)

				desired := kind.Items{"gh": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"gh-mcp"}})}

				So(surfaceOf(t, agent.ClaudeCode(home, t.TempDir()), kind.MCP).Write(t.Context(), desired), ShouldBeNil)

				text := readFile(t, path)

				Convey("Then the comment survives", func() {
					So(text, ShouldContainSubstring, tc.comment)
					So(text, ShouldContainSubstring, `"gh"`)
				})
			})
		}
	})
}

func TestMCPWriteKeepsMinifiedFileMinified(t *testing.T) {
	Convey("Given a minified Claude config", t, func() {
		home := t.TempDir()
		path := filepath.Join(home, ".claude.json")
		writeFile(t, path, `{"numStartups":3,"mcpServers":{"old":{"command":"old-mcp","type":"stdio"}}}`)

		Convey("When a new server is written", func() {
			So(surfaceOf(t, agent.ClaudeCode(home, t.TempDir()), kind.MCP).Write(t.Context(), claudeServers()), ShouldBeNil)

			text := readFile(t, path)

			Convey("Then the file and the fragment stay minified", func() {
				So(strings.Contains(text, "\n"), ShouldBeFalse)
				So(text, ShouldContainSubstring, `"gh":{"command":"gh-mcp"`)
				So(text, ShouldContainSubstring, `"numStartups":3`)
			})
		})
	})

	Convey("Given a minified Claude config with a multi-line comment", t, func() {
		home := t.TempDir()
		path := filepath.Join(home, ".claude.json")
		writeFile(t, path, "{\"numStartups\":3,/* note\n   cont */\"mcpServers\":{\"old\":{\"command\":\"old-mcp\",\"type\":\"stdio\"}}}")

		Convey("When a new server is written", func() {
			So(surfaceOf(t, agent.ClaudeCode(home, t.TempDir()), kind.MCP).Write(t.Context(), claudeServers()), ShouldBeNil)

			text := readFile(t, path)

			Convey("Then the comment survives and the fragment stays minified", func() {
				So(text, ShouldContainSubstring, "/* note\n   cont */")
				So(text, ShouldContainSubstring, `"gh":{"command":"gh-mcp"`)
			})
		})
	})
}
