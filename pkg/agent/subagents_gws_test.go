package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	yaml "go.yaml.in/yaml/v3"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/subagent"
)

func TestClaudeSubagentsReadAndWrite(t *testing.T) {
	Convey("Given a Claude agents directory with a nested file and a foreign key", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".claude", "agents")

		writeFile(t, filepath.Join(dir, "nested", "reviewer.md"),
			"---\nname: reviewer\ndescription: Reviews\ntools: [Read, Grep]\nmodel: haiku\nx-claude: keep\n---\nReview code.\n")
		writeFile(t, filepath.Join(dir, "notes.md"), "no frontmatter\n")

		claude := agent.ClaudeCode(home, home)
		snap := snapshot(t, claude, kind.Subagents)

		Convey("When the surface reads the directory", func() {
			Convey("Then the nested file is one canonical item", func() {
				So(snap.Items, ShouldContainKey, "reviewer.md")
				So(snap.Warnings, ShouldBeEmpty)

				doc, err := subagent.Parse(snap.Items["reviewer.md"])
				So(err, ShouldBeNil)
				So(doc.Name, ShouldEqual, "reviewer")
				So(doc.Tools, ShouldResemble, []string{"Read", "Grep"})
				So(doc.Body, ShouldEqual, "Review code.\n")
			})
		})

		Convey("When the canon is written back", func() {
			desired := kind.Items{
				"reviewer.md": subagent.Render(subagent.Document{
					Name: "reviewer", Description: "Updated", Tools: []string{"Read"}, Body: "Updated body.\n",
				}),
			}

			So(surfaceOf(t, claude, kind.Subagents).Write(t.Context(), desired), ShouldBeNil)

			Convey("Then the file is updated in place and keeps the foreign key", func() {
				data := readFile(t, filepath.Join(dir, "nested", "reviewer.md"))
				So(data, ShouldContainSubstring, "description: Updated")
				So(data, ShouldContainSubstring, "x-claude: keep")
				So(data, ShouldContainSubstring, "Updated body.")

				_, err := os.Stat(filepath.Join(dir, "reviewer.md"))
				So(err, ShouldNotBeNil)
			})

			Convey("Then a second write is a no-op", func() {
				before := readFile(t, filepath.Join(dir, "nested", "reviewer.md"))
				So(surfaceOf(t, claude, kind.Subagents).Write(t.Context(), desired), ShouldBeNil)
				So(readFile(t, filepath.Join(dir, "nested", "reviewer.md")), ShouldEqual, before)
			})
		})
	})

	Convey("Given two Claude files with the same name", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".claude", "agents")

		writeFile(t, filepath.Join(dir, "a.md"), "---\nname: same\ndescription: first\n---\nbody\n")
		writeFile(t, filepath.Join(dir, "b.md"), "---\nname: same\ndescription: second\n---\nbody\n")

		snap := snapshot(t, agent.ClaudeCode(home, home), kind.Subagents)

		Convey("When the surface reads the directory", func() {
			Convey("Then one definition wins and the duplicate is reported", func() {
				So(snap.Items, ShouldContainKey, "same.md")

				found := false

				for _, warning := range snap.Warnings {
					if strings.Contains(warning, "is defined in both") {
						found = true
					}
				}

				So(found, ShouldBeTrue)
			})
		})
	})
}

//nolint:funlen // one scenario per host codec keeps the fixture setup together
func TestOpenCodeSubagentsCodec(t *testing.T) {
	Convey("Given an OpenCode v2 agent file with permissions", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode", "agents")

		writeFile(t, filepath.Join(dir, "reviewer.md"),
			"---\ndescription: Reviews\nmode: primary\nsteps: 7\npermissions:\n  - action: read\n    resource: \"*\"\n    effect: allow\n  - action: edit\n    resource: \"*\"\n    effect: deny\n---\nReview.\n")

		opencode := agent.OpenCode(home, home)
		snap := snapshot(t, opencode, kind.Subagents)

		Convey("When the surface reads the file", func() {
			Convey("Then the name comes from the file name and the rules map to tools", func() {
				doc, err := subagent.Parse(snap.Items["reviewer.md"])
				So(err, ShouldBeNil)
				So(doc.Name, ShouldEqual, "reviewer")
				So(doc.Mode, ShouldEqual, "primary")
				So(doc.MaxTurns, ShouldEqual, 7)
				So(doc.Tools, ShouldResemble, []string{"Read"})
				So(doc.DisallowedTools, ShouldResemble, []string{"Write", "Edit", "NotebookEdit"})
			})
		})

		Convey("When the canon is written back", func() {
			desired := kind.Items{
				"reviewer.md": subagent.Render(subagent.Document{
					Name: "reviewer", Description: "Reviews", Tools: []string{"Read", "Grep"}, Body: "Review.\n",
				}),
			}

			So(surfaceOf(t, opencode, kind.Subagents).Write(t.Context(), desired), ShouldBeNil)

			Convey("Then the file holds only v2 keys and derives the read-only deny", func() {
				data := readFile(t, filepath.Join(dir, "reviewer.md"))
				keys := frontmatterKeys(t, data)

				So(keys, ShouldNotContainKey, "name")
				So(keys, ShouldNotContainKey, "tools")
				So(keys, ShouldContainKey, "mode")
				So(keys, ShouldContainKey, "permissions")

				rules, ok := keys["permissions"].([]any)
				So(ok, ShouldBeTrue)
				So(rules, ShouldHaveLength, 4)

				first, ok := rules[0].(map[string]any)
				So(ok, ShouldBeTrue)
				So(first["action"], ShouldEqual, "*")
				So(first["effect"], ShouldEqual, "deny")

				doc, err := subagent.Parse(snapshot(t, opencode, kind.Subagents).Items["reviewer.md"])
				So(err, ShouldBeNil)
				So(doc.Tools, ShouldResemble, []string{"Read", "Grep"})
				So(doc.DisallowedTools, ShouldResemble, []string{"Write", "Edit", "NotebookEdit"})
				So(doc.Mode, ShouldEqual, "subagent")
			})

			Convey("Then a second write is a no-op", func() {
				before := readFile(t, filepath.Join(dir, "reviewer.md"))
				So(surfaceOf(t, opencode, kind.Subagents).Write(t.Context(), desired), ShouldBeNil)
				So(readFile(t, filepath.Join(dir, "reviewer.md")), ShouldEqual, before)
			})
		})
	})

	Convey("Given an OpenCode agent in the legacy singular directory", t, func() {
		home := t.TempDir()
		legacy := filepath.Join(home, ".config", "opencode", "agent")

		writeFile(t, filepath.Join(legacy, "legacy.md"), "---\ndescription: Legacy\nmode: subagent\n---\nLegacy body.\n")

		opencode := agent.OpenCode(home, home)
		snap := snapshot(t, opencode, kind.Subagents)

		Convey("When the canon is written back", func() {
			desired := kind.Items{
				"legacy.md": subagent.Render(subagent.Document{
					Name: "legacy", Description: "Legacy updated", Mode: "subagent", Body: "Legacy body.\n",
				}),
			}

			So(surfaceOf(t, opencode, kind.Subagents).Write(t.Context(), desired), ShouldBeNil)

			Convey("Then the file stays in place and no plural duplicate appears", func() {
				So(readFile(t, filepath.Join(legacy, "legacy.md")), ShouldContainSubstring, "Legacy updated")
				_, err := os.Stat(filepath.Join(home, ".config", "opencode", "agents", "legacy.md"))
				So(err, ShouldNotBeNil)
			})
		})

		Convey("Then the item is readable", func() {
			So(snap.Items, ShouldContainKey, "legacy.md")
		})
	})

	Convey("Given a nested OpenCode agent id", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode", "agents")

		writeFile(t, filepath.Join(dir, "team", "reviewer.md"), "---\ndescription: Nested\nmode: subagent\n---\nbody\n")

		snap := snapshot(t, agent.OpenCode(home, home), kind.Subagents)

		Convey("When the surface reads the directory", func() {
			Convey("Then the nested id is skipped with a warning", func() {
				So(snap.Items, ShouldBeEmpty)
				So(snap.Warnings, ShouldHaveLength, 1)
				So(snap.Warnings[0], ShouldContainSubstring, "nested subagent id")
			})
		})
	})

	Convey("Given a legacy v1 OpenCode agent", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode", "agents")

		writeFile(t, filepath.Join(dir, "old.md"),
			"---\ndescription: Old\nprompt: Old prompt.\ntools:\n  write: false\n  bash: true\n---\n")

		snap := snapshot(t, agent.OpenCode(home, home), kind.Subagents)

		Convey("When the surface reads the file", func() {
			Convey("Then the prompt becomes the body and only the disabled tools map over", func() {
				doc, err := subagent.Parse(snap.Items["old.md"])
				So(err, ShouldBeNil)
				So(doc.Body, ShouldEqual, "Old prompt.\n")
				So(doc.Tools, ShouldBeNil)
				So(doc.DisallowedTools, ShouldResemble, []string{"Write", "Edit", "NotebookEdit"})
			})
		})
	})
}

func frontmatterKeys(t *testing.T, data string) map[string]any {
	t.Helper()

	front, _, ok := subagent.Split([]byte(data))
	if !ok {
		t.Fatalf("no frontmatter in %q", data)
	}

	keys := map[string]any{}
	if err := yaml.Unmarshal(front, &keys); err != nil {
		t.Fatalf("parse frontmatter: %v", err)
	}

	return keys
}
