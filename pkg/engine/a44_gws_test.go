package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
	"github.com/odiumuniverse/beadle/pkg/secret"
)

// a44Canon writes the canonical MCP server file.
func a44Canon(t *testing.T, f *fixture, servers mcp.Servers) {
	t.Helper()

	data, err := servers.MarshalCanonical()
	if err != nil {
		t.Fatalf("encode canon: %v", err)
	}

	write(t, f.vault.ServersPath(), string(data))
}

// a44Patch is the DSH home patch layer inside a fixture DSH_HOME.
func a44Patch(dsh string) string { return filepath.Join(dsh, "cordis.patch.yml") }

// a44Result finds the DSH result of the MCP kind report.
func a44Result(t *testing.T, report *engine.Report) engine.AgentResult {
	t.Helper()

	result, ok := report.Kind(kind.MCP).Agent(agent.DSHID)
	if !ok {
		t.Fatal("no DSH MCP result in the report")
	}

	return result
}

func TestA44DSHMCPRoundTrip(t *testing.T) {
	Convey("Given an enabled DSH adapter and a canon with two servers", t, func() {
		dsh := a38DSHHome(t)

		f := newFixture(t)
		f.enableAgent(t, agent.DSHID)
		f.emptyConfigs(t)

		a44Canon(t, f, mcp.Servers{
			"alpha": {Transport: mcp.TransportStdio, Command: []string{"npx", "-y", "alpha-mcp"}, Env: map[string]string{"A": "one"}},
			"beta":  {Transport: mcp.TransportHTTP, URL: "https://example.com/mcp"},
		})

		Convey("When the canon syncs", func() {
			report := f.sync(t)
			content := read(t, a44Patch(dsh))

			Convey("Then both servers land in the home patch layer", func() {
				So(content, ShouldContainSubstring, "beadle manages only the entries")
				So(content, ShouldContainSubstring, "id: beadle:alpha")
				So(content, ShouldContainSubstring, "serverName: alpha")
				So(content, ShouldContainSubstring, "id: beadle:beta")
				So(content, ShouldContainSubstring, "transport: streamable-http")
				So(content, ShouldContainSubstring, "A: one")
				So(a44Result(t, report).Action, ShouldEqual, engine.ActionPushed)

				Convey("And a repeat sync is a no-op with byte-stable bytes", func() {
					second := f.sync(t)

					So(read(t, a44Patch(dsh)), ShouldEqual, content)
					So(a44Result(t, second).Action, ShouldEqual, engine.ActionNoop)

					Convey("When the canon changes a server", func() {
						a44Canon(t, f, mcp.Servers{
							"alpha": {Transport: mcp.TransportStdio, Command: []string{"node", "alpha.js"}},
							"beta":  {Transport: mcp.TransportHTTP, URL: "https://example.com/mcp"},
						})

						f.sync(t)
						updated := read(t, a44Patch(dsh))

						Convey("Then the record is updated in place", func() {
							So(updated, ShouldContainSubstring, "alpha.js")
							So(updated, ShouldNotContainSubstring, "alpha-mcp")
							So(strings.Count(updated, "id: beadle:alpha"), ShouldEqual, 1)
							So(updated, ShouldContainSubstring, "id: beadle:beta")

							Convey("When the canon drops a server, its record leaves", func() {
								a44Canon(t, f, mcp.Servers{
									"beta": {Transport: mcp.TransportHTTP, URL: "https://example.com/mcp"},
								})

								f.sync(t)
								pruned := read(t, a44Patch(dsh))

								So(pruned, ShouldNotContainSubstring, "beadle:alpha")
								So(pruned, ShouldContainSubstring, "beadle:beta")
							})
						})
					})
				})
			})
		})
	})
}

func TestA44DSHMCPPreservesUserRecords(t *testing.T) {
	Convey("Given a user record sharing the patch file", t, func() {
		dsh := a38DSHHome(t)
		patch := a44Patch(dsh)

		write(t, patch, `# my layer
- insert:
    - id: user-mcp
      name: '@deepseek-ai/dsh-mcp-client'
      config:
        transport: stdio
        serverName: usermcp
        command: user-mcp
        cwd: !!js dshHomePath('mcp')
`)

		f := newFixture(t)
		f.enableAgent(t, agent.DSHID)
		f.emptyConfigs(t)

		a44Canon(t, f, mcp.Servers{
			"alpha": {Transport: mcp.TransportStdio, Command: []string{"node"}},
		})

		Convey("When the canon syncs", func() {
			f.sync(t)
			content := read(t, patch)

			Convey("Then the foreign record and comment survive", func() {
				So(content, ShouldContainSubstring, "# my layer")
				So(content, ShouldContainSubstring, "id: user-mcp")
				So(content, ShouldContainSubstring, "cwd: !!js dshHomePath('mcp')")
				So(content, ShouldContainSubstring, "id: beadle:alpha")

				Convey("And dropping the canon leaves the user file in place", func() {
					a44Canon(t, f, mcp.Servers{})
					f.sync(t)

					pruned := read(t, patch)
					So(pruned, ShouldNotContainSubstring, "beadle:")
					So(pruned, ShouldContainSubstring, "id: user-mcp")
					So(pruned, ShouldContainSubstring, "cwd: !!js dshHomePath('mcp')")
					So(pruned, ShouldContainSubstring, "# my layer")
				})
			})
		})
	})
}

func TestA44DSHMCPBlockedRecordKeepsCanon(t *testing.T) {
	Convey("Given a delivered DSH server whose record the user broke", t, func() {
		dsh := a38DSHHome(t)

		f := newFixture(t)
		f.enableAgent(t, agent.DSHID)
		f.emptyConfigs(t)

		a44Canon(t, f, mcp.Servers{
			"alpha": {Transport: mcp.TransportStdio, Command: []string{"node"}},
		})

		f.sync(t)

		patch := a44Patch(dsh)
		broken := strings.Replace(read(t, patch), "name: '@deepseek-ai/dsh-mcp-client'", "name: other-plugin", 1)
		So(broken, ShouldContainSubstring, "other-plugin")
		write(t, patch, broken)

		Convey("When sync runs", func() {
			report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then the canon keeps the server and the record stays", func() {
				So(read(t, patch), ShouldEqual, broken)
				So(f.servers(t), ShouldContainKey, "alpha")
				So(strings.Join(report.Kind(kind.MCP).Warnings, "\n"), ShouldContainSubstring, "carries name")

				Convey("And the doctor names the blocker", func() {
					issues, err := f.engine.Doctor(t.Context())
					So(err, ShouldBeNil)
					So(hasIssue(issues, engine.SeverityWarn, `MCP server "alpha" cannot reach DSH`), ShouldBeTrue)
					So(hasIssue(issues, engine.SeverityWarn, "carries name"), ShouldBeTrue)
				})
			})
		})
	})
}

func TestA44DSHMCPKeepsUserEdit(t *testing.T) {
	Convey("Given a delivered DSH record the user edits", t, func() {
		dsh := a38DSHHome(t)

		f := newFixture(t)
		f.enableAgent(t, agent.DSHID)
		f.emptyConfigs(t)

		a44Canon(t, f, mcp.Servers{
			"alpha": {Transport: mcp.TransportStdio, Command: []string{"node"}},
		})

		f.sync(t)

		patch := a44Patch(dsh)
		edited := strings.Replace(read(t, patch), "command: node", "command: /usr/local/bin/node", 1)
		write(t, patch, edited)

		Convey("When sync runs", func() {
			f.sync(t)

			Convey("Then the edit became the canon and the file was not rewritten", func() {
				So(f.servers(t)["alpha"].Command, ShouldResemble, []string{"/usr/local/bin/node"})
				So(read(t, patch), ShouldEqual, edited)
			})
		})
	})
}

func TestA44DSHMCPInvalidPatchFile(t *testing.T) {
	Convey("Given a canon server and a damaged patch file", t, func() {
		dsh := a38DSHHome(t)

		f := newFixture(t)
		f.enableAgent(t, agent.DSHID)
		f.emptyConfigs(t)

		a44Canon(t, f, mcp.Servers{
			"alpha": {Transport: mcp.TransportStdio, Command: []string{"node"}},
		})

		f.sync(t)

		patch := a44Patch(dsh)
		damaged := "not: [a list\n"
		write(t, patch, damaged)

		Convey("When sync and doctor run", func() {
			report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			issues, doctorErr := f.engine.Doctor(t.Context())
			So(doctorErr, ShouldBeNil)

			Convey("Then the failure is reported into the untouched file and the canon stays", func() {
				So(report.Errors(), ShouldNotBeEmpty)
				So(read(t, patch), ShouldEqual, damaged)
				So(f.servers(t), ShouldContainKey, "alpha")
				So(hasIssue(issues, engine.SeverityError, "parse"), ShouldBeTrue)
			})
		})
	})
}

func TestA44DSHMCPSecrets(t *testing.T) {
	Convey("Given a canon server whose env carries a secret reference", t, func() {
		dshHome := filepath.Join(t.TempDir(), "dsh")
		So(os.MkdirAll(dshHome, 0o750), ShouldBeNil)
		t.Setenv("DSH_HOME", dshHome)

		keyring := &keyringFake{values: map[string]string{"TOKEN": "s3cret"}}
		f := newKeyringFixture(t, keyring, "TOKEN")
		f.enableAgent(t, agent.DSHID)

		a44Canon(t, f, mcp.Servers{
			"alpha": {Transport: mcp.TransportStdio, Command: []string{"node"}, Env: map[string]string{"TOKEN": "{secret:TOKEN}"}},
		})

		Convey("When the canon syncs in the literal mode", func() {
			f.sync(t)

			patch := a44Patch(dshHome)

			Convey("Then the value is materialized and no reference is written", func() {
				content := read(t, patch)
				So(content, ShouldContainSubstring, "s3cret")
				So(content, ShouldNotContainSubstring, "{secret:")

				Convey("When the mode switches to env, the canonical ref form follows", func() {
					f.config.Secrets = secret.ModeEnv
					So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

					f.sync(t)

					// DSH does not interpolate config strings; the server
					// receives whatever the vault mode wrote, like the other
					// hosts without a ref syntax of their own.
					switched := read(t, patch)
					So(switched, ShouldContainSubstring, "{env:TOKEN}")
					So(switched, ShouldNotContainSubstring, "s3cret")
				})
			})
		})
	})
}

func TestA44DSHMCPUnsupportedServers(t *testing.T) {
	Convey("Given a canon with servers DSH cannot run", t, func() {
		dsh := a38DSHHome(t)

		f := newFixture(t)
		f.enableAgent(t, agent.DSHID)
		f.emptyConfigs(t)

		a44Canon(t, f, mcp.Servers{
			"ok":      {Transport: mcp.TransportStdio, Command: []string{"node"}},
			"sse-srv": {Transport: mcp.TransportSSE, URL: "https://example.com/sse"},
			"dot.srv": {Transport: mcp.TransportStdio, Command: []string{"node"}},
		})

		Convey("When the canon syncs", func() {
			report := f.sync(t)
			content := read(t, a44Patch(dsh))

			Convey("Then only the runnable server lands and the rest is reported", func() {
				So(content, ShouldContainSubstring, "id: beadle:ok")
				So(content, ShouldNotContainSubstring, "beadle:sse-srv")
				So(content, ShouldNotContainSubstring, "dot.srv")
				So(content, ShouldNotContainSubstring, "transport: sse")
				So(a44Result(t, report).Note, ShouldContainSubstring, "did not keep")

				Convey("And the doctor explains why", func() {
					issues, err := f.engine.Doctor(t.Context())
					So(err, ShouldBeNil)
					So(hasIssue(issues, engine.SeverityWarn, `DSH cannot run MCP server "sse-srv"`), ShouldBeTrue)
					So(hasIssue(issues, engine.SeverityWarn, `DSH cannot run MCP server "dot.srv"`), ShouldBeTrue)
					So(hasIssue(issues, engine.SeverityWarn, `DSH cannot run MCP server "ok"`), ShouldBeFalse)

					Convey("When the canon turns the served server into an unsupported shape", func() {
						a44Canon(t, f, mcp.Servers{
							"ok": {Transport: mcp.TransportSSE, URL: "https://example.com/sse"},
						})

						second := f.sync(t)

						Convey("Then its record is withdrawn", func() {
							pruned := read(t, a44Patch(dsh))
							So(pruned, ShouldNotContainSubstring, "beadle:ok")
							So(pruned, ShouldNotContainSubstring, "transport: stdio")
							So(a44Result(t, second).Note, ShouldContainSubstring, "did not keep")
						})
					})
				})
			})
		})
	})
}

func TestA44DSHMCPForeignClaimWarns(t *testing.T) {
	Convey("Given a foreign DSH record mounting a canon server name", t, func() {
		dsh := a38DSHHome(t)

		write(t, a44Patch(dsh), `- insert:
    - id: user-mcp
      name: '@deepseek-ai/dsh-mcp-client'
      config:
        transport: stdio
        serverName: alpha
        command: user-mcp
`)

		f := newFixture(t)
		f.enableAgent(t, agent.DSHID)
		f.emptyConfigs(t)

		a44Canon(t, f, mcp.Servers{
			"alpha": {Transport: mcp.TransportStdio, Command: []string{"node"}},
		})

		Convey("When the canon syncs", func() {
			f.sync(t)
			content := read(t, a44Patch(dsh))

			Convey("Then no second server with the name is added", func() {
				So(content, ShouldNotContainSubstring, "beadle:alpha")
				So(strings.Count(content, "serverName: alpha"), ShouldEqual, 1)

				Convey("And the doctor names the collision", func() {
					issues, err := f.engine.Doctor(t.Context())
					So(err, ShouldBeNil)
					So(hasIssue(issues, engine.SeverityWarn, `MCP server "alpha" cannot reach DSH`), ShouldBeTrue)
					So(hasIssue(issues, engine.SeverityWarn, "which beadle does not own"), ShouldBeTrue)
				})
			})
		})
	})
}

func TestA44DSHMCPHeldIDBlocksDelivery(t *testing.T) {
	Convey("Given a record mounting beta under the id beadle:alpha and both names in the canon", t, func() {
		dsh := a38DSHHome(t)

		write(t, a44Patch(dsh), `# my rename
- insert:
    - id: beadle:alpha
      name: '@deepseek-ai/dsh-mcp-client'
      config:
        transport: stdio
        serverName: beta
        command: node
`)

		f := newFixture(t)
		f.enableAgent(t, agent.DSHID)
		f.emptyConfigs(t)

		a44Canon(t, f, mcp.Servers{
			"alpha": {Transport: mcp.TransportStdio, Command: []string{"node"}},
			"beta":  {Transport: mcp.TransportStdio, Command: []string{"node"}},
		})

		Convey("When the canon syncs", func() {
			report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then beta stays delivered, alpha is blocked and the doctor names the held id", func() {
				content := read(t, a44Patch(dsh))
				So(content, ShouldContainSubstring, "# my rename")
				So(content, ShouldContainSubstring, "serverName: beta")
				So(content, ShouldNotContainSubstring, "serverName: alpha")
				So(a44Result(t, report).Note, ShouldContainSubstring, "did not keep")

				issues, doctorErr := f.engine.Doctor(t.Context())
				So(doctorErr, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityWarn,
					`MCP server "alpha" cannot reach DSH: id "beadle:alpha" is held by the record mounting "beta"`), ShouldBeTrue)
			})
		})
	})
}

func TestA44DSHMCPDoctorSilentWhenModeOff(t *testing.T) {
	Convey("Given an unsupported canon server and the DSH MCP mode off", t, func() {
		a38DSHHome(t)

		f := newFixture(t)
		f.enableAgent(t, agent.DSHID)
		f.emptyConfigs(t)

		f.config.SetMode(agent.DSHID, kind.MCP, config.ModeOff)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		a44Canon(t, f, mcp.Servers{
			"sse-srv": {Transport: mcp.TransportSSE, URL: "https://example.com/sse"},
		})

		Convey("When the doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the DSH MCP checks stay silent", func() {
				So(hasIssue(issues, engine.SeverityWarn, "DSH cannot run MCP server"), ShouldBeFalse)
				So(hasIssue(issues, engine.SeverityWarn, "cannot reach DSH"), ShouldBeFalse)
			})
		})
	})
}

func TestA44DSHMCPDoctorSilentWithoutDSH(t *testing.T) {
	Convey("Given a canon server and no DSH install", t, func() {
		withoutDSHHome(t)
		t.Setenv("PATH", "/usr/bin:/bin")

		f := newFixture(t)
		f.emptyConfigs(t)

		a44Canon(t, f, mcp.Servers{
			"sse-srv": {Transport: mcp.TransportSSE, URL: "https://example.com/sse"},
		})

		Convey("When the doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the DSH checks stay silent", func() {
				So(hasIssue(issues, engine.SeverityWarn, "DSH"), ShouldBeFalse)
				So(hasIssue(issues, engine.SeverityWarn, `DSH cannot run MCP server`), ShouldBeFalse)
			})
		})
	})
}
