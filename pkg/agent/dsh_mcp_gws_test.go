package agent_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

// dshMCPHome prepares a DSH adapter over a temp home. A non-empty patch seeds
// `$DSH_HOME/cordis.patch.yml` before the adapter resolves the home; an empty
// one leaves the file absent (the DSH home itself exists).
func dshMCPHome(t *testing.T, patch string) (*agent.Agent, string) {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "dsh")

	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir DSH_HOME: %v", err)
	}

	path := filepath.Join(dir, "cordis.patch.yml")
	if patch != "" {
		writeFile(t, path, patch)
	}

	t.Setenv("DSH_HOME", dir)

	return agent.DSH(t.TempDir(), t.TempDir()), path
}

func dshMCPWrite(t *testing.T, a *agent.Agent, items kind.Items) {
	t.Helper()

	if err := surfaceOf(t, a, kind.MCP).Write(t.Context(), items); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func dshMCPRead(t *testing.T, a *agent.Agent) agent.Snapshot {
	t.Helper()

	snap, err := surfaceOf(t, a, kind.MCP).Read(t.Context())
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	return snap
}

// dshUserPatch is a user-authored patch: comments, two records beadle does not
// own (one with a `!!js` expression), flow styles and an id-targeted override.
const dshUserPatch = `# My own DSH patch layer.
# Second header line.

- id: my-row
  name: some-plugin
  config:
    alpha: 1
    beta: two   # inline note
- insert:
    - id: user-mcp
      name: '@deepseek-ai/dsh-mcp-client'
      config:
        transport: stdio
        serverName: usermcp
        command: npx
        args: ['-y', pkg]      # keep my args
        env: {TOKEN: 'abc'}
    - id: user-file
      name: '@deepseek-ai/dsh-mcp-client'
      config:
        transport: stdio
        serverName: userfile
        command: node
        args: ['${DSH_HOME}/mcp/server.js']
        cwd: !!js dshHomePath('mcp')

# trailing note
`

func TestDSHMCPCreateAndIdempotent(t *testing.T) {
	Convey("Given a DSH home without a patch file", t, func() {
		a, path := dshMCPHome(t, "")

		desired := kind.Items{
			"alpha": mcp.Encode(mcp.Server{
				Transport: mcp.TransportStdio,
				Command:   []string{"npx", "-y", "alpha-mcp"},
				Env:       map[string]string{"TOKEN": "${TOKEN}"},
			}),
			"beta": mcp.Encode(mcp.Server{
				Transport: mcp.TransportHTTP,
				URL:       "https://example.com/mcp",
				Headers:   map[string]string{"X-Probe": "1"},
			}),
		}

		Convey("When the canon servers are written", func() {
			dshMCPWrite(t, a, desired)
			first := readFile(t, path)

			Convey("Then the created file explains itself and carries both records", func() {
				So(first, ShouldContainSubstring, `beadle manages only the entries whose id starts with "beadle:"`)
				So(first, ShouldContainSubstring, "- insert:")
				So(first, ShouldContainSubstring, "id: beadle:alpha")
				So(first, ShouldContainSubstring, "name: '@deepseek-ai/dsh-mcp-client'")
				So(first, ShouldContainSubstring, "transport: stdio")
				So(first, ShouldContainSubstring, "transport: streamable-http")
				So(first, ShouldContainSubstring, "TOKEN: ${TOKEN}")

				Convey("And the surface reads the canonical servers back", func() {
					snap := dshMCPRead(t, a)
					So(snap.Present, ShouldBeTrue)
					So(snap.Items.Equal(desired), ShouldBeTrue)

					Convey("And a repeat write leaves the file byte-for-byte", func() {
						dshMCPWrite(t, a, desired)
						So(readFile(t, path), ShouldEqual, first)

						Convey("And the record order is stable across a change", func() {
							updated := kind.Items{
								"alpha": desired["alpha"],
								"beta":  desired["beta"],
								"gamma": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"gamma"}}),
							}

							dshMCPWrite(t, a, updated)
							second := readFile(t, path)

							So(second, ShouldContainSubstring, "id: beadle:gamma")
							So(strings.Index(second, "beadle:alpha"), ShouldBeLessThan, strings.Index(second, "beadle:gamma"))

							Convey("And the empty list removes every beadle record", func() {
								dshMCPWrite(t, a, kind.Items{})

								// The last removal prunes the entry and the empty
								// document returns to the shipped `[]` shape.
								So(readFile(t, path), ShouldEqual, "[]\n")
							})
						})
					})
				})
			})
		})
	})
}

func TestDSHMCPPreservesUserPatch(t *testing.T) {
	Convey("Given a user-authored patch with foreign records and comments", t, func() {
		a, path := dshMCPHome(t, dshUserPatch)

		Convey("When beadle adds its server", func() {
			dshMCPWrite(t, a, kind.Items{"alpha": mcp.Encode(mcp.Server{
				Transport: mcp.TransportStdio,
				Command:   []string{"node", "alpha.js"},
			})})

			Convey("Then the user's entries and comments are intact", func() {
				content := readFile(t, path)

				So(content, ShouldContainSubstring, "# My own DSH patch layer.")
				So(content, ShouldContainSubstring, "# inline note")
				So(content, ShouldContainSubstring, "# keep my args")
				So(content, ShouldContainSubstring, "# trailing note")
				So(content, ShouldContainSubstring, "cwd: !!js dshHomePath('mcp')")
				So(content, ShouldContainSubstring, "id: user-mcp")
				So(content, ShouldContainSubstring, "serverName: usermcp")
				So(content, ShouldContainSubstring, "env: {TOKEN: 'abc'}")
				So(content, ShouldContainSubstring, "id: my-row")
				So(content, ShouldContainSubstring, "beta: two")

				So(content, ShouldContainSubstring, "id: beadle:alpha")

				Convey("And reading reports only beadle's server", func() {
					snap := dshMCPRead(t, a)
					So(snap.Items.Keys(), ShouldResemble, []string{"alpha"})

					Convey("When the canon changes, the record is updated in place", func() {
						dshMCPWrite(t, a, kind.Items{"alpha": mcp.Encode(mcp.Server{
							Transport: mcp.TransportHTTP,
							URL:       "https://example.com/alpha",
						})})

						Convey("Then the new config replaced the old one and the user file stands", func() {
							updated := readFile(t, path)

							So(updated, ShouldContainSubstring, "url: https://example.com/alpha")
							So(updated, ShouldContainSubstring, "transport: streamable-http")
							So(updated, ShouldNotContainSubstring, "alpha.js")
							So(updated, ShouldContainSubstring, "# trailing note")
							So(updated, ShouldContainSubstring, "id: user-mcp")
							So(updated, ShouldContainSubstring, "cwd: !!js dshHomePath('mcp')")
							So(strings.Count(updated, "id: beadle:alpha"), ShouldEqual, 1)

							Convey("When the canon drops the server, only the record leaves", func() {
								dshMCPWrite(t, a, kind.Items{})

								pruned := readFile(t, path)
								So(pruned, ShouldNotContainSubstring, "beadle:")
								So(pruned, ShouldContainSubstring, "# My own DSH patch layer.")
								So(pruned, ShouldContainSubstring, "id: user-mcp")
								So(pruned, ShouldContainSubstring, "id: my-row")
								So(pruned, ShouldContainSubstring, "cwd: !!js dshHomePath('mcp')")
							})
						})
					})
				})
			})
		})
	})
}

func TestDSHMCPSkipsUnsupportedServers(t *testing.T) {
	Convey("Given a canon with servers DSH cannot run", t, func() {
		a, path := dshMCPHome(t, "")

		items := kind.Items{
			"ok":       mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"node"}}),
			"sse":      mcp.Encode(mcp.Server{Transport: mcp.TransportSSE, URL: "https://x.example/sse"}),
			"dot.name": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"node"}}),
		}

		Convey("When the servers are written", func() {
			dshMCPWrite(t, a, items)

			Convey("Then only the runnable server lands", func() {
				content := readFile(t, path)
				So(content, ShouldContainSubstring, "id: beadle:ok")
				So(content, ShouldNotContainSubstring, "beadle:sse")
				So(content, ShouldNotContainSubstring, "dot.name")

				snap := dshMCPRead(t, a)
				So(snap.Items.Keys(), ShouldResemble, []string{"ok"})
			})
		})

		Convey("And only unsupported servers leave no file behind", func() {
			dshMCPWrite(t, a, kind.Items{
				"sse": mcp.Encode(mcp.Server{Transport: mcp.TransportSSE, URL: "https://x.example/sse"}),
			})

			_, err := os.Stat(path)
			So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)
		})
	})
}

func TestDSHMCPServerReason(t *testing.T) {
	Convey("Given canon servers in every DSH shape", t, func() {
		cases := []struct {
			name string
			data string
			want string
		}{
			{"ok", `{"transport":"stdio","command":["node"]}`, ""},
			{"ok", `{"command":["node"]}`, ""},
			{"ok", `{"transport":"http","url":"https://x.example/mcp"}`, ""},
			{"ok", `{"url":"https://x.example/mcp"}`, ""},
			{"dot.name", `{"command":["node"]}`, "serverName"},
			{"", `{"command":["node"]}`, "serverName"},
			{strings.Repeat("a", 33), `{"command":["node"]}`, "serverName"},
			{"ok", `{"transport":"stdio"}`, "needs a command"},
			{"ok", `{"transport":"http"}`, "needs a url"},
			{"ok", `{"command":["node"],"url":"https://x.example/mcp"}`, "both a command and a url"},
			{"ok", `{"transport":"sse","url":"https://x.example/sse"}`, "not supported"},
			{"ok", `{"transport":"ws","url":"wss://x.example/mcp"}`, "not supported"},
			{"ok", `{"command":["node"]}extra`, "decode mcp server"},
		}

		for _, tc := range cases {
			reason := agent.DSHMCPServerReason(tc.name, []byte(tc.data))

			if tc.want == "" {
				So(reason, ShouldBeEmpty)

				continue
			}

			So(reason, ShouldContainSubstring, tc.want)
		}
	})
}

func TestDSHMCPReadBlockedRecords(t *testing.T) {
	const patch = `- insert:
    - id: beadle:mismatch
      name: other-plugin
      config:
        transport: stdio
        serverName: mismatch
        command: node
    - id: beadle:broken
      name: '@deepseek-ai/dsh-mcp-client'
      config:
        transport: stdio
        serverName: broken
    - id: beadle:off
      name: '@deepseek-ai/dsh-mcp-client'
      disabled: true
      config:
        transport: stdio
        serverName: off
        command: node
    - id: beadle:nameless
      name: '@deepseek-ai/dsh-mcp-client'
      config:
        transport: stdio
        command: node
- id: beadle:root
  name: '@deepseek-ai/dsh-mcp-client'
  config:
    transport: stdio
    serverName: root
    command: node
`

	Convey("Given a patch full of unusable beadle records", t, func() {
		a, _ := dshMCPHome(t, patch)

		Convey("When the surface reads", func() {
			snap := dshMCPRead(t, a)

			Convey("Then each blocked record is reported, not read", func() {
				So(snap.Items, ShouldBeEmpty)

				So(snap.Unreadable["mismatch"], ShouldContainSubstring, `carries name "other-plugin"`)
				So(snap.Unreadable["broken"], ShouldContainSubstring, "config.command")
				So(snap.Unreadable["off"], ShouldContainSubstring, "disabled")
				So(snap.Unreadable["root"], ShouldContainSubstring, "outside an insert list")

				So(snap.Warnings, ShouldHaveLength, 1)
				So(snap.Warnings[0], ShouldContainSubstring, "beadle:nameless")
			})
		})

		Convey("When the canon offers the blocked servers", func() {
			before := readFile(t, a.Surface(kind.MCP).Path())

			items := kind.Items{
				"mismatch": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"node"}}),
				"broken":   mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"node"}}),
				"off":      mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"node"}}),
				"root":     mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"node"}}),
				"nameless": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"node"}}),
			}

			dshMCPWrite(t, a, items)

			Convey("Then the blocked records stay untouched and no duplicate id appears", func() {
				after := readFile(t, a.Surface(kind.MCP).Path())
				So(after, ShouldEqual, before)
				So(strings.Count(after, "id: beadle:mismatch"), ShouldEqual, 1)
			})
		})

		Convey("And the blockers name every stuck server", func() {
			blockers, err := agent.DSHMCPBlockers(t.TempDir())
			So(err, ShouldBeNil)

			So(blockers["mismatch"], ShouldContainSubstring, "other-plugin")
			So(blockers["broken"], ShouldContainSubstring, "config.command")
			So(blockers["off"], ShouldContainSubstring, "disabled")
			So(blockers["root"], ShouldContainSubstring, "outside an insert list")
			So(blockers["nameless"], ShouldContainSubstring, "beadle:nameless")
		})
	})
}

func TestDSHMCPRenamedRecordFollowsServerName(t *testing.T) {
	const patch = `- insert:
    - id: beadle:old
      name: '@deepseek-ai/dsh-mcp-client'
      config:
        transport: stdio
        serverName: alpha
        command: node
`

	Convey("Given a record whose id suffix differs from its serverName", t, func() {
		a, path := dshMCPHome(t, patch)

		Convey("When the surface reads", func() {
			snap := dshMCPRead(t, a)

			Convey("Then the record belongs to its serverName, like DSH mounts it", func() {
				So(snap.Items.Keys(), ShouldResemble, []string{"alpha"})
				So(snap.Unreadable, ShouldBeEmpty)

				Convey("And an update follows the serverName without adding an id", func() {
					dshMCPWrite(t, a, kind.Items{"alpha": mcp.Encode(mcp.Server{
						Transport: mcp.TransportStdio,
						Command:   []string{"node", "new.js"},
					})})

					updated := readFile(t, path)
					So(updated, ShouldContainSubstring, "new.js")
					So(updated, ShouldContainSubstring, "id: beadle:old")
					So(updated, ShouldNotContainSubstring, "id: beadle:alpha")
					So(strings.Count(updated, "serverName: alpha"), ShouldEqual, 1)

					Convey("And dropping the server removes the record", func() {
						dshMCPWrite(t, a, kind.Items{})
						So(readFile(t, path), ShouldNotContainSubstring, "beadle:")
					})
				})
			})
		})
	})
}

func TestDSHMCPForeignServerNameBlocksInsert(t *testing.T) {
	const patch = `- insert:
    - id: user-mcp
      name: '@deepseek-ai/dsh-mcp-client'
      config:
        transport: stdio
        serverName: alpha
        command: user-mcp
`

	Convey("Given a foreign record already mounting a serverName", t, func() {
		a, path := dshMCPHome(t, patch)

		Convey("When the canon wants the same name", func() {
			before := readFile(t, path)

			dshMCPWrite(t, a, kind.Items{
				"alpha": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"node"}}),
				"beta":  mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"node"}}),
			})

			Convey("Then only the free name lands and the foreign record stays", func() {
				after := readFile(t, path)
				So(after, ShouldContainSubstring, "user-mcp")
				So(after, ShouldContainSubstring, "id: beadle:beta")
				So(after, ShouldNotContainSubstring, "id: beadle:alpha")
				So(strings.Count(after, "serverName: alpha"), ShouldEqual, 1)

				blockers, err := agent.DSHMCPBlockers(t.TempDir())
				So(err, ShouldBeNil)
				So(blockers["alpha"], ShouldContainSubstring, "does not own")

				So(before, ShouldNotContainSubstring, "beadle:beta")
			})
		})
	})
}

func TestDSHMCPKeepsUnmanagedConfigKeys(t *testing.T) {
	const patch = `- insert:
    - id: beadle:alpha
      name: '@deepseek-ai/dsh-mcp-client'
      config:
        transport: stdio
        serverName: alpha
        command: node
        toolCallTimeoutMs: 5000
`

	Convey("Given a beadle record carrying a plugin tuning key", t, func() {
		a, path := dshMCPHome(t, patch)

		Convey("When the canon matches the record", func() {
			dshMCPWrite(t, a, kind.Items{"alpha": mcp.Encode(mcp.Server{
				Transport: mcp.TransportStdio,
				Command:   []string{"node"},
			})})

			Convey("Then the file is not rewritten and the tuning key stays", func() {
				So(readFile(t, path), ShouldEqual, patch)

				Convey("When the canon changes, the config is replaced wholesale", func() {
					dshMCPWrite(t, a, kind.Items{"alpha": mcp.Encode(mcp.Server{
						Transport: mcp.TransportStdio,
						Command:   []string{"node", "--new"},
					})})

					updated := readFile(t, path)
					So(updated, ShouldContainSubstring, "--new")
					So(updated, ShouldNotContainSubstring, "toolCallTimeoutMs")
				})
			})
		})
	})
}

func TestDSHMCPRefusesBrokenFiles(t *testing.T) {
	cases := []struct {
		label   string
		content string
	}{
		{"an unparsable document", "- insert: [\n"},
		{"a non-array document", "{}\n"},
		{"a null document", "null\n"},
		{"an empty file", "\n"},
	}

	Convey("Given a patch file DSH would fail loud on", t, func() {
		for _, tc := range cases {
			a, path := dshMCPHome(t, tc.content)

			surface := surfaceOf(t, a, kind.MCP)

			_, readErr := surface.Read(t.Context())
			So(readErr, ShouldBeError)

			writeErr := surface.Write(t.Context(), kind.Items{
				"alpha": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"node"}}),
			})
			So(writeErr, ShouldBeError)

			So(readFile(t, path), ShouldEqual, tc.content)
		}
	})
}

func TestDSHMCPTurnsTemplateIntoBlockList(t *testing.T) {
	const template = `# Your patch layer for this dsh profile, applied after every bundle layer:
# a top-level YAML array of loader patch entries (id-targeted config
# overrides, disables, and insert lists; ` + "`!!js`" + ` expressions allowed).
[]
`

	Convey("Given the shipped empty patch template", t, func() {
		a, path := dshMCPHome(t, template)

		Convey("When beadle adds a server", func() {
			dshMCPWrite(t, a, kind.Items{"alpha": mcp.Encode(mcp.Server{
				Transport: mcp.TransportStdio,
				Command:   []string{"node"},
			})})

			Convey("Then the comments survive and the array turns into a block list", func() {
				content := readFile(t, path)
				So(content, ShouldContainSubstring, "# Your patch layer for this dsh profile")
				So(content, ShouldContainSubstring, "expressions allowed).")
				So(content, ShouldContainSubstring, "- insert:")
				So(content, ShouldContainSubstring, "id: beadle:alpha")
				So(content, ShouldNotContainSubstring, "\n[]\n")
			})
		})
	})
}

func TestDSHMCPRemovesNestedRecord(t *testing.T) {
	const patch = `- insert:
    - id: group-a
      group: true
      config:
        - id: beadle:alpha
          name: '@deepseek-ai/dsh-mcp-client'
          config:
            transport: stdio
            serverName: alpha
            command: node
        - id: group-child
          name: some-plugin
`

	Convey("Given a beadle record inside an inserted group", t, func() {
		a, path := dshMCPHome(t, patch)

		Convey("When the surface reads", func() {
			snap := dshMCPRead(t, a)

			Convey("Then the nested record is beadle's", func() {
				So(snap.Items.Keys(), ShouldResemble, []string{"alpha"})

				Convey("And dropping the server keeps the group and its children", func() {
					dshMCPWrite(t, a, kind.Items{})

					content := readFile(t, path)
					So(content, ShouldNotContainSubstring, "beadle:")
					So(content, ShouldContainSubstring, "id: group-a")
					So(content, ShouldContainSubstring, "id: group-child")
				})
			})
		})
	})
}

func TestDSHMCPUpdatesDuplicateRecords(t *testing.T) {
	const patch = `- insert:
    - id: beadle:alpha
      name: '@deepseek-ai/dsh-mcp-client'
      config:
        transport: stdio
        serverName: alpha
        command: old
- insert:
    - id: beadle:alpha
      name: '@deepseek-ai/dsh-mcp-client'
      config:
        transport: stdio
        serverName: alpha
        command: stale
`

	Convey("Given two beadle records for one server", t, func() {
		a, path := dshMCPHome(t, patch)

		Convey("When the surface reads", func() {
			snap := dshMCPRead(t, a)

			Convey("Then the last record wins, like DSH applies them", func() {
				server, err := mcp.Decode(snap.Items["alpha"])
				So(err, ShouldBeNil)
				So(server.Command, ShouldResemble, []string{"stale"})

				Convey("And an update rewrites both copies", func() {
					dshMCPWrite(t, a, kind.Items{"alpha": mcp.Encode(mcp.Server{
						Transport: mcp.TransportStdio,
						Command:   []string{"fresh"},
					})})

					content := readFile(t, path)
					So(content, ShouldNotContainSubstring, "old")
					So(content, ShouldNotContainSubstring, "stale")
					So(strings.Count(content, "command: fresh"), ShouldEqual, 2)
				})
			})
		})
	})
}

func TestDSHMCPKeepsRootFlowStyle(t *testing.T) {
	Convey("Given a flow-style patch list", t, func() {
		a, path := dshMCPHome(t, "[{id: user, name: some-plugin}]\n")

		Convey("When beadle adds a server", func() {
			dshMCPWrite(t, a, kind.Items{"alpha": mcp.Encode(mcp.Server{
				Transport: mcp.TransportStdio,
				Command:   []string{"node"},
			})})

			Convey("Then the entry is appended in the file's own style", func() {
				content := readFile(t, path)
				So(content, ShouldContainSubstring, "{id: user, name: some-plugin}")
				So(content, ShouldContainSubstring, "beadle:alpha")

				Convey("And the surface reads it back", func() {
					So(dshMCPRead(t, a).Items.Keys(), ShouldResemble, []string{"alpha"})
				})
			})
		})
	})
}

func TestDSHMCPReusesFreedID(t *testing.T) {
	const patch = `- insert:
    - id: beadle:alpha
      name: '@deepseek-ai/dsh-mcp-client'
      config:
        transport: stdio
        serverName: beta
        command: node
`

	Convey("Given a record mounting beta under the id beadle:alpha", t, func() {
		a, path := dshMCPHome(t, patch)

		Convey("When the canon wants alpha instead", func() {
			dshMCPWrite(t, a, kind.Items{"alpha": mcp.Encode(mcp.Server{
				Transport: mcp.TransportStdio,
				Command:   []string{"node"},
			})})

			Convey("Then the stale record is replaced by a fresh beadle:alpha", func() {
				content := readFile(t, path)
				So(content, ShouldContainSubstring, "serverName: alpha")
				So(content, ShouldNotContainSubstring, "serverName: beta")
				So(strings.Count(content, "id: beadle:alpha"), ShouldEqual, 1)

				So(dshMCPRead(t, a).Items.Keys(), ShouldResemble, []string{"alpha"})
			})
		})
	})
}

func TestDSHMCPKeepsFileIndent(t *testing.T) {
	const patch = `- id: user-row
  name: some-plugin
  config:
    alpha: 1
    nested:
        deep: 1
`

	Convey("Given a patch file formatted with four-space nesting", t, func() {
		a, path := dshMCPHome(t, patch)

		Convey("When beadle adds a server", func() {
			dshMCPWrite(t, a, kind.Items{"alpha": mcp.Encode(mcp.Server{
				Transport: mcp.TransportStdio,
				Command:   []string{"node"},
			})})

			Convey("Then the user's layout is kept and the file is byte-stable", func() {
				content := readFile(t, path)
				So(content, ShouldContainSubstring, "    nested:\n        deep: 1\n")
				So(content, ShouldContainSubstring, "beadle:alpha")

				Convey("And a no-op write leaves it untouched", func() {
					dshMCPWrite(t, a, kind.Items{"alpha": mcp.Encode(mcp.Server{
						Transport: mcp.TransportStdio,
						Command:   []string{"node"},
					})})

					So(readFile(t, path), ShouldEqual, content)
				})
			})
		})
	})
}

func TestDSHMCPBlockersForHeldID(t *testing.T) {
	const patch = `- insert:
    - id: beadle:alpha
      name: '@deepseek-ai/dsh-mcp-client'
      config:
        transport: stdio
        serverName: beta
        command: node
`

	Convey("Given a working record whose id names another server", t, func() {
		dshMCPHome(t, patch)

		Convey("When the blockers are collected", func() {
			blockers, err := agent.DSHMCPBlockers(t.TempDir())
			So(err, ShouldBeNil)

			Convey("Then the id's own name is reported as held", func() {
				So(blockers["alpha"], ShouldContainSubstring, `id "beadle:alpha" is held by the record mounting "beta"`)
				So(blockers["beta"], ShouldBeEmpty)
			})
		})
	})
}

func TestDSHMCPIgnoresMappingValueRecords(t *testing.T) {
	const patch = `- insert:
    - id: user-row
      name: some-plugin
      config:
        nested:
          id: beadle:hidden
          name: '@deepseek-ai/dsh-mcp-client'
          config:
            transport: stdio
            serverName: hidden
            command: node
`

	Convey("Given a beadle id nested as a mapping value, not as a row", t, func() {
		a, path := dshMCPHome(t, patch)

		Convey("When the surface reads", func() {
			snap := dshMCPRead(t, a)

			Convey("Then it is not a mounted row and cannot be dropped by mistake", func() {
				So(snap.Items, ShouldBeEmpty)

				dshMCPWrite(t, a, kind.Items{})
				So(readFile(t, path), ShouldEqual, patch)

				Convey("And the canon still delivers the name with a real row", func() {
					dshMCPWrite(t, a, kind.Items{"hidden": mcp.Encode(mcp.Server{
						Transport: mcp.TransportStdio,
						Command:   []string{"node"},
					})})

					So(dshMCPRead(t, a).Items.Keys(), ShouldResemble, []string{"hidden"})
					So(readFile(t, path), ShouldContainSubstring, "id: user-row")
				})
			})
		})
	})
}

func TestDSHMCPTraits(t *testing.T) {
	Convey("Given the DSH adapter", t, func() {
		home := t.TempDir()
		t.Setenv("DSH_HOME", filepath.Join(home, "dsh"))

		a := agent.DSH(home, t.TempDir())
		surface := surfaceOf(t, a, kind.MCP)

		Convey("Then its MCP surface targets the home patch layer", func() {
			So(surface.Path(), ShouldEqual, agent.DSHPatchPath(home))
			So(surface.Kind(), ShouldEqual, kind.MCP)
			So(surface.Traits().Creatable, ShouldBeTrue)
			So(surface.WatchPaths(), ShouldResemble, []string{agent.DSHPatchPath(home)})
		})
	})
}
