package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/subagent"
)

func TestOpenCodeSubagentsPushMigratesLegacy(t *testing.T) {
	Convey("Given a legacy v1 OpenCode agent with tool and permission maps", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode", "agents")

		writeFile(t, filepath.Join(dir, "old.md"),
			"---\ndescription: Old\nmode: subagent\nprompt: Old prompt.\ntools:\n  write: false\n  bash: true\n"+
				"permission:\n  edit: deny\n  shell:\n    \"git push *\": ask\n---\n")

		opencode := agent.OpenCode(home, home)
		surface := surfaceOf(t, opencode, kind.Subagents)
		snap := snapshot(t, opencode, kind.Subagents)

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), snap.Items), ShouldBeNil)

			data := readFile(t, filepath.Join(dir, "old.md"))
			keys := frontmatterKeys(t, data)

			Convey("Then no legacy key survives the write", func() {
				for _, key := range []string{"prompt", "permission", "tools", "temperature", "top_p", "disable", "maxSteps", "options"} {
					So(keys, ShouldNotContainKey, key)
				}
			})

			Convey("Then the v1 write-deny and the shell pattern survive as v2 rules", func() {
				rules, ok := keys["permissions"].([]any)
				So(ok, ShouldBeTrue)

				var editDeny, gitPushAsk bool

				for _, entry := range rules {
					rule, ok := entry.(map[string]any)
					So(ok, ShouldBeTrue)

					if rule["action"] == "edit" && rule["effect"] == "deny" {
						editDeny = true
					}

					if rule["action"] == "shell" && rule["resource"] == "git push *" && rule["effect"] == "ask" {
						gitPushAsk = true
					}
				}

				So(editDeny, ShouldBeTrue)
				So(gitPushAsk, ShouldBeTrue)
			})

			Convey("Then a second write is a no-op", func() {
				So(surface.Write(t.Context(), snap.Items), ShouldBeNil)
				So(readFile(t, filepath.Join(dir, "old.md")), ShouldEqual, data)
			})
		})
	})
}

func TestOpenCodeSubagentsPushPreservesUnmanagedRules(t *testing.T) {
	Convey("Given a v2 agent with resource-scoped and ask rules", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode", "agents")

		writeFile(t, filepath.Join(dir, "guard.md"),
			"---\ndescription: Guard\nmode: subagent\npermissions:\n  - action: shell\n    resource: \"git push *\"\n    effect: deny\n"+
				"  - action: read\n    resource: \"*\"\n    effect: ask\n---\nGuard.\n")

		opencode := agent.OpenCode(home, home)
		surface := surfaceOf(t, opencode, kind.Subagents)
		snap := snapshot(t, opencode, kind.Subagents)

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), snap.Items), ShouldBeNil)

			data := readFile(t, filepath.Join(dir, "guard.md"))
			keys := frontmatterKeys(t, data)

			Convey("Then both rules stay and the write is idempotent", func() {
				rules, ok := keys["permissions"].([]any)
				So(ok, ShouldBeTrue)

				var scopedDeny, readAsk bool

				for _, entry := range rules {
					rule, ok := entry.(map[string]any)
					So(ok, ShouldBeTrue)

					if rule["action"] == "shell" && rule["resource"] == "git push *" && rule["effect"] == "deny" {
						scopedDeny = true
					}

					if rule["action"] == "read" && rule["effect"] == "ask" {
						readAsk = true
					}
				}

				So(scopedDeny, ShouldBeTrue)
				So(readAsk, ShouldBeTrue)

				So(surface.Write(t.Context(), snap.Items), ShouldBeNil)
				So(readFile(t, filepath.Join(dir, "guard.md")), ShouldEqual, data)
			})
		})
	})
}

func TestOpenCodeSubagentsPushValidatesSchema(t *testing.T) {
	Convey("Given a canon with a Claude color, an alias model and a bad mode", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode", "agents")

		opencode := agent.OpenCode(home, home)
		surface := surfaceOf(t, opencode, kind.Subagents)

		desired := kind.Items{
			"x.md": subagent.Render(subagent.Document{
				Name: "x", Description: "d", Mode: "weird", Model: "haiku", Color: "blue", Body: "b\n",
			}),
		}

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), desired), ShouldBeNil)

			data := readFile(t, filepath.Join(dir, "x.md"))
			keys := frontmatterKeys(t, data)

			Convey("Then the file holds only schema-valid values and still loads", func() {
				So(keys, ShouldNotContainKey, "color")
				So(keys, ShouldNotContainKey, "model")
				So(keys["mode"], ShouldEqual, "subagent")

				snap := snapshot(t, opencode, kind.Subagents)
				So(snap.Items, ShouldContainKey, "x.md")
			})
		})
	})
}

func TestClaudeSubagentsPushValidatesSchema(t *testing.T) {
	Convey("Given a canon with palette and out-of-palette values", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".claude", "agents")

		claude := agent.ClaudeCode(home, home)
		surface := surfaceOf(t, claude, kind.Subagents)

		valid := subagent.Render(subagent.Document{
			Name: "a", Description: "d", Color: "blue", PermissionMode: "plan", Body: "b\n",
		})
		invalid := subagent.Render(subagent.Document{
			Name: "a", Description: "d", Color: "#ff0000", PermissionMode: "weird", Isolation: "vm", Body: "b\n",
		})

		Convey("When valid values are written", func() {
			So(surface.Write(t.Context(), kind.Items{"a.md": valid}), ShouldBeNil)

			Convey("Then the palette color and the permission mode are written", func() {
				keys := frontmatterKeys(t, readFile(t, filepath.Join(dir, "a.md")))
				So(keys["color"], ShouldEqual, "blue")
				So(keys["permissionMode"], ShouldEqual, "plan")
			})
		})

		Convey("When invalid values are written", func() {
			So(surface.Write(t.Context(), kind.Items{"a.md": invalid}), ShouldBeNil)

			Convey("Then the invalid fields are dropped", func() {
				keys := frontmatterKeys(t, readFile(t, filepath.Join(dir, "a.md")))
				So(keys, ShouldNotContainKey, "color")
				So(keys, ShouldNotContainKey, "permissionMode")
				So(keys, ShouldNotContainKey, "isolation")
			})
		})
	})

	Convey("Given a Claude file with foreign mode and hidden keys", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".claude", "agents")

		writeFile(t, filepath.Join(dir, "b.md"),
			"---\nname: b\ndescription: d\nmode: primary\nhidden: true\n---\nbody\n")

		claude := agent.ClaudeCode(home, home)
		snap := snapshot(t, claude, kind.Subagents)

		Convey("When the canon is written back", func() {
			doc, err := subagent.Parse(snap.Items["b.md"])
			So(err, ShouldBeNil)

			So(surfaceOf(t, claude, kind.Subagents).Write(t.Context(), snap.Items), ShouldBeNil)

			data := readFile(t, filepath.Join(dir, "b.md"))

			Convey("Then mode and hidden stay foreign extras and never reach the canon", func() {
				So(doc.Mode, ShouldEqual, "")
				So(doc.Hidden, ShouldBeNil)
				So(data, ShouldContainSubstring, "mode: primary")
				So(data, ShouldContainSubstring, "hidden: true")
			})
		})
	})
}

func TestSubagentSymlinksAreLeftAlone(t *testing.T) {
	Convey("Given symlinked agent files at the top level and nested", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".claude", "agents")
		foreign := filepath.Join(home, "foreign")

		writeFile(t, filepath.Join(foreign, "linked.md"), "---\nname: linked\ndescription: d\n---\nbody\n")
		writeFile(t, filepath.Join(foreign, "nested.md"), "---\nname: nested\ndescription: d\n---\nbody\n")
		So(os.MkdirAll(filepath.Join(dir, "sub"), 0o750), ShouldBeNil)
		So(os.Symlink(filepath.Join(foreign, "linked.md"), filepath.Join(dir, "linked.md")), ShouldBeNil)
		So(os.Symlink(filepath.Join(foreign, "nested.md"), filepath.Join(dir, "sub", "nested.md")), ShouldBeNil)

		claude := agent.ClaudeCode(home, home)
		surface := surfaceOf(t, claude, kind.Subagents)
		snap := snapshot(t, claude, kind.Subagents)

		Convey("When the surface reads and the canon is written back", func() {
			Convey("Then symlinked entries are not read and not replaced", func() {
				So(snap.Items, ShouldBeEmpty)
				So(snap.ReadOnly, ShouldContainKey, "linked.md")
				So(snap.ReadOnly, ShouldContainKey, "nested.md")

				desired := kind.Items{
					"linked.md": subagent.Render(subagent.Document{Name: "linked", Description: "d", Body: "new\n"}),
					"nested.md": subagent.Render(subagent.Document{Name: "nested", Description: "d", Body: "new\n"}),
				}

				So(surface.Write(t.Context(), desired), ShouldBeNil)

				top, err := os.Lstat(filepath.Join(dir, "linked.md"))
				So(err, ShouldBeNil)
				So(top.Mode()&os.ModeSymlink != 0, ShouldBeTrue)

				nested, err := os.Lstat(filepath.Join(dir, "sub", "nested.md"))
				So(err, ShouldBeNil)
				So(nested.Mode()&os.ModeSymlink != 0, ShouldBeTrue)

				So(readFile(t, filepath.Join(foreign, "linked.md")), ShouldContainSubstring, "body")
				So(readFile(t, filepath.Join(foreign, "nested.md")), ShouldContainSubstring, "body")
			})
		})
	})
}

func TestSubagentWriteSkipsInvalidItems(t *testing.T) {
	Convey("Given a canon with an invalid name and a body-only file", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".claude", "agents")

		claude := agent.ClaudeCode(home, home)
		surface := surfaceOf(t, claude, kind.Subagents)

		desired := kind.Items{
			"Bad Name.md": subagent.Render(subagent.Document{Name: "Bad Name", Description: "d", Body: "b\n"}),
			"README.md":   []byte("documentation\n"),
		}

		Convey("When the canon is written back", func() {
			Convey("Then both items are skipped without an error", func() {
				So(surface.Write(t.Context(), desired), ShouldBeNil)

				_, err := os.Stat(filepath.Join(dir, "Bad Name.md"))
				So(err, ShouldNotBeNil)

				_, err = os.Stat(filepath.Join(dir, "README.md"))
				So(err, ShouldNotBeNil)
			})
		})
	})
}

func TestClaudeSubagentsBasenameMismatchWarns(t *testing.T) {
	Convey("Given a Claude file whose name differs from its file name", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".claude", "agents")

		writeFile(t, filepath.Join(dir, "foo.md"), "---\nname: bar\ndescription: d\n---\nbody\n")

		snap := snapshot(t, agent.ClaudeCode(home, home), kind.Subagents)

		Convey("When the surface reads the directory", func() {
			Convey("Then the item is keyed by name and a warning explains the mismatch", func() {
				So(snap.Items, ShouldContainKey, "bar.md")
				So(snap.Warnings, ShouldHaveLength, 1)
				So(snap.Warnings[0], ShouldContainSubstring, "does not match")
			})
		})
	})
}

func TestOpenCodeSubagentsPushNarrowsTools(t *testing.T) {
	Convey("Given a canon that first allows Bash and then narrows to Read", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode", "agents")

		opencode := agent.OpenCode(home, home)
		surface := surfaceOf(t, opencode, kind.Subagents)

		bash := kind.Items{
			"x.md": subagent.Render(subagent.Document{Name: "x", Description: "d", Tools: []string{"Bash"}, Body: "b\n"}),
		}
		read := kind.Items{
			"x.md": subagent.Render(subagent.Document{Name: "x", Description: "d", Tools: []string{"Read"}, Body: "b\n"}),
		}

		So(surface.Write(t.Context(), bash), ShouldBeNil)

		Convey("When the canon narrows to Read", func() {
			So(surface.Write(t.Context(), read), ShouldBeNil)

			data := readFile(t, filepath.Join(dir, "x.md"))
			keys := frontmatterKeys(t, data)

			Convey("Then the stale shell allow is gone and the read-back is the canon", func() {
				rules, ok := keys["permissions"].([]any)
				So(ok, ShouldBeTrue)

				for _, entry := range rules {
					rule, ok := entry.(map[string]any)
					So(ok, ShouldBeTrue)
					So(rule["action"], ShouldNotEqual, "shell")
				}

				back := snapshot(t, opencode, kind.Subagents)

				doc, err := subagent.Parse(back.Items["x.md"])
				So(err, ShouldBeNil)
				So(doc.Tools, ShouldResemble, []string{"Read"})
			})
		})
	})
}

func TestOpenCodeSubagentsPushDropsToolsWhenCanonForgets(t *testing.T) {
	Convey("Given a canon that first allows Read and then forgets tools", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode", "agents")

		opencode := agent.OpenCode(home, home)
		surface := surfaceOf(t, opencode, kind.Subagents)

		withTools := kind.Items{
			"x.md": subagent.Render(subagent.Document{Name: "x", Description: "d", Tools: []string{"Read"}, Body: "b\n"}),
		}
		without := kind.Items{
			"x.md": subagent.Render(subagent.Document{Name: "x", Description: "d", Body: "b\n"}),
		}

		So(surface.Write(t.Context(), withTools), ShouldBeNil)

		Convey("When the canon drops its tools", func() {
			So(surface.Write(t.Context(), without), ShouldBeNil)

			data := readFile(t, filepath.Join(dir, "x.md"))
			keys := frontmatterKeys(t, data)

			Convey("Then the permissions key is gone and the write is idempotent", func() {
				So(keys, ShouldNotContainKey, "permissions")

				So(surface.Write(t.Context(), without), ShouldBeNil)
				So(readFile(t, filepath.Join(dir, "x.md")), ShouldEqual, data)
			})
		})
	})
}

func TestOpenCodeSubagentsPushDropsStrayKeys(t *testing.T) {
	Convey("Given an OpenCode agent with keys outside the v2 schema", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode", "agents")

		writeFile(t, filepath.Join(dir, "stray.md"),
			"---\ndescription: Stray\nmode: subagent\nhooks:\n  on-save: echo\nx-custom: 1\n"+
				"tools:\n  write: false\n---\nBody.\n")

		opencode := agent.OpenCode(home, home)
		surface := surfaceOf(t, opencode, kind.Subagents)
		snap := snapshot(t, opencode, kind.Subagents)

		Convey("When the surface reads the file", func() {
			Convey("Then the stray keys are reported", func() {
				So(snap.Warnings, ShouldHaveLength, 1)
				So(snap.Warnings[0], ShouldContainSubstring, "hooks")
				So(snap.Warnings[0], ShouldContainSubstring, "x-custom")
			})
		})

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), snap.Items), ShouldBeNil)

			data := readFile(t, filepath.Join(dir, "stray.md"))
			keys := frontmatterKeys(t, data)

			Convey("Then the strays are gone and the permissions apply", func() {
				So(keys, ShouldNotContainKey, "hooks")
				So(keys, ShouldNotContainKey, "x-custom")
				So(keys, ShouldNotContainKey, "tools")

				rules, ok := keys["permissions"].([]any)
				So(ok, ShouldBeTrue)

				var editDeny bool

				for _, entry := range rules {
					rule, ok := entry.(map[string]any)
					So(ok, ShouldBeTrue)

					if rule["action"] == "edit" && rule["effect"] == "deny" {
						editDeny = true
					}
				}

				So(editDeny, ShouldBeTrue)
			})
		})
	})
}

func TestOpenCodeSubagentsV1MapsOnlyDeny(t *testing.T) {
	Convey("Given a v1 agent with a mixed tools map and permission scalars", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode", "agents")

		writeFile(t, filepath.Join(dir, "mixed.md"),
			"---\ndescription: Mixed\nprompt: Mixed.\ntools:\n  read: false\n  bash: true\n  webfetch: false\n"+
				"permission:\n  grep: allow\n  glob: deny\n---\n")

		opencode := agent.OpenCode(home, home)
		surface := surfaceOf(t, opencode, kind.Subagents)
		snap := snapshot(t, opencode, kind.Subagents)

		Convey("When the file is read and the canon is written back", func() {
			doc, err := subagent.Parse(snap.Items["mixed.md"])
			So(err, ShouldBeNil)

			So(surface.Write(t.Context(), snap.Items), ShouldBeNil)

			data := readFile(t, filepath.Join(dir, "mixed.md"))
			keys := frontmatterKeys(t, data)

			Convey("Then only denials reach the canon and no allowlist appears", func() {
				So(doc.Tools, ShouldBeNil)
				So(doc.DisallowedTools, ShouldResemble, []string{"Read", "Glob", "WebFetch"})

				rules, ok := keys["permissions"].([]any)
				So(ok, ShouldBeTrue)

				for _, entry := range rules {
					rule, ok := entry.(map[string]any)
					So(ok, ShouldBeTrue)
					So(rule["action"], ShouldNotEqual, "shell")
					So(rule["action"], ShouldNotEqual, "*")
				}

				So(keys, ShouldNotContainKey, "tools")
				So(keys, ShouldNotContainKey, "permission")
			})
		})
	})
}

func TestOpenCodeSubagentsDropsForeignEnvelope(t *testing.T) {
	Convey("Given an OpenCode agent with a foreign deny-everything rule", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode", "agents")

		writeFile(t, filepath.Join(dir, "guard.md"),
			"---\ndescription: Guard\nmode: subagent\npermissions:\n  - action: \"*\"\n    resource: \"*\"\n    effect: allow\n---\nBody.\n")

		opencode := agent.OpenCode(home, home)
		surface := surfaceOf(t, opencode, kind.Subagents)
		snap := snapshot(t, opencode, kind.Subagents)

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), snap.Items), ShouldBeNil)

			Convey("Then the codec artifact is not preserved", func() {
				keys := frontmatterKeys(t, readFile(t, filepath.Join(dir, "guard.md")))
				So(keys, ShouldNotContainKey, "permissions")
			})
		})
	})
}

func TestOpenCodeSubagentsMCPToolsRoundTrip(t *testing.T) {
	Convey("Given an OpenCode agent that allows an MCP tool", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode", "agents")

		writeFile(t, filepath.Join(dir, "mcp.md"),
			"---\ndescription: MCP\nmode: subagent\npermissions:\n  - action: slack_post\n    resource: \"*\"\n    effect: allow\n---\nBody.\n")

		opencode := agent.OpenCode(home, home)
		surface := surfaceOf(t, opencode, kind.Subagents)
		snap := snapshot(t, opencode, kind.Subagents)

		Convey("When the file is read", func() {
			Convey("Then the MCP action lifts to the canonical tool", func() {
				doc, err := subagent.Parse(snap.Items["mcp.md"])
				So(err, ShouldBeNil)
				So(doc.Tools, ShouldResemble, []string{"mcp__slack__post"})
			})
		})

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), snap.Items), ShouldBeNil)

			data := readFile(t, filepath.Join(dir, "mcp.md"))
			keys := frontmatterKeys(t, data)

			Convey("Then the allow rule survives and the read-back keeps the tool", func() {
				rules, ok := keys["permissions"].([]any)
				So(ok, ShouldBeTrue)

				var slackAllow bool

				for _, entry := range rules {
					rule, ok := entry.(map[string]any)
					So(ok, ShouldBeTrue)

					if rule["action"] == "slack_post" && rule["effect"] == "allow" {
						slackAllow = true
					}
				}

				So(slackAllow, ShouldBeTrue)

				back := snapshot(t, opencode, kind.Subagents)

				doc, err := subagent.Parse(back.Items["mcp.md"])
				So(err, ShouldBeNil)
				So(doc.Tools, ShouldResemble, []string{"mcp__slack__post"})

				So(surface.Write(t.Context(), snap.Items), ShouldBeNil)
				So(readFile(t, filepath.Join(dir, "mcp.md")), ShouldEqual, data)
			})
		})
	})

	Convey("Given a canon that denies an MCP tool", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode", "agents")

		opencode := agent.OpenCode(home, home)
		surface := surfaceOf(t, opencode, kind.Subagents)

		desired := kind.Items{"mcp.md": subagent.Render(subagent.Document{
			Name: "mcp", Description: "MCP", DisallowedTools: []string{"mcp__slack__post"}, Body: "Body.\n",
		})}

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), desired), ShouldBeNil)

			keys := frontmatterKeys(t, readFile(t, filepath.Join(dir, "mcp.md")))

			Convey("Then the file carries the deny and the read-back keeps it", func() {
				rules, ok := keys["permissions"].([]any)
				So(ok, ShouldBeTrue)

				var slackDeny bool

				for _, entry := range rules {
					rule, ok := entry.(map[string]any)
					So(ok, ShouldBeTrue)

					if rule["action"] == "slack_post" && rule["effect"] == "deny" {
						slackDeny = true
					}
				}

				So(slackDeny, ShouldBeTrue)

				back := snapshot(t, opencode, kind.Subagents)

				doc, err := subagent.Parse(back.Items["mcp.md"])
				So(err, ShouldBeNil)
				So(doc.DisallowedTools, ShouldResemble, []string{"mcp__slack__post"})
			})
		})
	})
}

func TestOpenCodeSubagentsShadowedDuplicateKeepsNoRewriteFlag(t *testing.T) {
	Convey("Given a shadowed legacy duplicate that carries stray keys", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode")

		writeFile(t, filepath.Join(dir, "agents", "dup.md"), "---\ndescription: Winner\nmode: subagent\n---\nWinner.\n")
		writeFile(t, filepath.Join(dir, "agent", "dup.md"),
			"---\ndescription: Legacy\nmode: subagent\nhooks:\n  on-save: echo\n---\nLegacy.\n")

		snap := snapshot(t, agent.OpenCode(home, home), kind.Subagents)

		Convey("When the surface reads the directory", func() {
			Convey("Then the duplicate is reported without a stray rewrite promise", func() {
				So(snap.NeedsRewrite, ShouldBeFalse)
				So(snap.Items, ShouldContainKey, "dup.md")

				joined := strings.Join(snap.Warnings, "\n")
				So(joined, ShouldContainSubstring, "is defined in both")
				So(joined, ShouldNotContainSubstring, "keys outside the host schema")
			})
		})
	})
}

func TestOpenCodeSubagentsKeepsHostExtras(t *testing.T) {
	Convey("Given an OpenCode agent with v2 host extras", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode", "agents")

		writeFile(t, filepath.Join(dir, "extra.md"),
			"---\ndescription: Extra\nmode: subagent\nvariant: fast\nrequest:\n  timeout: 30\ndisabled: false\n---\nBody.\n")

		opencode := agent.OpenCode(home, home)
		surface := surfaceOf(t, opencode, kind.Subagents)
		snap := snapshot(t, opencode, kind.Subagents)

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), snap.Items), ShouldBeNil)

			data := readFile(t, filepath.Join(dir, "extra.md"))

			Convey("Then the extras stay and the write is idempotent", func() {
				So(data, ShouldContainSubstring, "variant: fast")
				So(data, ShouldContainSubstring, "request:")
				So(data, ShouldContainSubstring, "disabled: false")

				So(surface.Write(t.Context(), snap.Items), ShouldBeNil)
				So(readFile(t, filepath.Join(dir, "extra.md")), ShouldEqual, data)
			})
		})
	})
}
