package engine_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/subagent"
)

func TestSubagentsEngineCodexRoundTrip(t *testing.T) {
	Convey("Given a Claude subagent and a Codex agent", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.CodexID)
		f.enableAgent(t, agent.KiloID)

		So(os.MkdirAll(filepath.Join(f.home, ".config", "kilo"), 0o750), ShouldBeNil)

		write(t, filepath.Join(f.home, ".claude", "agents", "reviewer.md"),
			"---\nname: reviewer\ndescription: Reviews code\ntools: [Read, Grep]\n---\nReview code.\n")
		write(t, filepath.Join(f.home, ".codex", "agents", "worker.toml"),
			"name = \"worker\"\ndescription = \"Works\"\ndeveloper_instructions = \"\"\"\nWork.\n\"\"\"\n# keep\nx-extra = true\n")

		f.sync(t)

		Convey("Then every host carries both agents and the next sync is a no-op", func() {
			codexReviewer := read(t, filepath.Join(f.home, ".codex", "agents", "reviewer.toml"))
			So(codexReviewer, ShouldContainSubstring, `sandbox_mode = "read-only"`)

			claudeWorker := read(t, filepath.Join(f.home, ".claude", "agents", "worker.md"))
			So(claudeWorker, ShouldContainSubstring, "name: worker")
			So(claudeWorker, ShouldContainSubstring, "Work.")

			ocReviewer := read(t, filepath.Join(f.home, ".config", "opencode", "agents", "reviewer.md"))
			So(ocReviewer, ShouldContainSubstring, "permissions:")

			kiloReviewer := read(t, filepath.Join(f.home, ".config", "kilo", "agents", "reviewer.md"))
			So(kiloReviewer, ShouldContainSubstring, "permissions:")
			So(kiloReviewer, ShouldNotContainSubstring, "tools:")

			report := f.sync(t)

			for _, result := range report.Kind(kind.Subagents).Agents {
				So(result.Action, ShouldEqual, engine.ActionNoop)
				So(result.Note, ShouldEqual, "")
			}

			So(read(t, filepath.Join(f.home, ".codex", "agents", "reviewer.toml")), ShouldEqual, codexReviewer)
		})

		Convey("Then the Codex file keeps its comment and foreign key", func() {
			codexWorker := read(t, filepath.Join(f.home, ".codex", "agents", "worker.toml"))
			So(codexWorker, ShouldContainSubstring, "# keep")
			So(codexWorker, ShouldContainSubstring, "x-extra = true")
		})
	})
}

func TestDoctorSubagentIssues(t *testing.T) {
	Convey("Given a subagent canon with an unmappable tool and a secret-like line", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, filepath.Join(f.vault.SubagentsDir(), "x.md"), string(subagent.Render(subagent.Document{
			Name: "x", Description: "d", Tools: []string{"TodoWrite"},
			Body: "token = \"sk-proj-abcdefghijklmnopqrstuvwxyz\"\n",
		})))

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("Then the unmappable tool is an Info and the secret a Warn", func() {
			So(hasIssue(issues, engine.SeverityInfo, `unmappable tool "TodoWrite" for opencode`), ShouldBeTrue)
			So(hasIssue(issues, engine.SeverityWarn, "secret-like line"), ShouldBeTrue)
		})
	})

	Convey("Given a legacy OpenCode agent file", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, filepath.Join(f.home, ".config", "opencode", "agents", "old.md"),
			"---\ndescription: Old\nprompt: Old prompt.\ntools:\n  write: false\n---\n")

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("Then the legacy keys are an Info note", func() {
			So(hasIssue(issues, engine.SeverityInfo, "keys outside the host schema"), ShouldBeTrue)
		})
	})
}

func TestDoctorOpenCodeInlineAgents(t *testing.T) {
	Convey("Given an OpenCode config with inline agents", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, filepath.Join(f.home, ".config", "opencode", "opencode.jsonc"),
			"{\n  // inline agents are not managed\n  \"agents\": {\"inline\": {\"description\": \"x\"}},\n  \"mcp\": {}\n}\n")

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When doctor runs", func() {
			Convey("Then the inline agents are an Info", func() {
				So(hasIssue(issues, engine.SeverityInfo, "declares inline agents: inline"), ShouldBeTrue)
			})
		})
	})
}

func TestDoctorOpenCodeInlineAgentsFiltersFiles(t *testing.T) {
	Convey("Given an inline agent that already exists as a file and a second config", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, filepath.Join(f.home, ".config", "opencode", "opencode.jsonc"), "{\"mcp\": {}}\n")
		write(t, filepath.Join(f.home, ".config", "opencode", "opencode.json"),
			"{\"agents\": {\"known\": {\"description\": \"x\"}, \"unknown\": {\"description\": \"y\"}}}\n")
		write(t, filepath.Join(f.home, ".config", "opencode", "agents", "known.md"),
			"---\ndescription: Known\nmode: subagent\n---\nBody.\n")

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When doctor runs", func() {
			Convey("Then only the file-less inline agent is reported", func() {
				So(hasIssue(issues, engine.SeverityInfo, "declares inline agents: unknown"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityInfo, "declares inline agents: known"), ShouldBeFalse)
			})
		})
	})
}
