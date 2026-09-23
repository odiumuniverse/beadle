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

func TestCursorSubagentsRoundTrip(t *testing.T) {
	Convey("Given a Cursor subagent with a tool restriction", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".cursor", "agents")

		writeFile(t, filepath.Join(dir, "reviewer.md"),
			"---\nname: reviewer\ndescription: Reviews code\nmodel: inherit\nreadonly: true\n"+
				"is_background: true\nx-cursor: keep\n---\nReview code.\n")

		cursor := agent.Cursor(home, home)
		surface := surfaceOf(t, cursor, kind.Subagents)
		snap := snapshot(t, cursor, kind.Subagents)

		Convey("When the surface reads the file", func() {
			Convey("Then readonly lifts to the write-class deny", func() {
				doc, err := subagent.Parse(snap.Items["reviewer.md"])
				So(err, ShouldBeNil)
				So(doc.Model, ShouldEqual, "inherit")
				So(doc.Background, ShouldNotBeNil)
				So(*doc.Background, ShouldBeTrue)
				So(doc.DisallowedTools, ShouldResemble, []string{"Write", "Edit", "NotebookEdit"})
			})
		})

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), snap.Items), ShouldBeNil)

			data := readFile(t, filepath.Join(dir, "reviewer.md"))
			keys := frontmatterKeys(t, data)

			Convey("Then the file keeps readonly and its extras, and stays stable", func() {
				So(keys["readonly"], ShouldEqual, true)
				So(data, ShouldContainSubstring, "x-cursor: keep")

				So(surface.Write(t.Context(), snap.Items), ShouldBeNil)
				So(readFile(t, filepath.Join(dir, "reviewer.md")), ShouldEqual, data)
			})
		})
	})

	Convey("Given a Cursor subagent with a .mdc extension", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".cursor", "agents")

		writeFile(t, filepath.Join(dir, "helper.mdc"), "---\ndescription: Helps\nreadonly: false\n---\nHelp.\n")

		snap := snapshot(t, agent.Cursor(home, home), kind.Subagents)

		Convey("When the surface reads the directory", func() {
			Convey("Then the file name defaults the identity and readonly false adds nothing", func() {
				So(snap.Items, ShouldContainKey, "helper.md")

				doc, err := subagent.Parse(snap.Items["helper.md"])
				So(err, ShouldBeNil)
				So(doc.Name, ShouldEqual, "helper")
				So(doc.DisallowedTools, ShouldBeNil)
			})
		})
	})
}

func TestGeminiSubagentsRoundTrip(t *testing.T) {
	Convey("Given a Gemini subagent that allows tools", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".gemini", "agents")

		writeFile(t, filepath.Join(dir, "planner.md"),
			"---\nname: planner\ndescription: Plans work\ntools:\n  - read_file\n  - run_shell_command\n"+
				"  - mcp_slack_post\nmodel: gemini-2.5-pro\nmax_turns: 12\ntemperature: 0.4\ntimeout_mins: 5\n---\nPlan work.\n")

		gemini := agent.GeminiCLI(home, home)
		surface := surfaceOf(t, gemini, kind.Subagents)
		snap := snapshot(t, gemini, kind.Subagents)

		Convey("When the surface reads the file", func() {
			Convey("Then the tools lift to canonical names", func() {
				doc, err := subagent.Parse(snap.Items["planner.md"])
				So(err, ShouldBeNil)
				So(doc.Tools, ShouldResemble, []string{"Read", "Bash", "mcp__slack__post"})
				So(doc.Model, ShouldEqual, "gemini-2.5-pro")
				So(doc.MaxTurns, ShouldEqual, 12)
			})
		})

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), snap.Items), ShouldBeNil)

			data := readFile(t, filepath.Join(dir, "planner.md"))
			keys := frontmatterKeys(t, data)

			Convey("Then the tools render back and the host extras survive", func() {
				So(keys["tools"], ShouldResemble, []any{"read_file", "run_shell_command", "mcp_slack_post"})
				So(data, ShouldContainSubstring, "temperature: 0.4")
				So(data, ShouldContainSubstring, "timeout_mins: 5")

				So(surface.Write(t.Context(), snap.Items), ShouldBeNil)
				So(readFile(t, filepath.Join(dir, "planner.md")), ShouldEqual, data)
			})
		})
	})

	Convey("Given a remote Gemini subagent and a canon with a Claude model", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".gemini", "agents")

		writeFile(t, filepath.Join(dir, "remote.md"),
			"---\nname: remote\ndescription: Remote\nkind: remote\n---\nRemote.\n")

		gemini := agent.GeminiCLI(home, home)
		surface := surfaceOf(t, gemini, kind.Subagents)

		Convey("When the surface reads the directory", func() {
			Convey("Then the remote agent is skipped with a warning", func() {
				snap := snapshot(t, gemini, kind.Subagents)
				So(snap.Items, ShouldBeEmpty)
				So(strings.Join(snap.Warnings, "\n"), ShouldContainSubstring, "remote subagents are not synced")
			})
		})

		Convey("When a canon model does not map", func() {
			desired := kind.Items{"x.md": subagent.Render(subagent.Document{
				Name: "x", Description: "d", Model: "haiku", Body: "b\n",
			})}

			So(surface.Write(t.Context(), desired), ShouldBeNil)

			Convey("Then the file falls back to inherit without guessing", func() {
				data := readFile(t, filepath.Join(dir, "x.md"))
				So(data, ShouldContainSubstring, "model: inherit")
				So(data, ShouldNotContainSubstring, "haiku")
			})
		})
	})
}

func TestAntigravitySubagentsRoundTrip(t *testing.T) {
	Convey("Given an Antigravity subagent in the nested layout", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".gemini", "config", "agents")

		writeFile(t, filepath.Join(dir, "runner", "agent.md"),
			"---\nname: runner\ndescription: Runs commands\ntools:\n  - view_file\n  - run_command\n"+
				"  - replace_file_content\nmainAgent: false\nsubagent: true\nmodel: pro\n"+
				"commandExecutionPolicy: sandbox\n---\nRun.\n")

		agy := agent.AntigravityCLI(home, home)
		surface := surfaceOf(t, agy, kind.Subagents)
		snap := snapshot(t, agy, kind.Subagents)

		Convey("When the surface reads the nested file", func() {
			Convey("Then the identity comes from the name, the roles map and no mismatch is reported", func() {
				doc, err := subagent.Parse(snap.Items["runner.md"])
				So(err, ShouldBeNil)
				So(doc.Mode, ShouldEqual, "subagent")
				So(doc.Tools, ShouldResemble, []string{"Read", "Edit", "Bash"})
				So(snap.Warnings, ShouldBeEmpty)
			})
		})

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), snap.Items), ShouldBeNil)

			data := readFile(t, filepath.Join(dir, "runner", "agent.md"))

			Convey("Then the roles and the host extras stay and the file is stable", func() {
				So(data, ShouldContainSubstring, "mainAgent: false")
				So(data, ShouldContainSubstring, "subagent: true")
				So(data, ShouldContainSubstring, "commandExecutionPolicy: sandbox")

				So(surface.Write(t.Context(), snap.Items), ShouldBeNil)
				So(readFile(t, filepath.Join(dir, "runner", "agent.md")), ShouldEqual, data)
			})
		})
	})

	Convey("Given a canon without a mode", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".gemini", "config", "agents")

		agy := agent.AntigravityCLI(home, home)
		surface := surfaceOf(t, agy, kind.Subagents)

		desired := kind.Items{"x.md": subagent.Render(subagent.Document{
			Name: "x", Description: "d", Tools: []string{"Read", "Grep"}, Body: "b\n",
		})}

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), desired), ShouldBeNil)

			Convey("Then the host gets a subagent with the mapped tools", func() {
				data := readFile(t, filepath.Join(dir, "x.md"))
				So(data, ShouldContainSubstring, "mainAgent: false")
				So(data, ShouldContainSubstring, "subagent: true")
				So(data, ShouldContainSubstring, "view_file")
				So(data, ShouldContainSubstring, "grep_search")
			})
		})
	})

	Convey("Given a primary canon", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".gemini", "config", "agents")

		agy := agent.AntigravityCLI(home, home)
		surface := surfaceOf(t, agy, kind.Subagents)

		desired := kind.Items{"p.md": subagent.Render(subagent.Document{
			Name: "p", Description: "d", Mode: "primary", Body: "b\n",
		})}

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), desired), ShouldBeNil)

			Convey("Then the role flags mark it as a main agent only", func() {
				data := readFile(t, filepath.Join(dir, "p.md"))
				So(data, ShouldContainSubstring, "mainAgent: true")
				So(data, ShouldContainSubstring, "subagent: false")
			})
		})
	})
}

func TestKiloSubagentsRoundTrip(t *testing.T) {
	Convey("Given Kilo agents in the plural, legacy and mode directories", t, func() {
		home := t.TempDir()
		configDir := filepath.Join(home, ".config", "kilo")

		writeFile(t, filepath.Join(configDir, "agents", "reviewer.md"),
			"---\ndescription: Reviews\nmode: subagent\n---\nReview.\n")
		writeFile(t, filepath.Join(configDir, "agent", "legacy.md"),
			"---\ndescription: Legacy\nmode: subagent\nx-host: keep\n---\nLegacy.\n")
		writeFile(t, filepath.Join(configDir, "modes", "build.md"),
			"---\ndescription: Build\n---\nBuild.\n")

		kilo := agent.Kilo(home, home)
		surface := surfaceOf(t, kilo, kind.Subagents)
		snap := snapshot(t, kilo, kind.Subagents)

		Convey("When the surface reads the directories", func() {
			Convey("Then the mode directory forces primary and the extras stay", func() {
				So(snap.Items, ShouldContainKey, "reviewer.md")
				So(snap.Items, ShouldContainKey, "legacy.md")
				So(snap.Items, ShouldContainKey, "build.md")

				doc, err := subagent.Parse(snap.Items["build.md"])
				So(err, ShouldBeNil)
				So(doc.Mode, ShouldEqual, "primary")

				legacy, err := subagent.Parse(snap.Items["legacy.md"])
				So(err, ShouldBeNil)
				So(legacy.Name, ShouldEqual, "legacy")
			})
		})

		Convey("When the canon is written back twice", func() {
			So(surface.Write(t.Context(), snap.Items), ShouldBeNil)

			first := map[string]string{
				"reviewer": readFile(t, filepath.Join(configDir, "agents", "reviewer.md")),
				"legacy":   readFile(t, filepath.Join(configDir, "agent", "legacy.md")),
				"build":    readFile(t, filepath.Join(configDir, "modes", "build.md")),
			}

			So(surface.Write(t.Context(), snap.Items), ShouldBeNil)

			Convey("Then every file is written in place and the second write is byte-stable", func() {
				So(readFile(t, filepath.Join(configDir, "agents", "reviewer.md")), ShouldEqual, first["reviewer"])
				So(readFile(t, filepath.Join(configDir, "agent", "legacy.md")), ShouldEqual, first["legacy"])
				So(readFile(t, filepath.Join(configDir, "modes", "build.md")), ShouldEqual, first["build"])
				So(first["build"], ShouldContainSubstring, "mode: primary")
				// The strict v2 allowlist migrates the legacy file: the
				// keys outside the host schema are dropped by design.
				So(first["legacy"], ShouldNotContainSubstring, "x-host")
				So(first["legacy"], ShouldContainSubstring, "mode: subagent")
			})
		})

		Convey("When a canon with tools is written back", func() {
			desired := kind.Items{"fresh.md": subagent.Render(subagent.Document{
				Name: "fresh", Description: "d", Tools: []string{"Read"}, Body: "b\n",
			})}

			So(surface.Write(t.Context(), desired), ShouldBeNil)

			Convey("Then the strict v2 allowlist holds and the file is not legacy", func() {
				data := readFile(t, filepath.Join(configDir, "agents", "fresh.md"))
				So(data, ShouldNotContainSubstring, "name:")
				So(data, ShouldNotContainSubstring, "tools:")
				So(data, ShouldContainSubstring, "permissions:")
				So(data, ShouldContainSubstring, "mode: subagent")
			})
		})
	})

	Convey("Given a nested Kilo agent", t, func() {
		home := t.TempDir()
		configDir := filepath.Join(home, ".config", "kilo")

		writeFile(t, filepath.Join(configDir, "agents", "team", "reviewer.md"),
			"---\ndescription: Reviews\nmode: subagent\n---\nReview.\n")

		snap := snapshot(t, agent.Kilo(home, home), kind.Subagents)

		Convey("When the surface reads the directory", func() {
			Convey("Then the nested id is skipped with a warning", func() {
				So(snap.Items, ShouldBeEmpty)
				So(strings.Join(snap.Warnings, "\n"), ShouldContainSubstring, "nested subagent id")
			})
		})
	})
}

func TestCodexSubagentsRoundTrip(t *testing.T) {
	Convey("Given a Codex agent TOML with comments and foreign keys", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".codex", "agents")

		writeFile(t, filepath.Join(dir, "pr_explorer.toml"), `# explores pull requests
name = "pr_explorer" # the identity
description = "Explores pull requests"
developer_instructions = """
Look at the diff.
"""
model = "gpt-5-codex"

model_reasoning_effort = "high"

[mcp_servers.github]
command = "gh-mcp"
`)

		codex := agent.Codex(home, home)
		surface := surfaceOf(t, codex, kind.Subagents)
		snap := snapshot(t, codex, kind.Subagents)

		Convey("When the surface reads the file", func() {
			Convey("Then the TOML keys lift to the canon", func() {
				doc, err := subagent.Parse(snap.Items["pr_explorer.md"])
				So(err, ShouldBeNil)
				So(doc.Name, ShouldEqual, "pr_explorer")
				So(doc.Description, ShouldEqual, "Explores pull requests")
				So(doc.Model, ShouldEqual, "gpt-5-codex")
				So(doc.Body, ShouldEqual, "Look at the diff.\n")
			})
		})

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), snap.Items), ShouldBeNil)

			data := readFile(t, filepath.Join(dir, "pr_explorer.toml"))

			Convey("Then comments, foreign keys and tables stay and the write is stable", func() {
				So(data, ShouldContainSubstring, "# explores pull requests")
				So(data, ShouldContainSubstring, "# the identity")
				So(data, ShouldContainSubstring, `model_reasoning_effort = "high"`)
				So(data, ShouldContainSubstring, "[mcp_servers.github]")

				So(surface.Write(t.Context(), snap.Items), ShouldBeNil)
				So(readFile(t, filepath.Join(dir, "pr_explorer.toml")), ShouldEqual, data)
			})
		})
	})

	Convey("Given a canon with tools and a broken TOML file", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".codex", "agents")

		writeFile(t, filepath.Join(dir, "broken.toml"), "name = \"broken\"\ndescription = [unclosed\n")

		codex := agent.Codex(home, home)
		surface := surfaceOf(t, codex, kind.Subagents)

		Convey("When the surface reads the directory", func() {
			Convey("Then the invalid file is reported and not read", func() {
				snap := snapshot(t, codex, kind.Subagents)
				So(snap.Items, ShouldBeEmpty)
				So(strings.Join(snap.Warnings, "\n"), ShouldContainSubstring, "parse toml")
			})
		})

		Convey("When a readonly canon is written back", func() {
			desired := kind.Items{"guard.md": subagent.Render(subagent.Document{
				Name: "guard", Description: "d", Tools: []string{"Read"}, Body: "b\n",
			})}

			So(surface.Write(t.Context(), desired), ShouldBeNil)

			Convey("Then the readonly canon becomes sandbox_mode and tools stay out", func() {
				data := readFile(t, filepath.Join(dir, "guard.toml"))
				So(data, ShouldContainSubstring, `sandbox_mode = "read-only"`)
				So(data, ShouldNotContainSubstring, "tools")
				So(data, ShouldContainSubstring, "developer_instructions")
			})
		})

		Convey("When the broken file is in the canon", func() {
			desired := kind.Items{"broken.md": subagent.Render(subagent.Document{
				Name: "broken", Description: "d", Body: "b\n",
			})}

			before := readFile(t, filepath.Join(dir, "broken.toml"))

			Convey("Then the write is refused without touching the file", func() {
				err := surface.Write(t.Context(), desired)
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, "parse toml")
				So(readFile(t, filepath.Join(dir, "broken.toml")), ShouldEqual, before)
			})
		})
	})
}

func TestCodexSubagentsSpanEdit(t *testing.T) {
	Convey("Given a minimal Codex agent TOML", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".codex", "agents")

		writeFile(t, filepath.Join(dir, "mini.toml"), "name = \"mini\"\n")

		codex := agent.Codex(home, home)
		surface := surfaceOf(t, codex, kind.Subagents)

		desired := kind.Items{"mini.md": subagent.Render(subagent.Document{
			Name: "mini", Description: "Small", Model: "gpt-5-codex", Body: "Do the thing.\n",
		})}

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), desired), ShouldBeNil)

			data := readFile(t, filepath.Join(dir, "mini.toml"))

			Convey("Then the missing keys are inserted and the body is a multiline string", func() {
				So(data, ShouldContainSubstring, `name = "mini"`)
				So(data, ShouldContainSubstring, `description = "Small"`)
				So(data, ShouldContainSubstring, `model = "gpt-5-codex"`)
				So(data, ShouldContainSubstring, "developer_instructions")

				back := snapshot(t, codex, kind.Subagents)

				doc, err := subagent.Parse(back.Items["mini.md"])
				So(err, ShouldBeNil)
				So(doc.Body, ShouldEqual, "Do the thing.\n")

				So(surface.Write(t.Context(), back.Items), ShouldBeNil)
				So(readFile(t, filepath.Join(dir, "mini.toml")), ShouldEqual, data)
			})
		})
	})

	Convey("Given a file with a stale read-only sandbox", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".codex", "agents")

		writeFile(t, filepath.Join(dir, "writer.toml"),
			"name = \"writer\"\ndescription = \"Writes\"\nsandbox_mode = \"read-only\"\n")

		codex := agent.Codex(home, home)
		surface := surfaceOf(t, codex, kind.Subagents)

		desired := kind.Items{"writer.md": subagent.Render(subagent.Document{
			Name: "writer", Description: "Writes", Tools: []string{"Read", "Write"}, Body: "Write.\n",
		})}

		Convey("When a writable canon is written back", func() {
			So(surface.Write(t.Context(), desired), ShouldBeNil)

			data := readFile(t, filepath.Join(dir, "writer.toml"))

			Convey("Then the stale read-only sandbox is gone", func() {
				So(data, ShouldNotContainSubstring, "sandbox_mode")
			})
		})
	})

	Convey("Given a file whose own model the canon does not have", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".codex", "agents")

		writeFile(t, filepath.Join(dir, "plain.toml"),
			"name = \"plain\"\ndescription = \"Plain\"\nmodel = \"gpt-5-codex\"\nsandbox_mode = \"workspace-write\"\n")

		codex := agent.Codex(home, home)
		surface := surfaceOf(t, codex, kind.Subagents)

		desired := kind.Items{"plain.md": subagent.Render(subagent.Document{
			Name: "plain", Description: "Plain", Body: "Plain.\n",
		})}

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), desired), ShouldBeNil)

			data := readFile(t, filepath.Join(dir, "plain.toml"))

			Convey("Then the canon owns the model while a non-read-only sandbox stays", func() {
				So(data, ShouldNotContainSubstring, "model =")
				So(data, ShouldContainSubstring, `sandbox_mode = "workspace-write"`)
			})
		})
	})

	Convey("Given a canon without a description", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".codex", "agents")

		writeFile(t, filepath.Join(dir, "keep.toml"), "name = \"keep\"\ndescription = \"Required by codex\"\n")

		codex := agent.Codex(home, home)
		surface := surfaceOf(t, codex, kind.Subagents)

		desired := kind.Items{"keep.md": subagent.Render(subagent.Document{
			Name: "keep", Body: "Keep.\n",
		})}

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), desired), ShouldBeNil)

			Convey("Then the required description stays", func() {
				So(readFile(t, filepath.Join(dir, "keep.toml")), ShouldContainSubstring, `description = "Required by codex"`)
			})
		})
	})
}

func TestCursorSubagentsReadOnlyDerive(t *testing.T) {
	Convey("Given canons with and without write-class tools", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".cursor", "agents")

		cursor := agent.Cursor(home, home)
		surface := surfaceOf(t, cursor, kind.Subagents)

		items := kind.Items{
			"reader.md": subagent.Render(subagent.Document{
				Name: "reader", Description: "d", Tools: []string{"Read", "Grep"}, Body: "b\n",
			}),
			"writer.md": subagent.Render(subagent.Document{
				Name: "writer", Description: "d", Tools: []string{"Read", "Write"}, Body: "b\n",
			}),
		}

		Convey("When both canons are written back", func() {
			So(surface.Write(t.Context(), items), ShouldBeNil)

			Convey("Then only the write-class-free canon is read-only and it lifts back", func() {
				So(readFile(t, filepath.Join(dir, "reader.md")), ShouldContainSubstring, "readonly: true")
				So(readFile(t, filepath.Join(dir, "writer.md")), ShouldNotContainSubstring, "readonly")

				snap := snapshot(t, cursor, kind.Subagents)

				doc, err := subagent.Parse(snap.Items["reader.md"])
				So(err, ShouldBeNil)
				So(doc.DisallowedTools, ShouldResemble, []string{"Write", "Edit", "NotebookEdit"})
			})
		})
	})
}

func TestCodexSubagentsInsertsBeforeTables(t *testing.T) {
	Convey("Given a Codex TOML that starts with a table", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".codex", "agents")

		writeFile(t, filepath.Join(dir, "tables.toml"), "[mcp_servers.github]\ncommand = \"gh-mcp\"\n")

		codex := agent.Codex(home, home)
		surface := surfaceOf(t, codex, kind.Subagents)

		desired := kind.Items{"tables.md": subagent.Render(subagent.Document{
			Name: "tables", Description: "d", Body: "b\n",
		})}

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), desired), ShouldBeNil)

			data := readFile(t, filepath.Join(dir, "tables.toml"))

			Convey("Then the keys land before the table and the table stays", func() {
				So(strings.Index(data, `name = "tables"`), ShouldBeLessThan, strings.Index(data, "[mcp_servers.github]"))
				So(data, ShouldContainSubstring, "[mcp_servers.github]")
				So(data, ShouldContainSubstring, `command = "gh-mcp"`)
			})
		})
	})
}

func TestGeminiSubagentsKeepsToolWildcards(t *testing.T) {
	Convey("Given a Gemini agent with a wildcard next to a mapped tool", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".gemini", "agents")

		writeFile(t, filepath.Join(dir, "mixed.md"),
			"---\nname: mixed\ndescription: Mixed\ntools:\n  - read_file\n  - mcp_*\n  - custom_tool\n---\nBody.\n")

		gemini := agent.GeminiCLI(home, home)
		surface := surfaceOf(t, gemini, kind.Subagents)
		snap := snapshot(t, gemini, kind.Subagents)

		Convey("When the canon is written back", func() {
			doc, err := subagent.Parse(snap.Items["mixed.md"])
			So(err, ShouldBeNil)
			So(doc.Tools, ShouldResemble, []string{"Read"})

			So(surface.Write(t.Context(), snap.Items), ShouldBeNil)

			data := readFile(t, filepath.Join(dir, "mixed.md"))

			Convey("Then the mappable tool stays canonical and the wildcards survive", func() {
				So(data, ShouldContainSubstring, "read_file")
				So(data, ShouldContainSubstring, "mcp_*")
				So(data, ShouldContainSubstring, "custom_tool")
			})
		})
	})
}

func TestCodexSubagentsHandlesBOM(t *testing.T) {
	Convey("Given a Codex TOML with a UTF-8 BOM", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".codex", "agents")

		writeFile(t, filepath.Join(dir, "bom.toml"), "\ufeffname = \"bom\"\ndescription = \"Old\"\n")

		codex := agent.Codex(home, home)
		surface := surfaceOf(t, codex, kind.Subagents)
		snap := snapshot(t, codex, kind.Subagents)

		Convey("When the canon is written back", func() {
			doc, err := subagent.Parse(snap.Items["bom.md"])
			So(err, ShouldBeNil)
			So(doc.Name, ShouldEqual, "bom")
			So(doc.Description, ShouldEqual, "Old")

			updated := kind.Items{"bom.md": subagent.Render(subagent.Document{
				Name: "bom", Description: "New", Body: "b\n",
			})}

			So(surface.Write(t.Context(), updated), ShouldBeNil)

			data := readFile(t, filepath.Join(dir, "bom.toml"))

			Convey("Then the file keeps a single name key and stays valid TOML", func() {
				So(strings.Count(data, "name ="), ShouldEqual, 1)
				So(data, ShouldContainSubstring, "description = \"New\"")

				back := snapshot(t, codex, kind.Subagents)

				doc, err := subagent.Parse(back.Items["bom.md"])
				So(err, ShouldBeNil)
				So(doc.Description, ShouldEqual, "New")
				So(doc.Body, ShouldEqual, "b\n")
			})
		})
	})
}

func TestCodexSubagentsFileNameIsSecondary(t *testing.T) {
	Convey("Given a Codex TOML whose file name differs from its name key", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".codex", "agents")

		writeFile(t, filepath.Join(dir, "pr-explorer.toml"), "name = \"pr_explorer\"\ndescription = \"d\"\n")

		codex := agent.Codex(home, home)
		snap := snapshot(t, codex, kind.Subagents)

		Convey("When the surface reads the file", func() {
			Convey("Then the identity is the name key and no mismatch is reported", func() {
				So(snap.Items, ShouldContainKey, "pr_explorer.md")
				So(snap.Warnings, ShouldBeEmpty)
			})
		})

		Convey("When the canon is written back", func() {
			desired := kind.Items{"pr_explorer.md": subagent.Render(subagent.Document{
				Name: "pr_explorer", Description: "New", Body: "b\n",
			})}

			So(surfaceOf(t, codex, kind.Subagents).Write(t.Context(), desired), ShouldBeNil)

			Convey("Then the write lands in place", func() {
				So(readFile(t, filepath.Join(dir, "pr-explorer.toml")), ShouldContainSubstring, `description = "New"`)
			})
		})
	})
}

func TestSubagentSymlinkKeysAreCanonical(t *testing.T) {
	Convey("Given a symlinked Cursor .mdc agent", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".cursor", "agents")
		foreign := filepath.Join(home, "foreign")

		writeFile(t, filepath.Join(foreign, "helper.mdc"), "---\ndescription: d\n---\nbody\n")

		So(os.MkdirAll(dir, 0o750), ShouldBeNil)
		So(os.Symlink(filepath.Join(foreign, "helper.mdc"), filepath.Join(dir, "helper.mdc")), ShouldBeNil)

		cursor := agent.Cursor(home, home)
		snap := snapshot(t, cursor, kind.Subagents)

		Convey("When the surface reads the directory", func() {
			Convey("Then the read-only key is the canonical item name and the link survives a write", func() {
				So(snap.Items, ShouldBeEmpty)
				So(snap.ReadOnly, ShouldContainKey, "helper.md")

				desired := kind.Items{"helper.md": subagent.Render(subagent.Document{
					Name: "helper", Description: "d", Body: "new\n",
				})}

				So(surfaceOf(t, cursor, kind.Subagents).Write(t.Context(), desired), ShouldBeNil)

				info, err := os.Lstat(filepath.Join(dir, "helper.mdc"))
				So(err, ShouldBeNil)
				So(info.Mode()&os.ModeSymlink != 0, ShouldBeTrue)
				So(readFile(t, filepath.Join(foreign, "helper.mdc")), ShouldContainSubstring, "body")
			})
		})
	})
}

func TestSubagentExtensionlessSymlinkIsIgnored(t *testing.T) {
	Convey("Given an extensionless symlink next to a canon item", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".claude", "agents")
		foreign := filepath.Join(home, "foreign")

		writeFile(t, filepath.Join(foreign, "linked.md"), "---\nname: linked\ndescription: d\n---\nbody\n")

		So(os.MkdirAll(dir, 0o750), ShouldBeNil)
		So(os.Symlink(filepath.Join(foreign, "linked.md"), filepath.Join(dir, "linked")), ShouldBeNil)

		claude := agent.ClaudeCode(home, home)
		surface := surfaceOf(t, claude, kind.Subagents)
		snap := snapshot(t, claude, kind.Subagents)

		Convey("When the surface reads the directory", func() {
			Convey("Then the foreign link owns no canonical name", func() {
				So(snap.ReadOnly, ShouldBeEmpty)
				So(snap.Unreadable, ShouldBeEmpty)
			})
		})

		Convey("When the canon holds the item", func() {
			desired := kind.Items{"linked.md": subagent.Render(subagent.Document{
				Name: "linked", Description: "d", Body: "new\n",
			})}

			So(surface.Write(t.Context(), desired), ShouldBeNil)

			Convey("Then the item is delivered to its own file and the link stays", func() {
				So(readFile(t, filepath.Join(dir, "linked.md")), ShouldContainSubstring, "new")
				So(readFile(t, filepath.Join(foreign, "linked.md")), ShouldContainSubstring, "body")

				info, err := os.Lstat(filepath.Join(dir, "linked"))
				So(err, ShouldBeNil)
				So(info.Mode()&os.ModeSymlink != 0, ShouldBeTrue)
			})
		})
	})
}
