package agent_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/subagent"
)

func TestOmpSubagents(t *testing.T) {
	Convey("Given an omp adapter", t, func() {
		home := t.TempDir()

		withoutOmpEnv(t)

		a := agent.Omp(home, t.TempDir())
		dir := filepath.Join(home, ".omp", "agent", "agents")
		surface := surfaceOf(t, a, kind.Subagents)

		Convey("Then the task agents live in the agent directory", func() {
			So(surface.Path(), ShouldEqual, dir)
			So(surface.Traits().DefaultMode, ShouldEqual, config.ModeSync)
			So(surface.Traits().Creatable, ShouldBeTrue)
		})

		Convey("When a file is missing a required field", func() {
			writeFile(t, filepath.Join(dir, "no-desc.md"), "---\nname: no-desc\n---\nBody.\n")
			writeFile(t, filepath.Join(dir, "alpha.md"), "---\nname: alpha\ndescription: d\n---\nBody.\n")

			Convey("Then omp skips it and the canon keeps only the complete file", func() {
				snap := snapshot(t, a, kind.Subagents)
				So(snap.Items, ShouldNotContainKey, "no-desc.md")
				So(snap.Items, ShouldContainKey, "alpha.md")
			})
		})

		Convey("When a file carries omp-only keys", func() {
			path := filepath.Join(dir, "alpha.md")
			writeFile(t, path, "---\nname: alpha\ndescription: d\nspawns: \"*\"\n"+
				"autoloadSkills:\n  - beta\nthinking-level: medium\n---\nBody.\n")

			Convey("Then a rewrite keeps them and the canon does not adopt them", func() {
				snap := snapshot(t, a, kind.Subagents)
				So(string(snap.Items["alpha.md"]), ShouldNotContainSubstring, "spawns")

				So(surface.Write(t.Context(), kind.Items{"alpha.md": snap.Items["alpha.md"]}), ShouldBeNil)

				out := readFile(t, path)
				So(out, ShouldContainSubstring, `spawns: "*"`)
				So(out, ShouldContainSubstring, "autoloadSkills")
				So(out, ShouldContainSubstring, "thinking-level: medium")

				before := out

				So(surface.Write(t.Context(), kind.Items{"alpha.md": snap.Items["alpha.md"]}), ShouldBeNil)
				So(readFile(t, path), ShouldEqual, before)
			})
		})

		Convey("When the canon sets keys omp does not define", func() {
			value := subagent.Render(subagent.Document{
				Name: "alpha", Description: "d", Mode: "primary", PermissionMode: "plan",
				Color: "red", MaxTurns: 5, Skills: []string{"beta"}, Body: "Body.\n",
			})

			Convey("Then they do not reach the file and the doctor reports them", func() {
				So(surface.Write(t.Context(), kind.Items{"alpha.md": value}), ShouldBeNil)

				out := readFile(t, filepath.Join(dir, "alpha.md"))
				So(out, ShouldContainSubstring, "description: d")
				So(out, ShouldContainSubstring, "Body.")
				So(out, ShouldNotContainSubstring, "mode:")
				So(out, ShouldNotContainSubstring, "permissionMode")
				So(out, ShouldNotContainSubstring, "color:")
				So(out, ShouldNotContainSubstring, "maxTurns")
				So(out, ShouldNotContainSubstring, "skills")

				notice := noticeText(noticesFor(a, "alpha.md", value))
				So(notice, ShouldContainSubstring, "mode is not expressible for omp")
				So(notice, ShouldContainSubstring, "permissionMode is not expressible for omp")
				So(notice, ShouldContainSubstring, "color is not expressible for omp")
				So(notice, ShouldContainSubstring, "skills is not expressible for omp")
			})
		})

		Convey("When the canon model is a Claude alias", func() {
			value := subagent.Render(subagent.Document{Name: "alpha", Description: "d", Model: "haiku", Body: "Body.\n"})

			Convey("Then no model key is written and the doctor explains", func() {
				So(surface.Write(t.Context(), kind.Items{"alpha.md": value}), ShouldBeNil)
				So(readFile(t, filepath.Join(dir, "alpha.md")), ShouldNotContainSubstring, "model")

				notice := noticeText(noticesFor(a, "alpha.md", value))
				So(notice, ShouldContainSubstring, "does not map to omp")
			})
		})

		Convey("When a canon subagent has no description", func() {
			value := subagent.Render(subagent.Document{Name: "alpha", Body: "Body.\n"})

			Convey("Then nothing is written and the doctor reports the refusal", func() {
				So(surface.Write(t.Context(), kind.Items{"alpha.md": value}), ShouldBeNil)

				_, statErr := os.Stat(filepath.Join(dir, "alpha.md"))
				So(os.IsNotExist(statErr), ShouldBeTrue)

				notice := noticeText(noticesFor(a, "alpha.md", value))
				So(notice, ShouldContainSubstring, "omp requires a description")
			})
		})
	})
}

func TestOmpSubagentTools(t *testing.T) {
	Convey("Given an omp adapter", t, func() {
		home := t.TempDir()

		withoutOmpEnv(t)

		a := agent.Omp(home, t.TempDir())
		dir := filepath.Join(home, ".omp", "agent", "agents")
		path := filepath.Join(dir, "alpha.md")
		surface := surfaceOf(t, a, kind.Subagents)

		Convey("When the canon lists canonical tools", func() {
			value := subagent.Render(subagent.Document{
				Name: "alpha", Description: "d", Tools: []string{"Read", "Bash", "AskUserQuestion"}, Body: "b\n",
			})

			Convey("Then they are written in their omp spelling", func() {
				So(surface.Write(t.Context(), kind.Items{"alpha.md": value}), ShouldBeNil)

				out := readFile(t, path)
				So(out, ShouldContainSubstring, "tools: [read, bash, ask]")
				So(out, ShouldNotContainSubstring, "Read")
				So(noticeText(noticesFor(a, "alpha.md", value)), ShouldBeEmpty)
			})
		})

		Convey("When an omp file carries a tool with no canonical equivalent", func() {
			writeFile(t, path, "---\nname: alpha\ndescription: d\ntools:\n  - read\n  - find\n---\nb\n")

			Convey("Then the canon takes the known tool and a rewrite keeps the host one", func() {
				snap := snapshot(t, a, kind.Subagents)

				doc, err := subagent.Parse(snap.Items["alpha.md"])
				So(err, ShouldBeNil)
				So(doc.Tools, ShouldResemble, []string{"Read"})

				So(surface.Write(t.Context(), kind.Items{"alpha.md": snap.Items["alpha.md"]}), ShouldBeNil)

				out := readFile(t, path)
				So(out, ShouldContainSubstring, "tools: [read, find]")
			})
		})

		Convey("When the canon lists a tool omp has no equivalent for", func() {
			value := subagent.Render(subagent.Document{
				Name: "alpha", Description: "d", Tools: []string{"WebFetch"}, Body: "b\n",
			})

			Convey("Then no tool key is written and the doctor reports the loss", func() {
				So(surface.Write(t.Context(), kind.Items{"alpha.md": value}), ShouldBeNil)
				So(readFile(t, path), ShouldNotContainSubstring, "tools")

				notice := noticeText(noticesFor(a, "alpha.md", value))
				So(notice, ShouldContainSubstring, `tool "WebFetch" has no omp equivalent`)
				So(notice, ShouldContainSubstring, "the host keeps all tools (fail-open)")
			})
		})
	})
}
