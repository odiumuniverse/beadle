package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/subagent"
)

func TestSubagentsEngineRoundTrip(t *testing.T) {
	Convey("Given a Claude subagent and an OpenCode subagent", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		claudeFile := filepath.Join(f.home, ".claude", "agents", "reviewer.md")
		ocFile := filepath.Join(f.home, ".config", "opencode", "agents", "planner.md")

		write(t, claudeFile,
			"---\nname: reviewer\ndescription: Reviews code\ntools: [Read, Grep]\nmodel: haiku\nx-claude: keep\n---\nReview code.\n")
		write(t, ocFile, "---\ndescription: Plans work\nmode: primary\nsteps: 7\n---\nPlan work.\n")

		report := f.sync(t)

		Convey("Then both agents reach the canon without warnings", func() {
			So(read(t, filepath.Join(f.vault.SubagentsDir(), "reviewer.md")), ShouldContainSubstring, "name: reviewer")
			So(read(t, filepath.Join(f.vault.SubagentsDir(), "planner.md")), ShouldContainSubstring, "description: Plans work")
			So(report.Warnings, ShouldBeEmpty)
		})

		Convey("Then the Claude agent reaches OpenCode with a strict v2 frontmatter", func() {
			data := read(t, filepath.Join(f.home, ".config", "opencode", "agents", "reviewer.md"))
			So(data, ShouldContainSubstring, "mode: subagent")
			So(data, ShouldContainSubstring, "permissions:")
			So(data, ShouldNotContainSubstring, "name:")
			So(data, ShouldNotContainSubstring, "tools:")
			So(data, ShouldNotContainSubstring, "haiku")
		})

		Convey("Then the OpenCode agent reaches Claude with the canonical fields", func() {
			data := read(t, filepath.Join(f.home, ".claude", "agents", "planner.md"))
			So(data, ShouldContainSubstring, "name: planner")
			So(data, ShouldContainSubstring, "maxTurns: 7")
			So(data, ShouldNotContainSubstring, "mode:")
		})

		Convey("Then the Claude file keeps its foreign key", func() {
			So(read(t, claudeFile), ShouldContainSubstring, "x-claude: keep")
		})

		Convey("When sync runs again", func() {
			claudeBefore := read(t, claudeFile)
			ocBefore := read(t, ocFile)
			plannerBefore := read(t, filepath.Join(f.home, ".claude", "agents", "planner.md"))

			report2 := f.sync(t)

			Convey("Then every file is byte-identical and the vault does not change", func() {
				So(report2.VaultChanged(), ShouldBeFalse)
				So(read(t, claudeFile), ShouldEqual, claudeBefore)
				So(read(t, ocFile), ShouldEqual, ocBefore)
				So(read(t, filepath.Join(f.home, ".claude", "agents", "planner.md")), ShouldEqual, plannerBefore)
			})
		})
	})
}

func TestSubagentsEngineSkipsNestedAndWarns(t *testing.T) {
	Convey("Given a nested OpenCode subagent id", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, filepath.Join(f.home, ".config", "opencode", "agents", "team", "nested.md"),
			"---\ndescription: Nested\nmode: subagent\n---\nbody\n")

		report := f.sync(t)

		Convey("When the engine syncs", func() {
			Convey("Then the nested id is skipped with a warning and stays out of the canon", func() {
				found := false

				for _, warning := range report.Kind(kind.Subagents).Warnings {
					if strings.Contains(warning, "nested subagent id") {
						found = true
					}
				}

				So(found, ShouldBeTrue)

				entries, err := os.ReadDir(f.vault.SubagentsDir())
				So(err, ShouldBeNil)
				So(entries, ShouldBeEmpty)
			})
		})
	})
}

func TestSubagentsEngineIgnoresVaultDocumentation(t *testing.T) {
	Convey("Given a README next to the subagent canon", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		So(os.MkdirAll(f.vault.SubagentsDir(), 0o700), ShouldBeNil)
		write(t, filepath.Join(f.vault.SubagentsDir(), "README.md"), "docs\n")

		report := f.sync(t)

		Convey("When the engine syncs", func() {
			Convey("Then the documentation file is ignored and left alone", func() {
				_, err := os.Stat(filepath.Join(f.home, ".claude", "agents", "README.md"))
				So(err, ShouldNotBeNil)
				So(read(t, filepath.Join(f.vault.SubagentsDir(), "README.md")), ShouldEqual, "docs\n")

				for _, result := range report.Kind(kind.Subagents).Agents {
					So(result.Action, ShouldEqual, engine.ActionNoop)
					So(result.Note, ShouldEqual, "")
				}
			})
		})
	})
}

func TestSubagentsEngineDropsToolsWhenCanonForgets(t *testing.T) {
	Convey("Given a canon whose tools are removed after a first sync", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		canon := filepath.Join(f.vault.SubagentsDir(), "x.md")

		write(t, canon, string(subagent.Render(subagent.Document{
			Name: "x", Description: "d", Tools: []string{"Read"}, Body: "b\n",
		})))
		f.sync(t)

		write(t, canon, string(subagent.Render(subagent.Document{
			Name: "x", Description: "d", Body: "b\n",
		})))

		report := f.sync(t)

		Convey("When the engine syncs again", func() {
			Convey("Then the hosts converge without notes and the stale allowlist is gone", func() {
				for _, result := range report.Kind(kind.Subagents).Agents {
					So(result.Note, ShouldEqual, "")
				}

				data := read(t, filepath.Join(f.home, ".config", "opencode", "agents", "x.md"))
				So(data, ShouldNotContainSubstring, "permissions")
			})
		})
	})
}

func TestSubagentsEngineRewritesStrayKeys(t *testing.T) {
	Convey("Given an OpenCode file with keys outside the host schema", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		ocFile := filepath.Join(f.home, ".config", "opencode", "agents", "stray.md")

		write(t, ocFile, "---\ndescription: Stray\nmode: subagent\nhooks:\n  on-save: echo\n"+
			"permissions:\n  - action: shell\n    resource: \"*\"\n    effect: deny\n---\nBody.\n")

		report := f.sync(t)

		Convey("When the engine syncs", func() {
			Convey("Then the stray key is gone, the rule survives and the next sync is quiet", func() {
				So(strings.Join(report.Kind(kind.Subagents).Warnings, "\n"), ShouldContainSubstring, "keys outside the host schema")

				data := read(t, ocFile)
				So(data, ShouldNotContainSubstring, "hooks")
				So(data, ShouldContainSubstring, "action: shell")
				So(data, ShouldContainSubstring, "effect: deny")

				report2 := f.sync(t)

				So(report2.Kind(kind.Subagents).Warnings, ShouldBeEmpty)

				for _, result := range report2.Kind(kind.Subagents).Agents {
					So(result.Action, ShouldEqual, engine.ActionNoop)
					So(result.Note, ShouldEqual, "")
				}
			})
		})
	})
}

func TestSubagentsEngineKeepsMCPTools(t *testing.T) {
	Convey("Given a canon that allows an MCP tool", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		canon := filepath.Join(f.vault.SubagentsDir(), "mcp.md")

		write(t, canon, string(subagent.Render(subagent.Document{
			Name: "mcp", Description: "MCP", Tools: []string{"mcp__slack__post"}, Body: "Body.\n",
		})))

		report := f.sync(t)

		Convey("When the engine syncs", func() {
			Convey("Then the OpenCode file carries the allow and the next sync is a no-op", func() {
				data := read(t, filepath.Join(f.home, ".config", "opencode", "agents", "mcp.md"))
				So(data, ShouldContainSubstring, "action: slack_post")
				So(data, ShouldContainSubstring, "effect: allow")

				for _, result := range report.Kind(kind.Subagents).Agents {
					So(result.Note, ShouldEqual, "")
				}

				report2 := f.sync(t)

				for _, result := range report2.Kind(kind.Subagents).Agents {
					So(result.Action, ShouldEqual, engine.ActionNoop)
					So(result.Note, ShouldEqual, "")
				}

				So(read(t, canon), ShouldContainSubstring, "mcp__slack__post")
			})
		})
	})
}

func TestSubagentsEngineShadowedDuplicateDoesNotPush(t *testing.T) {
	Convey("Given a legacy OpenCode duplicate with stray keys behind the winner", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		winner := filepath.Join(f.home, ".config", "opencode", "agents", "dup.md")
		legacy := filepath.Join(f.home, ".config", "opencode", "agent", "dup.md")

		write(t, winner, "---\ndescription: Winner\nmode: subagent\n---\nWinner.\n")
		write(t, legacy, "---\ndescription: Legacy\nmode: subagent\nhooks:\n  on-save: echo\n---\nLegacy.\n")

		before, err := os.Stat(winner)
		So(err, ShouldBeNil)

		report := f.sync(t)
		report2 := f.sync(t)

		Convey("When the engine syncs twice", func() {
			Convey("Then the winner is not rewritten, the legacy file is untouched and only the duplicate is reported", func() {
				for _, report := range []*engine.Report{report, report2} {
					for _, result := range report.Kind(kind.Subagents).Agents {
						if result.Agent == agent.OpenCodeID {
							So(result.Action, ShouldEqual, engine.ActionNoop)
							So(result.Note, ShouldEqual, "")
						}
					}
				}

				joined := strings.Join(report2.Kind(kind.Subagents).Warnings, "\n")
				So(joined, ShouldContainSubstring, "is defined in both")
				So(joined, ShouldNotContainSubstring, "keys outside the host schema")

				after, err := os.Stat(winner)
				So(err, ShouldBeNil)
				So(after.ModTime().Equal(before.ModTime()), ShouldBeTrue)

				So(read(t, legacy), ShouldContainSubstring, "hooks")
			})
		})
	})
}
