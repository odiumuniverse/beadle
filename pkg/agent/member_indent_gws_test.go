package agent_test

import (
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

//nolint:funlen // one scenario per comment shape: line, block, blank line, multi-add
func TestWriteIndentsAddedMemberAfterTrailingComment(t *testing.T) {
	Convey("Given a pretty MCP container whose last member has a trailing line comment", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{
  "mcp": {
    "servers": {
      "a": {"type": "local", "command": ["a-mcp"]} // keep
    }
  }
}
`)

		surface := openCodeSurface(t, home, kind.MCP)
		desired := kind.Items{
			"a": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"a-mcp"}}),
			"b": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"b-mcp"}}),
		}

		So(surface.Write(t.Context(), desired), ShouldBeNil)

		Convey("Then the added member is indented like its siblings and the comment survives", func() {
			text := readFile(t, path)
			So(text, ShouldContainSubstring, "// keep")
			So(text, ShouldContainSubstring, "\n      \"b\": {")
			So(text, ShouldContainSubstring, "\n        \"command\": [")
			So(text, ShouldNotContainSubstring, "\n\"b\"")
			So(text, ShouldNotContainSubstring, "} // keep")
		})
	})

	Convey("Given a block comment on the last member's line", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		cases := []struct {
			name     string
			fixture  string
			expected string
		}{
			{
				name: "blank line before the brace",
				fixture: `{
  "mcp": {
    "servers": {
      "a": {"type": "local", "command": ["a-mcp"]} /* keep */

    }
  }
}
`,
				expected: "/* keep */\n      \"b\": {",
			},
			{
				name: "single newline before the brace",
				fixture: `{
  "mcp": {
    "servers": {
      "a": {"type": "local", "command": ["a-mcp"]} /* keep */
    }
  }
}
`,
				expected: "\"command\": [\"a-mcp\"]}, /* keep */\n      \"b\": {",
			},
		}

		for _, tc := range cases {
			Convey(tc.name, func() {
				home := t.TempDir()
				path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
				writeFile(t, path, tc.fixture)

				surface := openCodeSurface(t, home, kind.MCP)
				desired := kind.Items{
					"a": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"a-mcp"}}),
					"b": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"b-mcp"}}),
				}

				So(surface.Write(t.Context(), desired), ShouldBeNil)

				Convey("Then the comment stays with its member and the added member is indented", func() {
					text := readFile(t, path)
					So(text, ShouldContainSubstring, tc.expected)
					So(text, ShouldNotContainSubstring, "} /* keep */")
				})
			})
		}
	})

	Convey("Given a block comment and two added members", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{
  "mcp": {
    "servers": {
      "a": {"type": "local", "command": ["a-mcp"]} /* keep */
    }
  }
}
`)

		surface := openCodeSurface(t, home, kind.MCP)
		desired := kind.Items{
			"a": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"a-mcp"}}),
			"b": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"b-mcp"}}),
			"c": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"c-mcp"}}),
		}

		So(surface.Write(t.Context(), desired), ShouldBeNil)

		Convey("Then the comment stays with the original member", func() {
			text := readFile(t, path)
			So(text, ShouldContainSubstring, "\"command\": [\"a-mcp\"]}, /* keep */\n      \"b\": {")
			So(text, ShouldNotContainSubstring, "} /* keep */")
		})
	})

	Convey("Given a v1 permission map whose last rule has a trailing comment", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{
  "permission": {
    "read": "allow" // keep
  }
}
`)

		surface := openCodeSurface(t, home, kind.Permissions)
		desired := kind.Items{
			"tool:read": []byte("allow"),
			"tool:edit": []byte("deny"),
		}

		So(surface.Write(t.Context(), desired), ShouldBeNil)

		Convey("Then the added rule is indented like its siblings and the comment survives", func() {
			text := readFile(t, path)
			So(text, ShouldContainSubstring, "// keep")
			So(text, ShouldContainSubstring, "\n    \"edit\": \"deny\"")
			So(text, ShouldNotContainSubstring, "\n\"edit\"")
		})
	})
}
