package engine_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/permission"
	"github.com/odiumuniverse/beadle/pkg/skill"
)

// kiloFixtureConfig exercises every managed section of the Kilo config plus
// foreign keys and a comment that must survive patches byte for byte.
const kiloFixtureConfig = `{
  // keep this comment
  "$schema": "https://kilo.ai/config.json",
  "mcp": {
    "local-srv": {"type": "local", "command": ["npx", "-y", "pkg"], "environment": {"K": "V"}},
    "remote-srv": {"type": "remote", "url": "https://example.com/mcp", "headers": {"X-Trace": "on"}}
  },
  "permission": {"bash": {"git status": "allow"}, "edit": "deny"},
  "instructions": ["AGENTS.md"]
}
`

func piDir(f *fixture) string   { return filepath.Join(f.home, ".pi", "agent") }
func kiloDir(f *fixture) string { return filepath.Join(f.home, ".config", "kilo") }

func piMCPPath(f *fixture) string    { return filepath.Join(piDir(f), "mcp.json") }
func piAgentsPath(f *fixture) string { return filepath.Join(piDir(f), "AGENTS.md") }
func piSkillsDir(f *fixture) string  { return filepath.Join(piDir(f), "skills") }

func kiloConfigPath(f *fixture) string { return filepath.Join(kiloDir(f), "kilo.jsonc") }
func kiloAgentsPath(f *fixture) string { return filepath.Join(kiloDir(f), "AGENTS.md") }
func kiloSkillsDir(f *fixture) string  { return filepath.Join(f.home, ".kilo", "skills") }

// isolatedFixture enables exactly one host, so a Pi or Kilo report is not
// mixed with the default Claude/OpenCode surfaces.
func isolatedFixture(t *testing.T, id string) *fixture {
	t.Helper()

	f := newFixture(t)
	f.config.Disable(agent.ClaudeCodeID)
	f.config.Disable(agent.OpenCodeID)
	f.config.Enable(id)

	if err := f.config.Save(f.vault.ConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	return f
}

// piFixture enables only Pi; withMCP controls whether its non-creatable
// mcp.json exists.
func piFixture(t *testing.T, withMCP bool) *fixture {
	t.Helper()

	f := isolatedFixture(t, agent.PiID)

	if withMCP {
		write(t, piMCPPath(f), `{"mcpServers": {}}`)
	}

	write(t, piAgentsPath(f), "# pi rules\n")

	if err := os.MkdirAll(piSkillsDir(f), 0o750); err != nil {
		t.Fatalf("mkdir pi skills: %v", err)
	}

	return f
}

// kiloFixture enables only Kilo (with the permissions kind on) and its fixed
// config directory on disk.
func kiloFixture(t *testing.T) *fixture {
	t.Helper()

	f := isolatedFixture(t, agent.KiloID)

	f.config.Permissions = permission.ModeSync

	if err := f.config.Save(f.vault.ConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	if err := os.MkdirAll(kiloDir(f), 0o750); err != nil {
		t.Fatalf("mkdir kilo config: %v", err)
	}

	if err := os.MkdirAll(kiloSkillsDir(f), 0o750); err != nil {
		t.Fatalf("mkdir kilo skills: %v", err)
	}

	return f
}

// explainHostRows returns the explain rows of one host.
func explainHostRows(t *testing.T, f *fixture, id string) []engine.Explanation {
	t.Helper()

	rows, err := f.engine.Explain(t.Context(), "alpha")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}

	var out []engine.Explanation

	for _, row := range rows {
		if row.Host == id {
			out = append(out, row)
		}
	}

	return out
}

func alphaDigest() string {
	return string(skill.TreeDigest(skill.Tree{"SKILL.md": []byte("# alpha\n")}))[:12]
}

// noopSync runs one more sync and returns the report plus the pre-sync bytes
// of every given path, so the caller can assert both sides stayed put.
func noopSync(t *testing.T, f *fixture, paths ...string) (*engine.Report, map[string]string) {
	t.Helper()

	before := make(map[string]string, len(paths))

	for _, path := range paths {
		before[path] = read(t, path)
	}

	return f.sync(t), before
}

// assertPushGolden checks the pushed file against the golden bytes and that
// the follow-up sync is a no-op on the given kind, on the file and on the
// canon.
func assertPushGolden(t *testing.T, f *fixture, filePath, canonPath, golden string, k kind.ID, agentID string) {
	t.Helper()

	if got := read(t, filePath); got != golden {
		t.Fatalf("%s = %q, want the golden bytes %q", filePath, got, golden)
	}

	report, before := noopSync(t, f, filePath, canonPath)

	if errs := report.Errors(); len(errs) != 0 {
		t.Fatalf("sync errors during the no-op sync: %v", errs)
	}

	kindReport := report.Kind(k)
	if kindReport == nil {
		t.Fatalf("%s kind is missing from the report", k)
	}

	if kindReport.VaultChanged {
		t.Fatalf("%s changed the vault during the no-op sync", k)
	}

	if action := report.Action(k, agentID); action != engine.ActionNoop {
		t.Fatalf("%s/%s action = %s, want noop", k, agentID, action)
	}

	if got := read(t, filePath); got != before[filePath] {
		t.Fatalf("%s changed during the no-op sync", filePath)
	}

	if got := read(t, canonPath); got != before[canonPath] {
		t.Fatalf("%s changed during the no-op sync", canonPath)
	}
}

func TestPiEngineRoundTrip(t *testing.T) {
	Convey("Given a Pi home with an mcp server and rules", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := piFixture(t, true)

		const piMCP = `{"mcpServers": {"demo": {"command": "demo-mcp", "transport": "stdio"}}}`

		write(t, piMCPPath(f), piMCP)

		Convey("When sync runs", func() {
			f.sync(t)

			Convey("Then the server and the rules reach the canon and the file bytes stay", func() {
				servers := f.servers(t)
				So(servers, ShouldContainKey, "demo")
				So(servers["demo"].Command, ShouldResemble, []string{"demo-mcp"})
				So(read(t, f.vault.RulesPath()), ShouldEqual, "# pi rules\n")
				So(read(t, piMCPPath(f)), ShouldEqual, piMCP)
			})

			Convey("When the Pi file changes", func() {
				const edited = `{"mcpServers": {"demo": {"command": "demo-v2", "transport": "stdio"}}}`

				write(t, piMCPPath(f), edited)

				f.sync(t)

				Convey("Then the canon follows and the file stays exactly as edited", func() {
					So(f.servers(t)["demo"].Command, ShouldResemble, []string{"demo-v2"})

					assertPushGolden(t, f, piMCPPath(f), f.vault.ServersPath(), edited, kind.MCP, agent.PiID)
				})
			})

			Convey("When the canon changes", func() {
				// Golden bytes captured from the deterministic render; update
				// them only deliberately.
				const golden = `{"mcpServers": {"demo": {"command":"canon-mcp","transport":"stdio"}}}`

				write(t, f.vault.ServersPath(), `{"demo":{"transport":"stdio","command":["canon-mcp"]}}`)

				f.sync(t)

				Convey("Then the Pi file follows byte for byte", func() {
					assertPushGolden(t, f, piMCPPath(f), f.vault.ServersPath(), golden, kind.MCP, agent.PiID)
				})
			})

			Convey("Then a second sync is a no-op with identical bytes on both kinds", func() {
				report, before := noopSync(t, f, piMCPPath(f), f.vault.ServersPath(), piAgentsPath(f))

				So(report.Kind(kind.MCP).VaultChanged, ShouldBeFalse)
				So(report.Action(kind.MCP, agent.PiID), ShouldEqual, engine.ActionNoop)
				So(report.Kind(kind.Rules).VaultChanged, ShouldBeFalse)
				So(report.Action(kind.Rules, agent.PiID), ShouldEqual, engine.ActionNoop)
				So(read(t, piMCPPath(f)), ShouldEqual, before[piMCPPath(f)])
				So(read(t, f.vault.ServersPath()), ShouldEqual, before[f.vault.ServersPath()])
				So(read(t, piAgentsPath(f)), ShouldEqual, before[piAgentsPath(f)])
			})
		})
	})
}

func TestPiNonCreatableMCP(t *testing.T) {
	Convey("Given a Pi home without mcp.json", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := piFixture(t, false)

		const canon = `{"demo":{"transport":"stdio","command":["demo-mcp"]}}`

		write(t, f.vault.ServersPath(), canon)

		Convey("When sync runs", func() {
			report := f.sync(t)
			result, ok := report.Kind(kind.MCP).Agent(agent.PiID)

			Convey("Then the push is skipped and the canon is untouched", func() {
				So(ok, ShouldBeTrue)
				So(result.Action, ShouldEqual, engine.ActionSkipped)
				So(result.Note, ShouldContainSubstring, "no config file to write into")
				So(result.Note, ShouldContainSubstring, piMCPPath(f))
				So(result.Changes, ShouldBeEmpty)
				So(read(t, f.vault.ServersPath()), ShouldEqual, canon)
				So(fsutil.Exists(piMCPPath(f)), ShouldBeFalse)
			})

			Convey("When the empty config appears", func() {
				write(t, piMCPPath(f), "{}")

				f.sync(t)

				Convey("Then the server is written byte for byte and the canon stays", func() {
					So(read(t, f.vault.ServersPath()), ShouldEqual, canon)

					assertPushGolden(t, f, piMCPPath(f), f.vault.ServersPath(),
						`{"mcpServers":{"demo":{"command":"demo-mcp","transport":"stdio"}}}`, kind.MCP, agent.PiID)
				})
			})
		})
	})
}

func TestPiAndKiloSkillsStayPullOnly(t *testing.T) {
	Convey("Given Pi enabled with a shared skill copy", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := piFixture(t, true)

		write(t, f.vaultSkill("alpha"), "# alpha\n")
		write(t, f.sharedSkill("alpha"), "# alpha\n")

		f.sync(t)

		Convey("Then Pi writes nothing into its own skills directory", func() {
			So(fsutil.Exists(filepath.Join(piSkillsDir(f), "alpha")), ShouldBeFalse)
			So(explainHostRows(t, f, agent.PiID), ShouldResemble, []engine.Explanation{
				{Host: agent.PiID, Channel: "shared", Path: "~/.agents/skills/alpha", Digest: alphaDigest(), Role: "winner"},
			})
		})
	})

	Convey("Given Kilo enabled with a shared skill copy", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := kiloFixture(t)

		write(t, f.vaultSkill("alpha"), "# alpha\n")
		write(t, f.sharedSkill("alpha"), "# alpha\n")

		f.sync(t)

		Convey("Then Kilo writes nothing into its own skills directory", func() {
			So(fsutil.Exists(filepath.Join(kiloSkillsDir(f), "alpha")), ShouldBeFalse)
			So(explainHostRows(t, f, agent.KiloID), ShouldResemble, []engine.Explanation{
				{Host: agent.KiloID, Channel: "shared", Path: "~/.agents/skills/alpha", Digest: alphaDigest(), Role: "winner"},
			})
		})
	})
}

func TestKiloEngineRoundTrip(t *testing.T) {
	Convey("Given a Kilo config with comments, mcp, permissions and foreign keys", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := kiloFixture(t)

		write(t, kiloConfigPath(f), kiloFixtureConfig)
		write(t, kiloAgentsPath(f), "# kilo rules\n")

		Convey("When sync runs", func() {
			report := f.sync(t)
			So(report.Errors(), ShouldBeEmpty)

			Convey("Then mcp, permissions and rules reach the canon and the file stays byte-identical", func() {
				servers := f.servers(t)
				So(servers, ShouldHaveLength, 2)
				So(servers["local-srv"].Command, ShouldResemble, []string{"npx", "-y", "pkg"})
				So(servers["local-srv"].Env, ShouldResemble, map[string]string{"K": "V"})
				So(servers["remote-srv"].URL, ShouldEqual, "https://example.com/mcp")

				rules := f.rules(t)
				So(rules["bash:git status"], ShouldEqual, "allow")
				So(rules["tool:edit"], ShouldEqual, "deny")

				So(read(t, f.vault.RulesPath()), ShouldEqual, "# kilo rules\n")
				So(read(t, kiloConfigPath(f)), ShouldEqual, kiloFixtureConfig)
			})

			Convey("When a permission changes in the file", func() {
				const edited = `{"permission": {"edit": "ask"}}`

				write(t, kiloConfigPath(f), edited)

				f.sync(t)

				Convey("Then the canon follows and the file stays exactly as edited", func() {
					So(f.rules(t)["tool:edit"], ShouldEqual, "ask")

					assertPushGolden(t, f, kiloConfigPath(f), f.vault.PermissionsPath(), edited, kind.Permissions, agent.KiloID)
				})
			})

			Convey("When the canon permission changes", func() {
				write(t, f.vault.PermissionsPath(), `{"tool:edit":"allow"}`+"\n")

				f.sync(t)

				Convey("Then Kilo follows the canon byte for byte", func() {
					// Golden bytes captured from the deterministic render;
					// update them only deliberately. The empty "bash" parent is
					// the known openCodeCodec behavior (parents are not pruned).
					assertPushGolden(t, f, kiloConfigPath(f), f.vault.PermissionsPath(), `{
  // keep this comment
  "$schema": "https://kilo.ai/config.json",
  "mcp": {
    "local-srv": {"type": "local", "command": ["npx", "-y", "pkg"], "environment": {"K": "V"}},
    "remote-srv": {"type": "remote", "url": "https://example.com/mcp", "headers": {"X-Trace": "on"}}
  },
  "permission": {"bash": {}, "edit": "allow"},
  "instructions": ["AGENTS.md"]
}
`, kind.Permissions, agent.KiloID)
				})
			})

			Convey("When the canon mcp changes", func() {
				write(t, f.vault.ServersPath(), `{"local-srv":{"transport":"stdio","command":["canon-pkg"]}}`)

				f.sync(t)

				Convey("Then Kilo follows the canon and keeps the unmanaged regions byte for byte", func() {
					// Golden bytes captured from the deterministic render;
					// update them only deliberately.
					assertPushGolden(t, f, kiloConfigPath(f), f.vault.ServersPath(), `{
  // keep this comment
  "$schema": "https://kilo.ai/config.json",
  "mcp": {
    "local-srv": {"command":["canon-pkg"],"type":"local"}
  },
  "permission": {"bash": {"git status": "allow"}, "edit": "deny"},
  "instructions": ["AGENTS.md"]
}
`, kind.MCP, agent.KiloID)
				})
			})

			Convey("Then a second sync is a no-op with identical bytes", func() {
				report, before := noopSync(t, f,
					kiloConfigPath(f), kiloAgentsPath(f),
					f.vault.ServersPath(), f.vault.PermissionsPath(), f.vault.RulesPath())

				So(report.Errors(), ShouldBeEmpty)
				So(report.Kind(kind.MCP).VaultChanged, ShouldBeFalse)
				So(report.Kind(kind.Rules).VaultChanged, ShouldBeFalse)
				So(report.Action(kind.MCP, agent.KiloID), ShouldEqual, engine.ActionNoop)
				So(report.Action(kind.Permissions, agent.KiloID), ShouldEqual, engine.ActionNoop)
				So(report.Action(kind.Rules, agent.KiloID), ShouldEqual, engine.ActionNoop)
				So(read(t, kiloConfigPath(f)), ShouldEqual, before[kiloConfigPath(f)])
				So(read(t, kiloAgentsPath(f)), ShouldEqual, before[kiloAgentsPath(f)])
				So(read(t, f.vault.ServersPath()), ShouldEqual, before[f.vault.ServersPath()])
				So(read(t, f.vault.PermissionsPath()), ShouldEqual, before[f.vault.PermissionsPath()])
				So(read(t, f.vault.RulesPath()), ShouldEqual, before[f.vault.RulesPath()])
			})
		})
	})
}

func TestKiloIgnoresXDGConfigHome(t *testing.T) {
	Convey("Given Kilo with XDG_CONFIG_HOME pointing at a decoy", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := kiloFixture(t)

		write(t, kiloConfigPath(f), kiloFixtureConfig)
		write(t, kiloAgentsPath(f), "# kilo rules\n")

		xdg := t.TempDir()
		decoyPath := filepath.Join(xdg, "kilo", "kilo.jsonc")
		write(t, decoyPath, `{"mcp": {"decoy": {"type": "local", "command": ["decoy-mcp"]}}}`)

		decoyBefore := read(t, decoyPath)

		t.Setenv("XDG_CONFIG_HOME", xdg)

		Convey("When sync runs", func() {
			f.sync(t)

			Convey("Then only the home config is managed", func() {
				So(f.servers(t), ShouldContainKey, "local-srv")
				So(f.servers(t), ShouldNotContainKey, "decoy")
				So(read(t, decoyPath), ShouldEqual, decoyBefore)
				So(read(t, kiloConfigPath(f)), ShouldNotContainSubstring, "decoy-mcp")
			})
		})
	})
}
