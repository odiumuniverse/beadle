package agent_test

import (
	"errors"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

func TestOmpMCPSurface(t *testing.T) {
	Convey("Given an omp adapter", t, func() {
		home := t.TempDir()
		cwd := t.TempDir()

		withoutOmpEnv(t)

		a := agent.Omp(home, cwd)
		path := filepath.Join(home, ".omp", "agent", "mcp.json")

		Convey("Then the native mcp.json is the write target", func() {
			So(surfaceOf(t, a, kind.MCP).Path(), ShouldEqual, path)
		})

		Convey("When the native file does not exist", func() {
			Convey("Then a write fails instead of creating a config omp did not have", func() {
				err := surfaceOf(t, a, kind.MCP).Write(t.Context(), kind.Items{"srv": stdioServer("npx", "-y", "beadle")})

				So(err, ShouldBeError)
				So(errors.Is(err, agent.ErrNotConfigured), ShouldBeTrue)
			})
		})

		Convey("When the native file carries foreign and omp-only keys", func() {
			const fixture = `{
  "mcpServers": {
    "srv": {
      "type": "stdio",
      "command": "npx",
      "args": ["-y", "beadle"],
      "env": {"K": "V"},
      "timeout": 30,
      "enabled": true,
      "auth": {"type": "bearer"}
    }
  }
}
`
			writeFile(t, path, fixture)

			Convey("Then the server reads into the canon and a rewrite is idempotent", func() {
				snap := snapshot(t, a, kind.MCP)
				So(server(t, snap.Items["srv"]).Command, ShouldResemble, []string{"npx", "-y", "beadle"})

				desired := kind.Items{
					"srv":         mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"beadle", "--serve"}}),
					"fresh":       stdioServer("fresh"),
					"remote-test": mcp.Encode(mcp.Server{Transport: mcp.TransportHTTP, URL: "https://example.com/mcp"}),
				}

				surface := surfaceOf(t, a, kind.MCP)
				So(surface.Write(t.Context(), desired), ShouldBeNil)

				out := readFile(t, path)
				So(out, ShouldContainSubstring, `"timeout":30`)
				So(out, ShouldContainSubstring, `"enabled":true`)
				So(out, ShouldContainSubstring, `"auth":{"type":"bearer"}`)
				So(out, ShouldContainSubstring, "https://example.com/mcp")

				back := snapshot(t, a, kind.MCP)
				So(desired.Equal(back.Items), ShouldBeTrue)

				So(surface.Write(t.Context(), desired), ShouldBeNil)
				So(readFile(t, path), ShouldEqual, out)
			})
		})

		Convey("When the compatibility file .mcp.json exists", func() {
			compat := filepath.Join(home, ".omp", "agent", ".mcp.json")

			const compatFixture = "{\"mcpServers\": {\"compat\": {\"type\": \"stdio\", \"command\": \"compat\"}}}\n"

			writeFile(t, compat, compatFixture)
			writeFile(t, path, "{\"mcpServers\": {}}\n")

			Convey("Then it is neither read nor written", func() {
				snap := snapshot(t, a, kind.MCP)
				So(snap.Items, ShouldNotContainKey, "compat")

				So(surfaceOf(t, a, kind.MCP).Write(t.Context(), kind.Items{"srv": stdioServer("srv")}), ShouldBeNil)

				So(readFile(t, path), ShouldContainSubstring, `"srv"`)
				So(readFile(t, compat), ShouldEqual, compatFixture)
			})
		})
	})
}
