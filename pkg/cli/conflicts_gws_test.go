package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func gwsRun(t *testing.T, args ...string) (string, error) {
	t.Helper()

	return gwsRunIn(t, nil, args...)
}

func gwsRunIn(t *testing.T, stdin io.Reader, args ...string) (string, error) {
	t.Helper()

	root := newRootCmd(Options{Version: "test"})

	var out bytes.Buffer

	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	root.SilenceUsage = true

	if stdin != nil {
		root.SetIn(stdin)
	}

	err := root.ExecuteContext(t.Context())

	return out.String(), err
}

func gwsHome(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))

	return home
}

func gwsWrite(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gwsRead(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: the test reads its own temp file
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func gwsInitSync(t *testing.T) {
	t.Helper()

	if _, err := gwsRun(t, "init"); err != nil {
		t.Fatal(err)
	}

	if _, err := gwsRun(t, "sync"); err != nil {
		t.Fatal(err)
	}
}

func gwsRules(t *testing.T, home, claude, opencode string) {
	t.Helper()

	gwsWrite(t, filepath.Join(home, ".claude", "CLAUDE.md"), claude)
	gwsWrite(t, filepath.Join(home, ".config", "opencode", "AGENTS.md"), opencode)
}

type gwsConflicts struct {
	Conflicts []engine.ConflictView `json:"conflicts"`
}

func gwsConflictViews(t *testing.T, args ...string) gwsConflicts {
	t.Helper()

	out, err := gwsRun(t, args...)
	if err != nil {
		t.Fatal(err)
	}

	var payload gwsConflicts
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("conflicts --json is not valid JSON: %v\n%s", err, out)
	}

	return payload
}

var gwsHashPattern = regexp.MustCompile(`[0-9a-f]{64}`)

func gwsNormalizeJSON(t *testing.T, out string) string {
	t.Helper()

	out = regexp.MustCompile(`"since": "[^"]*"`).ReplaceAllString(out, `"since": "<since>"`)
	out = regexp.MustCompile(`"file": "[^"]*"`).ReplaceAllString(out, `"file": "<file>"`)
	out = gwsHashPattern.ReplaceAllString(out, "<hash>")

	return strings.TrimRight(out, "\n")
}

func TestConflictsJSONGolden(t *testing.T) {
	Convey("Given a first sync that produced a rules conflict", t, func() {
		home := gwsHome(t)
		gwsRules(t, home, "# claude\n", "# opencode\n")
		gwsInitSync(t)

		Convey("When conflicts --json is requested", func() {
			out, err := gwsRun(t, "conflicts", "--json")

			Convey("Then the schema is stable and sorted by id", func() {
				So(err, ShouldBeNil)
				So(gwsNormalizeJSON(t, out), ShouldEqual, gwsGolden)
			})
		})
	})
}

const gwsGolden = `{
  "conflicts": [
    {
      "id": "dd7acc2a",
      "kind": "rules",
      "agent": "opencode",
      "key": "main",
      "reason": "added",
      "since": "<since>",
      "base": "",
      "vault": "<hash>",
      "agent_hash": "<hash>",
      "file": "<file>",
      "binary": false,
      "values": {
        "agent": "# opencode\n",
        "base": "",
        "vault": "# claude\n"
      },
      "patch": "--- vault\n+++ agent\n@@ -1,2 +1,2 @@\n-# claude\n+# opencode\n \n"
    }
  ]
}`

func TestConflictsJSONEmpty(t *testing.T) {
	Convey("Given a vault without conflicts", t, func() {
		gwsHome(t)

		Convey("When conflicts --json is requested", func() {
			out, err := gwsRun(t, "init")
			So(err, ShouldBeNil)
			So(out, ShouldNotBeEmpty)

			out, err = gwsRun(t, "conflicts", "--json")

			Convey("Then the list is an empty array", func() {
				So(err, ShouldBeNil)
				So(strings.TrimSpace(out), ShouldEqual, "{\n  \"conflicts\": []\n}")
			})
		})
	})
}

func TestResolveBinding(t *testing.T) {
	Convey("Given a modified rules conflict with a base", t, func() {
		home := gwsHome(t)
		gwsRules(t, home, "# v1\n", "# v1\n")
		gwsInitSync(t)

		gwsRules(t, home, "# claude v2\n", "# opencode v2\n")

		if _, err := gwsRun(t, "sync"); err != nil {
			t.Fatal(err)
		}

		views := gwsConflictViews(t, "conflicts", "--json")
		So(views.Conflicts, ShouldHaveLength, 1)

		c := views.Conflicts[0]
		So(c.Base, ShouldNotBeEmpty)

		base := string(c.Base)
		vault := string(c.Vault)
		agentHash := string(c.AgentHash)

		resolved := filepath.Join(home, "resolved.md")
		gwsWrite(t, resolved, "# merged\n")

		Convey("When --from is used without --expect-base", func() {
			_, err := gwsRun(t, "resolve", c.ID, "--from", resolved)

			Convey("Then it is rejected", func() {
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, "--expect-base")
			})
		})

		Convey("When --expect-base is set but --expect-vault is not", func() {
			_, err := gwsRun(t, "resolve", c.ID, "--from", resolved, "--expect-base", base)

			Convey("Then it is rejected before anything is read", func() {
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, "--expect-vault")
			})
		})

		Convey("When the binding does not match", func() {
			out, err := gwsRun(t, "resolve", c.ID, "--from", resolved,
				"--expect-base", "deadbeef", "--expect-vault", vault, "--expect-agent", agentHash)

			Convey("Then the resolution is refused as stale and nothing is applied", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "refused "+c.ID+": stale-conflict")
				So(gwsRead(t, filepath.Join(home, ".claude", "CLAUDE.md")), ShouldEqual, "# claude v2\n")
				So(gwsConflictViews(t, "conflicts", "--json").Conflicts, ShouldHaveLength, 1)

				Convey("And the json report carries the refusal", func() {
					jout, err := gwsRun(t, "resolve", c.ID, "--from", resolved,
						"--expect-base", "deadbeef", "--expect-vault", vault, "--expect-agent", agentHash, "--json")
					So(err, ShouldBeNil)
					So(jout, ShouldContainSubstring, `"code": "stale-conflict"`)
				})

				Convey("And doctor reports the recent refusal", func() {
					dout, err := gwsRun(t, "doctor")
					So(err, ShouldBeNil)
					So(dout, ShouldContainSubstring, "were refused recently")
				})
			})
		})

		Convey("When the agent value changed after the read", func() {
			gwsWrite(t, filepath.Join(home, ".config", "opencode", "AGENTS.md"), "# opencode v3\n")

			if _, err := gwsRun(t, "sync"); err != nil {
				t.Fatal(err)
			}

			out, err := gwsRun(t, "resolve", c.ID, "--from", resolved,
				"--expect-base", base, "--expect-vault", vault, "--expect-agent", agentHash)

			Convey("Then the stale agent hash is refused too", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "stale-conflict")
			})
		})

		Convey("When the binding matches", func() {
			out, err := gwsRun(t, "resolve", c.ID, "--from", resolved,
				"--expect-base", base, "--expect-vault", vault, "--expect-agent", agentHash)

			Convey("Then it resolves and lands in both agents", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "resolved 1 conflict(s)")
				So(gwsRead(t, filepath.Join(home, ".claude", "CLAUDE.md")), ShouldEqual, "# merged\n")
				So(gwsRead(t, filepath.Join(home, ".config", "opencode", "AGENTS.md")), ShouldEqual, "# merged\n")
				So(gwsConflictViews(t, "conflicts", "--json").Conflicts, ShouldBeEmpty)
			})
		})
	})
}

func TestResolveStdinAndInvalidContent(t *testing.T) {
	Convey("Given a rules conflict with a base", t, func() {
		home := gwsHome(t)
		gwsRules(t, home, "# v1\n", "# v1\n")
		gwsInitSync(t)
		gwsRules(t, home, "# claude v2\n", "# opencode v2\n")

		if _, err := gwsRun(t, "sync"); err != nil {
			t.Fatal(err)
		}

		c := gwsConflictViews(t, "conflicts", "--json").Conflicts[0]

		expectArgs := []string{"--expect-base", string(c.Base), "--expect-vault", string(c.Vault), "--expect-agent", string(c.AgentHash)}

		Convey("When --stdin carries conflict markers", func() {
			args := append([]string{"resolve", c.ID, "--stdin"}, expectArgs...)
			out, err := gwsRunIn(t, strings.NewReader("<<<<<<< vault\n# a\n"), args...)

			Convey("Then it is refused as invalid-content", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "invalid-content")
				So(gwsConflictViews(t, "conflicts", "--json").Conflicts, ShouldHaveLength, 1)
			})
		})

		Convey("When --from and --stdin are combined", func() {
			_, err := gwsRun(t, "resolve", c.ID, "--from", "x", "--stdin")

			Convey("Then it is rejected", func() {
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, "either")
			})
		})

		Convey("When --stdin carries clean content", func() {
			args := append([]string{"resolve", c.ID, "--stdin"}, expectArgs...)
			out, err := gwsRunIn(t, strings.NewReader("# via stdin\n"), args...)

			Convey("Then it resolves", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "resolved 1 conflict(s)")
				So(gwsRead(t, filepath.Join(home, ".claude", "CLAUDE.md")), ShouldEqual, "# via stdin\n")
			})
		})

		Convey("When an ambiguous id prefix is used", func() {
			gwsWrite(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {"alpha": {"type": "stdio", "command": "a"}}}`)
			gwsWrite(t, filepath.Join(home, ".config", "opencode", "opencode.json"), `{"mcp": {"alpha": {"type": "local", "command": ["b"]}}}`)

			if _, err := gwsRun(t, "sync"); err != nil {
				t.Fatal(err)
			}

			out, err := gwsRun(t, "resolve", "", "--take", "vault")

			Convey("Then it is refused as ambiguous", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "ambiguous")
			})
		})

		Convey("When the id is unknown", func() {
			out, err := gwsRun(t, "resolve", "ffffffff", "--take", "vault")

			Convey("Then it is refused as unknown-conflict", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "unknown-conflict")
			})
		})
	})
}

func TestResolveRiskyMCPCommand(t *testing.T) {
	Convey("Given an MCP conflict over a server command", t, func() {
		home := gwsHome(t)
		gwsWrite(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {"alpha": {"type": "stdio", "command": "v1"}}}`)
		gwsWrite(t, filepath.Join(home, ".config", "opencode", "opencode.json"), `{"mcp": {"alpha": {"type": "local", "command": ["v2"]}}}`)
		gwsInitSync(t)

		views := gwsConflictViews(t, "conflicts", "--json")
		So(views.Conflicts, ShouldHaveLength, 1)

		c := views.Conflicts[0]
		So(c.Kind, ShouldEqual, kind.MCP)

		risky := filepath.Join(home, "alpha.json")
		gwsWrite(t, risky, `{"transport": "stdio", "command": ["v9"]}`)

		expectArgs := []string{"--expect-base", string(c.Base), "--expect-vault", string(c.Vault), "--expect-agent", string(c.AgentHash)}

		Convey("When the new content changes the command without --allow-risky", func() {
			args := append([]string{"resolve", c.ID, "--from", risky}, expectArgs...)
			out, err := gwsRun(t, args...)

			Convey("Then it is refused as risky-change", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "risky-change")
				So(gwsConflictViews(t, "conflicts", "--json").Conflicts, ShouldHaveLength, 1)
			})
		})

		Convey("When the content is not a valid server", func() {
			broken := filepath.Join(home, "broken.json")
			gwsWrite(t, broken, `{"not": "a server"}`)

			args := append([]string{"resolve", c.ID, "--from", broken}, expectArgs...)
			out, err := gwsRun(t, args...)

			Convey("Then it is refused as invalid-content", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "invalid-content")
			})
		})

		Convey("When --allow-risky is passed", func() {
			args := append([]string{"resolve", c.ID, "--from", risky}, expectArgs...)
			out, err := gwsRun(t, append(args, "--allow-risky")...)

			Convey("Then the resolution goes through", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "resolved 1 conflict(s)")
				So(gwsConflictViews(t, "conflicts", "--json").Conflicts, ShouldBeEmpty)
			})
		})
	})
}

func TestSeedSyncPresentsSkill(t *testing.T) {
	Convey("Given a fresh vault created by init", t, func() {
		home := gwsHome(t)
		gwsWrite(t, filepath.Join(home, ".claude", "CLAUDE.md"), "# claude\n")

		Convey("When the first sync runs", func() {
			gwsInitSync(t)

			Convey("Then the seeded beadle-conflicts skill reaches Claude Code", func() {
				presented := filepath.Join(home, ".claude", "skills", "beadle-conflicts", "SKILL.md")
				So(gwsRead(t, presented), ShouldContainSubstring, "name: beadle-conflicts")

				Convey("And re-seeding without --force does not clobber it", func() {
					out, err := gwsRun(t, "skills", "seed")
					So(err, ShouldBeNil)
					So(out, ShouldContainSubstring, "already in the vault")
				})
			})
		})
	})
}

func TestConflictsRefusalRedaction(t *testing.T) {
	Convey("Given a permissions conflict and a stored secret", t, func() {
		home := gwsHome(t)
		gwsWrite(t, filepath.Join(home, ".claude", "settings.json"), `{"permissions": {"allow": ["Bash(git status:*)"]}}`)
		gwsWrite(t, filepath.Join(home, ".config", "opencode", "opencode.json"), `{"permission": {"bash": {"git status*": "deny"}}}`)

		if _, err := gwsRun(t, "init"); err != nil {
			t.Fatal(err)
		}

		if _, err := gwsRun(t, "kinds", "enable", "permissions"); err != nil {
			t.Fatal(err)
		}

		for _, agentID := range []string{"claude-code", "opencode"} {
			if _, err := gwsRun(t, "agents", "mode", agentID, "permissions", "sync"); err != nil {
				t.Fatal(err)
			}
		}

		if _, err := gwsRun(t, "sync"); err != nil {
			t.Fatal(err)
		}

		if _, err := gwsRun(t, "secrets", "set", "API_TOKEN", "s3cr3t-value"); err != nil {
			t.Fatal(err)
		}

		views := gwsConflictViews(t, "conflicts", "--json")
		So(views.Conflicts, ShouldHaveLength, 1)

		c := views.Conflicts[0]
		So(c.Kind, ShouldEqual, kind.Permissions)

		bad := filepath.Join(home, "bad-effect.txt")
		gwsWrite(t, bad, "s3cr3t-value")

		Convey("When the invalid content carries a secret", func() {
			out, err := gwsRun(t, "resolve", c.ID, "--from", bad,
				"--expect-base", string(c.Base), "--expect-vault", string(c.Vault), "--expect-agent", string(c.AgentHash))

			Convey("Then the refusal is redacted", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "invalid-content")
				So(out, ShouldContainSubstring, "⟨secret:API_TOKEN⟩")
				So(out, ShouldNotContainSubstring, "s3cr3t-value")

				Convey("And the journal in state.json holds no secret", func() {
					journal := gwsRead(t, filepath.Join(home, ".beadle", "state.json"))
					So(journal, ShouldContainSubstring, "⟨secret:API_TOKEN⟩")
					So(journal, ShouldNotContainSubstring, "s3cr3t-value")
				})
			})
		})
	})
}

func TestConflictsJSONOrderAndBinary(t *testing.T) {
	Convey("Given a rules conflict and a binary skills conflict", t, func() {
		home := gwsHome(t)
		gwsRules(t, home, "# claude\n", "# opencode\n")
		gwsWrite(t, filepath.Join(home, ".claude", "skills", "tool", "data.bin"), string([]byte{0, 1, 2, 3}))
		gwsWrite(t, filepath.Join(home, ".config", "opencode", "skills", "tool", "data.bin"), string([]byte{9, 8, 7}))

		gwsInitSync(t)

		views := gwsConflictViews(t, "conflicts", "--json")

		Convey("Then conflicts are sorted by id", func() {
			So(views.Conflicts, ShouldHaveLength, 2)

			ids := make([]string, 0, len(views.Conflicts))
			for _, view := range views.Conflicts {
				ids = append(ids, view.ID)
			}

			So(slices.IsSorted(ids), ShouldBeTrue)

			Convey("And the binary conflict omits values and reports sizes", func() {
				var binary engine.ConflictView

				for _, view := range views.Conflicts {
					if view.Kind == kind.Skills {
						binary = view
					}
				}

				So(binary.Binary, ShouldBeTrue)
				So(binary.Values, ShouldBeEmpty)
				So(binary.Sizes, ShouldNotBeEmpty)
				So(binary.Patch, ShouldBeEmpty)
			})
		})
	})
}

func TestResolveAllSkipsPermissions(t *testing.T) {
	Convey("Given both a rules and a permissions conflict", t, func() {
		home := gwsHome(t)
		gwsRules(t, home, "# claude\n", "# opencode\n")
		gwsWrite(t, filepath.Join(home, ".claude", "settings.json"), `{"permissions": {"allow": ["Bash(git status:*)"]}}`)
		gwsWrite(t, filepath.Join(home, ".config", "opencode", "opencode.json"), `{"permission": {"bash": {"git status*": "deny"}}}`)

		if _, err := gwsRun(t, "init"); err != nil {
			t.Fatal(err)
		}

		if _, err := gwsRun(t, "kinds", "enable", "permissions"); err != nil {
			t.Fatal(err)
		}

		for _, agentID := range []string{"claude-code", "opencode"} {
			if _, err := gwsRun(t, "agents", "mode", agentID, "permissions", "sync"); err != nil {
				t.Fatal(err)
			}
		}

		if _, err := gwsRun(t, "sync"); err != nil {
			t.Fatal(err)
		}

		views := gwsConflictViews(t, "conflicts", "--json")
		So(views.Conflicts, ShouldHaveLength, 2)

		var permissions engine.ConflictView

		for _, view := range views.Conflicts {
			if view.Kind == kind.Permissions {
				permissions = view
			}
		}

		So(permissions.Kind, ShouldEqual, kind.Permissions)

		Convey("When a permissions content resolve is attempted", func() {
			effect := filepath.Join(home, "effect.txt")
			gwsWrite(t, effect, "deny")

			out, err := gwsRun(t, "resolve", permissions.ID, "--from", effect,
				"--expect-base", string(permissions.Base), "--expect-vault", string(permissions.Vault), "--expect-agent", string(permissions.AgentHash))

			Convey("Then it is refused as risky-change", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "risky-change")
			})
		})

		Convey("When resolve --all runs", func() {
			out, err := gwsRun(t, "resolve", "--all", "--take", "vault")

			Convey("Then permissions stays open and is never silently resolved", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "resolved 1 conflict(s)")

				remaining := gwsConflictViews(t, "conflicts", "--json").Conflicts
				So(remaining, ShouldHaveLength, 1)
				So(remaining[0].Kind, ShouldEqual, kind.Permissions)

				Convey("And an explicit --kind permissions resolves it", func() {
					out, err := gwsRun(t, "resolve", "--all", "--kind", "permissions", "--take", "vault")
					So(err, ShouldBeNil)
					So(out, ShouldContainSubstring, "resolved 1 conflict(s)")
					So(gwsConflictViews(t, "conflicts", "--json").Conflicts, ShouldBeEmpty)
				})
			})
		})
	})
}
