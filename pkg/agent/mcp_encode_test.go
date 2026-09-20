package agent_test

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

func TestEncodeMCPServers(t *testing.T) {
	Convey("Given canonical MCP servers", t, func() {
		servers := kind.Items{
			"plug": mcp.Encode(mcp.Server{Command: []string{"node", "srv.js"}, Env: map[string]string{"TOKEN": "x"}}),
			"web":  mcp.Encode(mcp.Server{Transport: mcp.TransportHTTP, URL: "https://example.com/mcp"}),
		}

		Convey("When they are encoded per agent dialect", func() {
			claude, err := agent.EncodeMCPServers(agent.ClaudeCodeID, servers)
			So(err, ShouldBeNil)

			gemini, err := agent.EncodeMCPServers(agent.GeminiCLIID, servers)
			So(err, ShouldBeNil)

			antigravity, err := agent.EncodeMCPServers(agent.AntigravityCLIID, servers)
			So(err, ShouldBeNil)

			Convey("Then each dialect uses its own field names", func() {
				So(claude["plug"], ShouldResemble, map[string]any{
					"type":    "stdio",
					"command": "node",
					"args":    []string{"srv.js"},
					"env":     map[string]string{"TOKEN": "x"},
				})
				So(claude["web"], ShouldResemble, map[string]any{"type": "http", "url": "https://example.com/mcp"})
				So(gemini["web"], ShouldResemble, map[string]any{"httpUrl": "https://example.com/mcp"})
				So(antigravity["web"], ShouldResemble, map[string]any{"serverUrl": "https://example.com/mcp"})
			})
		})
	})
}

func TestEncodeMCPServersUnknownAgent(t *testing.T) {
	Convey("Given an agent without a bundle dialect", t, func() {
		Convey("When servers are encoded", func() {
			_, err := agent.EncodeMCPServers(agent.CursorID, kind.Items{})

			Convey("Then it fails", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "no MCP dialect")
			})
		})
	})
}

func TestEncodeMCPServersRejectsBrokenItem(t *testing.T) {
	Convey("Given a broken server item", t, func() {
		Convey("When servers are encoded", func() {
			_, err := agent.EncodeMCPServers(agent.ClaudeCodeID, kind.Items{"plug": []byte("{")})

			Convey("Then it fails naming the server", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "server plug")
			})
		})
	})
}
