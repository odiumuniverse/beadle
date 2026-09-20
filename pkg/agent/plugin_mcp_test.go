package agent_test

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

type pluginMCPCase struct {
	name     string
	doc      string
	want     map[string]mcp.Server
	warnings []string
	wantErr  bool
}

const pluginMCPRoot = "/vault/plugins/acme/tool/current"

func assertPluginMCPCases(t *testing.T, tests []pluginMCPCase) {
	t.Helper()

	for _, tt := range tests {
		Convey("When "+tt.name, func() {
			items, warnings, err := agent.PluginMCPServers([]byte(tt.doc), pluginMCPRoot)

			if tt.wantErr {
				Convey("Then it errors", func() {
					So(err, ShouldBeError)
				})

				return
			}

			So(err, ShouldBeNil)
			So(items, ShouldHaveLength, len(tt.want))

			for name, want := range tt.want {
				server, decodeErr := mcp.Decode(items[name])
				So(decodeErr, ShouldBeNil)
				So(server, ShouldResemble, want)
			}

			So(warnings, ShouldHaveLength, len(tt.warnings))

			for i, substr := range tt.warnings {
				So(warnings[i], ShouldContainSubstring, substr)
			}
		})
	}
}

func TestPluginMCPServersRenders(t *testing.T) {
	Convey("Given a table of plugin .mcp.json documents", t, func() {
		tests := []pluginMCPCase{
			{
				name: "stdio with plugin root substitution",
				doc: `{"mcpServers": {"plug": {
					"command": "${CLAUDE_PLUGIN_ROOT}/bin/plug",
					"args": ["--root", "${CLAUDE_PLUGIN_ROOT}"],
					"env": {"ROOT": "${CLAUDE_PLUGIN_ROOT}", "MODE": "fast"}
				}}}`,
				want: map[string]mcp.Server{"plug": {
					Transport: "stdio",
					Command:   []string{pluginMCPRoot + "/bin/plug", "--root", pluginMCPRoot},
					Env:       map[string]string{"ROOT": pluginMCPRoot, "MODE": "fast"},
				}},
			},
			{
				name: "remote server and headers",
				doc: `{"mcpServers": {
					"sse": {"type": "sse", "url": "https://example.com/sse", "headers": {"X-Key": "${CLAUDE_PLUGIN_ROOT}"}},
					"http": {"type": "streamable-http", "url": "https://example.com/mcp"}
				}}`,
				want: map[string]mcp.Server{
					"sse": {
						Transport: "sse",
						URL:       "https://example.com/sse",
						Headers:   map[string]string{"X-Key": pluginMCPRoot},
					},
					"http": {Transport: "http", URL: "https://example.com/mcp"},
				},
			},
			{
				name: "multiple placeholders in one string",
				doc:  `{"mcpServers": {"plug": {"command": "${CLAUDE_PLUGIN_ROOT}/a:${CLAUDE_PLUGIN_ROOT}/b"}}}`,
				want: map[string]mcp.Server{"plug": {
					Transport: "stdio",
					Command:   []string{pluginMCPRoot + "/a:" + pluginMCPRoot + "/b"},
				}},
			},
			{
				name: "empty document",
				doc:  `{"mcpServers": {}}`,
				want: map[string]mcp.Server{},
			},
			{
				name: "missing key",
				doc:  `{}`,
				want: map[string]mcp.Server{},
			},
		}

		assertPluginMCPCases(t, tests)
	})
}

func TestPluginMCPServersRefusals(t *testing.T) {
	Convey("Given a table of unsupported plugin .mcp.json documents", t, func() {
		tests := []pluginMCPCase{
			{
				name:     "unknown variable is refused",
				doc:      `{"mcpServers": {"bad": {"command": "x", "args": ["${MY_VAR}"]}}}`,
				want:     map[string]mcp.Server{},
				warnings: []string{`server "bad" references an unsupported variable "MY_VAR"`},
			},
			{
				name:     "default form is refused",
				doc:      `{"mcpServers": {"bad": {"command": "${CLAUDE_PLUGIN_ROOT:-/tmp}/bin/x"}}}`,
				want:     map[string]mcp.Server{},
				warnings: []string{`server "bad" references an unsupported variable "CLAUDE_PLUGIN_ROOT:-/tmp"`},
			},
			{
				name:     "unclosed placeholder is refused",
				doc:      `{"mcpServers": {"bad": {"command": "x${OOPS"}}}`,
				want:     map[string]mcp.Server{},
				warnings: []string{`server "bad" references an unsupported variable "OOPS"`},
			},
			{
				name:     "an unclosed secret-looking placeholder is bounded",
				doc:      `{"mcpServers": {"bad": {"command": "x${sk-live-supersecret"}}}`,
				want:     map[string]mcp.Server{},
				warnings: []string{`server "bad" references an unsupported variable "sk"`},
			},
			{
				name:     "an unclosed non-name placeholder stays generic",
				doc:      `{"mcpServers": {"bad": {"command": "x${-weird"}}}`,
				want:     map[string]mcp.Server{},
				warnings: []string{`server "bad" references an unsupported variable "unterminated ${"`},
			},
			{
				name:     "secret reference is refused",
				doc:      `{"mcpServers": {"bad": {"command": "x", "env": {"TOKEN": "{secret:FOO}"}}}}`,
				want:     map[string]mcp.Server{},
				warnings: []string{`server "bad" references an unsupported variable "secret:FOO"`},
			},
			{
				name: "a broken entry does not kill the rest",
				doc: `{"mcpServers": {
					"broken": "not-an-object",
					"good": {"command": "echo"}
				}}`,
				want:     map[string]mcp.Server{"good": {Transport: "stdio", Command: []string{"echo"}}},
				warnings: []string{`server "broken" is not an object`},
			},
			{
				name:     "unknown entry shape is refused",
				doc:      `{"mcpServers": {"weird": {"foo": 1}}}`,
				want:     map[string]mcp.Server{},
				warnings: []string{`server "weird" is not a supported MCP entry`},
			},
			{
				name:     "gemini httpUrl is not a Claude entry",
				doc:      `{"mcpServers": {"gem": {"httpUrl": "https://example.com/mcp"}}}`,
				want:     map[string]mcp.Server{},
				warnings: []string{`server "gem" is not a supported MCP entry`},
			},
			{
				name:    "broken json is an error",
				doc:     `{"mcpServers": {`,
				wantErr: true,
			},
		}

		assertPluginMCPCases(t, tests)
	})
}
