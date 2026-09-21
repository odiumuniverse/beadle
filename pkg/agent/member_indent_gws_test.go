package agent_test

import (
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

func TestWriteIndentsAddedMemberAfterTrailingComment(t *testing.T) {
	Convey("Given a pretty MCP container whose last member has a trailing comment", t, func() {
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
		})
	})

	Convey("Given a block comment on the last member's line and a blank line before the brace", t, func() {
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
		}

		So(surface.Write(t.Context(), desired), ShouldBeNil)

		Convey("Then the added member starts on its own indented line", func() {
			text := readFile(t, path)
			So(text, ShouldContainSubstring, "/* keep */\n      \"b\": {")
			So(text, ShouldNotContainSubstring, "*/\"b\"")
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
