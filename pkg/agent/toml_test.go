package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

const codexConfigFixture = `# codex config, do not edit
model = "gpt-5"        # chosen model
approval_policy = "on-request"

[mcp_servers.foreign]
enabled = false        # foreign comment
tool_timeout_sec = 12

[mcp_servers.owned]
command = "old"
args = ["serve"]
env = { API_KEY = "literal" }
tool_timeout_sec = 7
enabled_tools = ["search"]

[bash]
timeout = 30           # unrelated section
`

func codexMCPFile(home string) string {
	return filepath.Join(home, ".codex", "config.toml")
}

func codexSurface(t *testing.T, home string) agent.Surface {
	t.Helper()

	return surfaceOf(t, agent.Codex(home, home), kind.MCP)
}

func stdioServer(command ...string) []byte {
	return mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: command, Env: map[string]string{"K": "V"}})
}

func TestTOMLConfigMissingAndEmpty(t *testing.T) {
	Convey("Given a table of missing and empty Codex configs", t, func() {
		for _, name := range []string{"missing", "empty"} {
			Convey("When the config is "+name, func() {
				home := t.TempDir()
				So(os.MkdirAll(filepath.Join(home, ".codex"), 0o750), ShouldBeNil)

				path := codexMCPFile(home)
				if name == "empty" {
					writeFile(t, path, "")
				}

				snap := snapshot(t, agent.Codex(home, home), kind.MCP)
				So(snap.Items, ShouldBeEmpty)

				So(codexSurface(t, home).Write(t.Context(), kind.Items{"srv": stdioServer("run")}), ShouldBeNil)

				back := snapshot(t, agent.Codex(home, home), kind.MCP)

				Convey("Then the file is created and the server round-trips", func() {
					So(readFile(t, path), ShouldEqual, "[mcp_servers.srv]\n  command = \"run\"\n  env = { K = \"V\" }\n\n")
					So(back.Items, ShouldHaveLength, 1)
					So(server(t, back.Items["srv"]).Command, ShouldResemble, []string{"run"})
				})
			})
		}
	})
}

func TestTOMLWritePreservesForeignContent(t *testing.T) {
	Convey("Given a Codex config with comments and foreign content", t, func() {
		home := t.TempDir()
		path := codexMCPFile(home)
		writeFile(t, path, codexConfigFixture)

		desired := kind.Items{
			"owned": stdioServer("new", "--flag"),
			"added": mcp.Encode(mcp.Server{Transport: mcp.TransportHTTP, URL: "https://example.com/mcp", Headers: map[string]string{"X": "1"}}),
		}

		Convey("When the owned server is rewritten", func() {
			So(codexSurface(t, home).Write(t.Context(), desired), ShouldBeNil)

			out := readFile(t, path)

			Convey("Then comments, order and agent-only fields survive", func() {
				So(out, ShouldContainSubstring, "# codex config, do not edit")
				So(out, ShouldContainSubstring, "# chosen model")
				So(out, ShouldContainSubstring, "# foreign comment")
				So(out, ShouldContainSubstring, "# unrelated section")
				So(out, ShouldContainSubstring, "approval_policy = \"on-request\"")
				So(strings.Index(out, "model = \"gpt-5\""), ShouldBeLessThan, strings.Index(out, "approval_policy"))

				So(out, ShouldContainSubstring, "enabled = false")
				So(out, ShouldContainSubstring, "tool_timeout_sec = 12")
				So(out, ShouldNotContainSubstring, `command = "old"`)
				So(out, ShouldContainSubstring, `command = "new"`)
				So(out, ShouldContainSubstring, `args = ["--flag"]`)
				So(out, ShouldContainSubstring, "env = { K = \"V\" }")
				So(out, ShouldContainSubstring, "tool_timeout_sec = 7")
				So(out, ShouldContainSubstring, `enabled_tools = ["search"]`)
				So(out, ShouldContainSubstring, `url = "https://example.com/mcp"`)
				So(out, ShouldContainSubstring, "http_headers = { X = \"1\" }")
			})
		})
	})
}

func TestTOMLWriteDeleteKeepsForeign(t *testing.T) {
	Convey("Given a Codex config", t, func() {
		home := t.TempDir()
		path := codexMCPFile(home)
		writeFile(t, path, codexConfigFixture)

		Convey("When the owned server is deleted", func() {
			So(codexSurface(t, home).Write(t.Context(), kind.Items{}), ShouldBeNil)

			out := readFile(t, path)

			Convey("Then only the owned server goes", func() {
				So(out, ShouldNotContainSubstring, "[mcp_servers.owned]")
				So(out, ShouldContainSubstring, "[mcp_servers.foreign]")
				So(out, ShouldContainSubstring, "# foreign comment")
				So(out, ShouldContainSubstring, "# codex config, do not edit")
				So(out, ShouldContainSubstring, "# unrelated section")
				So(out, ShouldContainSubstring, "timeout = 30")
			})
		})
	})
}

func TestTOMLWriteUnchangedKeepsBytes(t *testing.T) {
	Convey("Given a Codex config", t, func() {
		home := t.TempDir()
		path := codexMCPFile(home)
		writeFile(t, path, codexConfigFixture)

		snap := snapshot(t, agent.Codex(home, home), kind.MCP)
		before := readFile(t, path)

		Convey("When the unchanged owned server is written back", func() {
			So(codexSurface(t, home).Write(t.Context(), kind.Items{"owned": snap.Items["owned"]}), ShouldBeNil)

			Convey("Then the file is byte-identical", func() {
				So(readFile(t, path), ShouldEqual, before)
			})
		})
	})
}

func TestTOMLWritePreservesInlineServer(t *testing.T) {
	Convey("Given a Codex config with an inline server", t, func() {
		home := t.TempDir()
		path := codexMCPFile(home)
		writeFile(t, path, `model = "gpt-5"

[mcp_servers]
inline = { command = "keep-me" }

[other]
k = 1
`)

		desired := kind.Items{"inline": stdioServer("changed")}

		Convey("When it is rewritten", func() {
			So(codexSurface(t, home).Write(t.Context(), desired), ShouldBeNil)

			out := readFile(t, path)

			Convey("Then the inline server becomes a table and the rest survives", func() {
				So(out, ShouldContainSubstring, "[mcp_servers.inline]")
				So(out, ShouldContainSubstring, `command = "changed"`)
				So(out, ShouldContainSubstring, "k = 1")
				So(out, ShouldContainSubstring, `model = "gpt-5"`)

				back := snapshot(t, agent.Codex(home, home), kind.MCP)
				So(server(t, back.Items["inline"]).Command, ShouldResemble, []string{"changed"})
			})
		})
	})
}

//nolint:dupl // the delete/keep variants intentionally mirror each other
func TestTOMLWritePreservesSubtables(t *testing.T) {
	Convey("Given an owned server with a subtable and a foreign server", t, func() {
		home := t.TempDir()
		path := codexMCPFile(home)
		writeFile(t, path, `[mcp_servers.owned]
command = "old"

[mcp_servers.owned.env]
KEEP = "yes"

[mcp_servers.foreign]
other = "untouched"
`)

		Convey("When the owned server is rewritten", func() {
			So(codexSurface(t, home).Write(t.Context(), kind.Items{"owned": stdioServer("new")}), ShouldBeNil)

			out := readFile(t, path)

			Convey("Then its stale subtable is reclaimed and the foreign server survives", func() {
				So(out, ShouldNotContainSubstring, "KEEP = \"yes\"")
				So(out, ShouldContainSubstring, `command = "new"`)
				So(out, ShouldContainSubstring, "[mcp_servers.foreign]")
				So(out, ShouldContainSubstring, `other = "untouched"`)
			})
		})
	})
}

func TestTOMLWriteQuotedDottedName(t *testing.T) {
	Convey("Given quoted dotted and spaced server names", t, func() {
		home := t.TempDir()
		path := codexMCPFile(home)
		writeFile(t, path, `[mcp_servers."a.b"]
command = "old"

[mcp_servers."has space"]
command = "keep"
`)

		snap := snapshot(t, agent.Codex(home, home), kind.MCP)

		Convey("When one is rewritten", func() {
			So(snap.Items, ShouldContainKey, "a.b")
			So(snap.Items, ShouldContainKey, "has space")

			So(codexSurface(t, home).Write(t.Context(), kind.Items{
				"a.b":       stdioServer("new"),
				"has space": snap.Items["has space"],
			}), ShouldBeNil)

			out := readFile(t, path)

			Convey("Then the dotted name is one server and the other is untouched", func() {
				So(out, ShouldContainSubstring, `[mcp_servers."a.b"]`)
				So(out, ShouldContainSubstring, `command = "new"`)
				So(out, ShouldContainSubstring, `[mcp_servers."has space"]`)
				So(out, ShouldContainSubstring, `command = "keep"`)

				back := snapshot(t, agent.Codex(home, home), kind.MCP)
				So(server(t, back.Items["a.b"]).Command, ShouldResemble, []string{"new"})
			})
		})
	})
}

//nolint:dupl // mirrors the subtable-rewrite case above
func TestTOMLWriteInlineDoesNotTouchNestedKey(t *testing.T) {
	Convey("Given a nested key colliding with an inline server name", t, func() {
		home := t.TempDir()
		path := codexMCPFile(home)
		writeFile(t, path, `[mcp_servers.b]
a = "nested"

[mcp_servers]
a = { command = "top" }
`)

		Convey("When the inline server is rewritten", func() {
			So(codexSurface(t, home).Write(t.Context(), kind.Items{"a": stdioServer("changed")}), ShouldBeNil)

			out := readFile(t, path)

			Convey("Then the nested key of another table is untouched", func() {
				So(out, ShouldContainSubstring, "[mcp_servers.b]")
				So(out, ShouldContainSubstring, `a = "nested"`)
				So(out, ShouldContainSubstring, "[mcp_servers.a]")
				So(out, ShouldContainSubstring, `command = "changed"`)
			})
		})
	})
}

func TestCodexMCPCreatable(t *testing.T) {
	Convey("Given the Codex MCP surface", t, func() {
		Convey("When its traits are read", func() {
			Convey("Then it is creatable", func() {
				So(surfaceOf(t, agent.Codex(t.TempDir(), t.TempDir()), kind.MCP).Traits().Creatable, ShouldBeTrue)
			})
		})
	})
}

func TestTOMLWriteKeepsLaterForeignTables(t *testing.T) {
	Convey("Given tables after the owned server", t, func() {
		const fixture = `[mcp_servers.owned]
command = "old"

[[hooks]]
event = "x"          # hook comment

["a.b"]
x = 1

[other]
k = 2
`

		for i, desired := range []kind.Items{
			{"owned": stdioServer("new")},
			{},
		} {
			Convey("When desired case #"+string(rune('0'+i)), func() {
				home := t.TempDir()
				path := codexMCPFile(home)
				writeFile(t, path, fixture)

				So(codexSurface(t, home).Write(t.Context(), desired), ShouldBeNil)

				out := readFile(t, path)

				Convey("Then later tables survive", func() {
					So(out, ShouldContainSubstring, "[[hooks]]")
					So(out, ShouldContainSubstring, `event = "x"`)
					So(out, ShouldContainSubstring, "# hook comment")
					So(out, ShouldContainSubstring, `["a.b"]`)
					So(out, ShouldContainSubstring, "x = 1")
					So(out, ShouldContainSubstring, "[other]")
					So(out, ShouldContainSubstring, "k = 2")

					if _, keep := desired["owned"]; keep {
						So(out, ShouldContainSubstring, `command = "new"`)
					} else {
						So(out, ShouldNotContainSubstring, "[mcp_servers.owned]")
					}
				})
			})
		}
	})
}

func TestTOMLWriteInterleavedForeignTable(t *testing.T) {
	Convey("Given a foreign table between owned and foreign mcp servers", t, func() {
		home := t.TempDir()
		path := codexMCPFile(home)
		writeFile(t, path, `[mcp_servers.owned]
command = "old"

[bash]
x = 1          # keep this

[mcp_servers.foreign]
other = "untouched"
`)

		Convey("When the owned server is rewritten", func() {
			So(codexSurface(t, home).Write(t.Context(), kind.Items{"owned": stdioServer("new")}), ShouldBeNil)

			out := readFile(t, path)

			Convey("Then the interleaved table survives", func() {
				So(out, ShouldContainSubstring, `command = "new"`)
				So(out, ShouldContainSubstring, "[bash]")
				So(out, ShouldContainSubstring, "# keep this")
				So(out, ShouldContainSubstring, "[mcp_servers.foreign]")
				So(out, ShouldContainSubstring, `other = "untouched"`)
			})
		})
	})
}

func TestTOMLWriteTwiceAcrossForeignTable(t *testing.T) {
	Convey("Given a config written twice across a foreign table", t, func() {
		home := t.TempDir()
		path := codexMCPFile(home)
		writeFile(t, path, `[mcp_servers.owned]
command = "v1"

[bash]
x = 1
`)

		first := kind.Items{"owned": stdioServer("v2"), "added": stdioServer("added")}
		So(codexSurface(t, home).Write(t.Context(), first), ShouldBeNil)

		second := kind.Items{"owned": stdioServer("v3"), "added": first["added"]}

		Convey("When the second write runs", func() {
			So(codexSurface(t, home).Write(t.Context(), second), ShouldBeNil)

			out := readFile(t, path)

			Convey("Then both servers update and the foreign table survives", func() {
				So(out, ShouldContainSubstring, `command = "v3"`)
				So(out, ShouldContainSubstring, `command = "added"`)
				So(out, ShouldContainSubstring, "[bash]")
				So(out, ShouldContainSubstring, "x = 1")

				back := snapshot(t, agent.Codex(home, home), kind.MCP)
				So(second.Equal(back.Items), ShouldBeTrue)
			})
		})
	})
}

func TestTOMLWriteReclaimsStrayOwnedSubtable(t *testing.T) {
	Convey("Given an owned subtable separated from its table", t, func() {
		const fixture = `[mcp_servers.owned]
command = "old"

[other]
k = 1

[mcp_servers.owned.env]
KEEP = "yes"
`

		for i, desired := range []kind.Items{
			{"owned": stdioServer("new")},
			{},
		} {
			Convey("When desired case #"+string(rune('0'+i)), func() {
				home := t.TempDir()
				path := codexMCPFile(home)
				writeFile(t, path, fixture)

				So(codexSurface(t, home).Write(t.Context(), desired), ShouldBeNil)

				out := readFile(t, path)

				Convey("Then the stray subtable is reclaimed and the file parses back", func() {
					So(out, ShouldNotContainSubstring, "KEEP = \"yes\"")
					So(out, ShouldContainSubstring, "[other]")
					So(out, ShouldContainSubstring, "k = 1")

					back := snapshot(t, agent.Codex(home, home), kind.MCP)
					So(desired.Equal(back.Items), ShouldBeTrue)
				})
			})
		}
	})
}

func TestTOMLWriteRejectsParentAfterSubtable(t *testing.T) {
	Convey("Given a subtable declared before its table", t, func() {
		home := t.TempDir()
		path := codexMCPFile(home)

		const fixture = `[mcp_servers.owned.env]
KEEP = "yes"

[other]
k = 1

[mcp_servers.owned]
command = "old"
`

		writeFile(t, path, fixture)

		Convey("When the owned server is rewritten", func() {
			err := codexSurface(t, home).Write(t.Context(), kind.Items{"owned": stdioServer("new")})

			Convey("Then it is rejected and the file is untouched", func() {
				So(err, ShouldBeError)
				So(readFile(t, path), ShouldEqual, fixture)
			})
		})
	})
}

func TestTOMLWriteIgnoresForeignOrphanSubtable(t *testing.T) {
	Convey("Given a foreign orphan subtable", t, func() {
		home := t.TempDir()
		path := codexMCPFile(home)
		writeFile(t, path, `[mcp_servers.owned]
command = "old"

[mcp_servers.orphan.env]
KEEP = "yes"
`)

		Convey("When the owned server is rewritten", func() {
			So(codexSurface(t, home).Write(t.Context(), kind.Items{"owned": stdioServer("new")}), ShouldBeNil)

			out := readFile(t, path)

			Convey("Then the orphan is left alone", func() {
				So(out, ShouldContainSubstring, `command = "new"`)
				So(out, ShouldContainSubstring, "[mcp_servers.orphan.env]")
				So(out, ShouldContainSubstring, `KEEP = "yes"`)
			})
		})
	})
}

func TestTOMLWriteKeepsTrailingCommentsAfterOwned(t *testing.T) {
	Convey("Given a trailing comment and blank line before the next table", t, func() {
		const fixture = `# top comment

[mcp_servers.alpha]
command = "old"

# keep me: notes about the UI

[tui]
theme = "dark"
`

		for i, desired := range []kind.Items{
			{"alpha": stdioServer("new")},
			{},
		} {
			Convey("When desired case #"+string(rune('0'+i)), func() {
				home := t.TempDir()
				path := codexMCPFile(home)
				writeFile(t, path, fixture)

				So(codexSurface(t, home).Write(t.Context(), desired), ShouldBeNil)

				out := readFile(t, path)

				Convey("Then the trailing comment survives", func() {
					So(out, ShouldContainSubstring, "# top comment")
					So(out, ShouldContainSubstring, "# keep me: notes about the UI")
					So(out, ShouldContainSubstring, "[tui]")
					So(out, ShouldContainSubstring, `theme = "dark"`)

					if _, keep := desired["alpha"]; keep {
						So(out, ShouldContainSubstring, `command = "new"`)
					} else {
						So(out, ShouldNotContainSubstring, "[mcp_servers.alpha]")
					}

					back := snapshot(t, agent.Codex(home, home), kind.MCP)
					So(desired.Equal(back.Items), ShouldBeTrue)
				})
			})
		}
	})
}

func TestTOMLWriteKeepsTrailingCommentsBeforeNextServer(t *testing.T) {
	Convey("Given a comment before the next server", t, func() {
		home := t.TempDir()
		path := codexMCPFile(home)
		writeFile(t, path, `[mcp_servers.alpha]
command = "old"

# about beta

[mcp_servers.beta]
command = "b"
`)

		snap := snapshot(t, agent.Codex(home, home), kind.MCP)

		Convey("When alpha is rewritten", func() {
			So(codexSurface(t, home).Write(t.Context(), kind.Items{
				"alpha": stdioServer("new"),
				"beta":  snap.Items["beta"],
			}), ShouldBeNil)

			out := readFile(t, path)

			Convey("Then the comment between servers survives", func() {
				So(out, ShouldContainSubstring, `command = "new"`)
				So(out, ShouldContainSubstring, "# about beta")
				So(out, ShouldContainSubstring, "[mcp_servers.beta]")
				So(out, ShouldContainSubstring, `command = "b"`)
			})
		})
	})
}
