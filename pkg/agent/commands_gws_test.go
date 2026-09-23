package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/command"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func noticesForKind(t *testing.T, a *agent.Agent, k kind.ID, key string, value []byte) []string {
	t.Helper()

	var out []string

	for _, notice := range a.Notices(k, key, value) {
		out = append(out, notice.Message)
	}

	return out
}

func TestClaudeCommandsShift(t *testing.T) {
	Convey("Given a Claude command with 0-based arguments", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".claude", "commands")

		writeFile(t, filepath.Join(dir, "deploy.md"),
			"---\ndescription: Deploys\nargument-hint: <env>\narguments:\n  - FILE\n---\n"+
				"Deploy $0 to $1 with $FILE and $ARGUMENTS[2], see @src/deploy.go and !`git status`.\n")

		claude := agent.ClaudeCode(home, home)
		surface := surfaceOf(t, claude, kind.Commands)
		snap := snapshot(t, claude, kind.Commands)

		Convey("When the surface reads the file", func() {
			Convey("Then the positional placeholders shift to the 1-based canon", func() {
				doc, err := command.Parse(snap.Items["deploy.md"])
				So(err, ShouldBeNil)
				So(doc.Description, ShouldEqual, "Deploys")
				So(doc.ArgumentHint, ShouldEqual, "<env>")
				So(doc.Arguments, ShouldResemble, []string{"FILE"})
				So(doc.Body, ShouldContainSubstring, "Deploy $1 to $2 with $FILE and $3")
				So(doc.Body, ShouldContainSubstring, "@src/deploy.go")
				So(doc.Body, ShouldContainSubstring, "!`git status`")
			})
		})

		Convey("When the canon uses a named placeholder Claude cannot declare", func() {
			desired := kind.Items{"undeclared.md": command.Render(command.Document{
				Name: "undeclared", Description: "d", Body: "Use $FILE now.\n",
			})}

			notes := noticesForKind(t, claude, kind.Commands, "undeclared.md", desired["undeclared.md"])

			Convey("Then the doctor reports the literal placeholder", func() {
				So(strings.Join(notes, "\n"), ShouldContainSubstring, "arguments does not declare it")
			})
		})

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), snap.Items), ShouldBeNil)

			data := readFile(t, filepath.Join(dir, "deploy.md"))

			Convey("Then the file gets 0-based placeholders again and stays stable", func() {
				So(data, ShouldContainSubstring, "Deploy $0 to $1 with $FILE and $2")
				So(data, ShouldContainSubstring, "argument-hint: <env>")
				So(data, ShouldContainSubstring, "arguments:")

				So(surface.Write(t.Context(), snap.Items), ShouldBeNil)
				So(readFile(t, filepath.Join(dir, "deploy.md")), ShouldEqual, data)
			})
		})
	})
}

func TestClaudeCommandsSkipsDefaults(t *testing.T) {
	Convey("Given a canon whose template uses ${1:-def}", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".claude", "commands")

		claude := agent.ClaudeCode(home, home)
		surface := surfaceOf(t, claude, kind.Commands)

		desired := kind.Items{"pick.md": command.Render(command.Document{
			Name: "pick", Description: "Picks", Body: "Pick ${1:-main}.\n",
		})}

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), desired), ShouldBeNil)

			Convey("Then the command is not written and the doctor explains why", func() {
				_, err := os.Stat(filepath.Join(dir, "pick.md"))
				So(err, ShouldNotBeNil)

				notes := noticesForKind(t, claude, kind.Commands, "pick.md", desired["pick.md"])
				So(strings.Join(notes, "\n"), ShouldContainSubstring, "placeholder claude cannot express")
			})
		})
	})
}

func TestOpenCodeCommandsLegacyDir(t *testing.T) {
	Convey("Given a legacy OpenCode command directory", t, func() {
		home := t.TempDir()
		configDir := filepath.Join(home, ".config", "opencode")

		writeFile(t, filepath.Join(configDir, "commands", "ship.md"), "---\ndescription: Ships\n---\nShip $1.\n")
		writeFile(t, filepath.Join(configDir, "command", "old.md"), "---\ndescription: Old\n---\nOld $1.\n")

		snap := snapshot(t, agent.OpenCode(home, home), kind.Commands)

		Convey("When the surface reads the directories", func() {
			Convey("Then both the plural and the legacy directory are read", func() {
				So(snap.Items, ShouldContainKey, "ship.md")
				So(snap.Items, ShouldContainKey, "old.md")
			})
		})
	})
}

func TestOpenCodeCommandsDialect(t *testing.T) {
	Convey("Given an OpenCode command with 1-based arguments", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode", "commands")

		writeFile(t, filepath.Join(dir, "review.md"),
			"---\ndescription: Reviews\nagent: build\n---\nReview $1 at @src/main.go.\n")

		opencode := agent.OpenCode(home, home)
		surface := surfaceOf(t, opencode, kind.Commands)
		snap := snapshot(t, opencode, kind.Commands)

		Convey("When the surface reads the file", func() {
			Convey("Then the positional argument stays 1-based and the host extra is kept", func() {
				doc, err := command.Parse(snap.Items["review.md"])
				So(err, ShouldBeNil)
				So(doc.Body, ShouldEqual, "Review $1 at @src/main.go.\n")
			})
		})

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), snap.Items), ShouldBeNil)

			Convey("Then the file keeps its agent extra and the literal file reference", func() {
				data := readFile(t, filepath.Join(dir, "review.md"))
				So(data, ShouldContainSubstring, "agent: build")
				So(data, ShouldContainSubstring, "@src/main.go")

				So(surface.Write(t.Context(), snap.Items), ShouldBeNil)
				So(readFile(t, filepath.Join(dir, "review.md")), ShouldEqual, data)
			})
		})

		Convey("When an OpenCode file carries canonical keys the host does not define", func() {
			writeFile(t, filepath.Join(dir, "extra.md"),
				"---\ndescription: d\nargument-hint: <x>\narguments:\n  - FILE\ndisable-model-invocation: true\n---\nBody $1.\n")

			snap := snapshot(t, opencode, kind.Commands)

			doc, err := command.Parse(snap.Items["extra.md"])
			So(err, ShouldBeNil)

			Convey("Then they stay host extras and never enter the canon", func() {
				So(doc.ArgumentHint, ShouldEqual, "")
				So(doc.Arguments, ShouldBeNil)
				So(doc.DisableModelInvocation, ShouldBeNil)

				So(surface.Write(t.Context(), snap.Items), ShouldBeNil)

				data := readFile(t, filepath.Join(dir, "extra.md"))
				So(data, ShouldContainSubstring, "argument-hint: <x>")
				So(data, ShouldContainSubstring, "disable-model-invocation: true")
			})
		})

		Convey("When a canon uses named arguments", func() {
			desired := kind.Items{"named.md": command.Render(command.Document{
				Name: "named", Description: "d", Body: "Use $FILE now.\n",
			})}

			So(surface.Write(t.Context(), desired), ShouldBeNil)

			Convey("Then the command is skipped with a doctor note", func() {
				_, err := os.Stat(filepath.Join(dir, "named.md"))
				So(err, ShouldNotBeNil)

				notes := noticesForKind(t, opencode, kind.Commands, "named.md", desired["named.md"])
				So(strings.Join(notes, "\n"), ShouldContainSubstring, "placeholder opencode cannot express")
			})
		})
	})
}

func TestGeminiCommandsRoundTrip(t *testing.T) {
	Convey("Given a Gemini command TOML", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".gemini", "commands")

		writeFile(t, filepath.Join(dir, "deploy.toml"), `# deploys the app
prompt = """
Deploy {{args}} with !{git status} and read @{README.md}.
"""
description = "Deploys"

[meta]
x = 1
`)

		gemini := agent.GeminiCLI(home, home)
		surface := surfaceOf(t, gemini, kind.Commands)
		snap := snapshot(t, gemini, kind.Commands)

		Convey("When the surface reads the file", func() {
			Convey("Then the Gemini placeholders lift to the canon", func() {
				doc, err := command.Parse(snap.Items["deploy.md"])
				So(err, ShouldBeNil)
				So(doc.Description, ShouldEqual, "Deploys")
				So(doc.Body, ShouldContainSubstring, "Deploy $ARGUMENTS with !`git status` and read @README.md.")
			})
		})

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), snap.Items), ShouldBeNil)

			data := readFile(t, filepath.Join(dir, "deploy.toml"))

			Convey("Then the TOML keeps its comment, table and Gemini syntax", func() {
				So(data, ShouldContainSubstring, "# deploys the app")
				So(data, ShouldContainSubstring, "[meta]")
				So(data, ShouldContainSubstring, "{{args}}")
				So(data, ShouldContainSubstring, "!{git status}")
				So(data, ShouldContainSubstring, "@{README.md}")

				So(surface.Write(t.Context(), snap.Items), ShouldBeNil)
				So(readFile(t, filepath.Join(dir, "deploy.toml")), ShouldEqual, data)
			})
		})

		Convey("When a canon uses positional arguments", func() {
			desired := kind.Items{"pos.md": command.Render(command.Document{
				Name: "pos", Description: "d", Body: "Do $1 now.\n",
			})}

			So(surface.Write(t.Context(), desired), ShouldBeNil)

			Convey("Then no TOML is written and the doctor explains why", func() {
				_, err := os.Stat(filepath.Join(dir, "pos.toml"))
				So(err, ShouldNotBeNil)

				notes := noticesForKind(t, gemini, kind.Commands, "pos.md", desired["pos.md"])
				So(strings.Join(notes, "\n"), ShouldContainSubstring, "placeholder gemini cannot express")
			})
		})
	})
}

func TestPiCommandsDialect(t *testing.T) {
	Convey("Given a Pi prompt template with defaults and a literal shell line", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".pi", "agent", "prompts")

		writeFile(t, filepath.Join(dir, "ship.md"),
			"---\ndescription: Ships\nargument-hint: <env>\n---\nShip ${1:-main} with $@.\n")

		pi := agent.Pi(home, home)
		surface := surfaceOf(t, pi, kind.Commands)
		snap := snapshot(t, pi, kind.Commands)

		Convey("When the surface reads the file", func() {
			Convey("Then the all-args alias lifts and the default stays verbatim", func() {
				doc, err := command.Parse(snap.Items["ship.md"])
				So(err, ShouldBeNil)
				So(doc.Body, ShouldEqual, "Ship ${1:-main} with $ARGUMENTS.\n")
				So(doc.ArgumentHint, ShouldEqual, "<env>")
			})
		})

		Convey("When the canon carries a shell block", func() {
			desired := kind.Items{"log.md": command.Render(command.Document{
				Name: "log", Description: "d", Body: "Logs: !`git log`\n",
			})}

			So(surface.Write(t.Context(), desired), ShouldBeNil)

			Convey("Then the text stays literal with a doctor note", func() {
				data := readFile(t, filepath.Join(dir, "log.md"))
				So(data, ShouldContainSubstring, "Logs: !`git log`")

				notes := noticesForKind(t, pi, kind.Commands, "log.md", desired["log.md"])
				So(strings.Join(notes, "\n"), ShouldContainSubstring, "shell blocks are not expanded here")
			})
		})
	})
}

func TestCodexCommandsArePullOnly(t *testing.T) {
	Convey("Given a deprecated Codex prompt", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".codex", "prompts")

		writeFile(t, filepath.Join(dir, "ship.md"),
			"---\ndescription: Ships\nargument-hint: <env>\n---\nShip $1 with $FILE and $$HOME.\n")

		codex := agent.Codex(home, home)
		surface := surfaceOf(t, codex, kind.Commands)
		snap := snapshot(t, codex, kind.Commands)

		Convey("When the surface reads the file", func() {
			Convey("Then the prompts dialect lifts verbatim and the mode stays pull", func() {
				doc, err := command.Parse(snap.Items["ship.md"])
				So(err, ShouldBeNil)
				So(doc.Body, ShouldEqual, "Ship $1 with $FILE and $$HOME.\n")
				So(surface.Traits().DefaultMode, ShouldEqual, config.ModePull)
				So(surface.Traits().Note, ShouldContainSubstring, "deprecated")
			})
		})

		Convey("When the canon carries a placeholder codex cannot express", func() {
			desired := kind.Items{"pos.md": command.Render(command.Document{
				Name: "pos", Description: "d", Body: "Pick ${1:-main}.\n",
			})}

			Convey("Then the pull-only host reports no expressiveness notes", func() {
				So(noticesForKind(t, codex, kind.Commands, "pos.md", desired["pos.md"]), ShouldBeEmpty)
			})
		})
	})
}

func TestKiloCommandsDirs(t *testing.T) {
	Convey("Given Kilo commands in the plural and legacy directories", t, func() {
		home := t.TempDir()
		configDir := filepath.Join(home, ".config", "kilo")

		writeFile(t, filepath.Join(configDir, "commands", "ship.md"), "---\ndescription: Ships\n---\nShip $1.\n")
		writeFile(t, filepath.Join(configDir, "command", "old.md"), "---\ndescription: Old\n---\nOld $1.\n")
		writeFile(t, filepath.Join(configDir, "commands", "team", "nested.md"), "---\ndescription: Nested\n---\nNested.\n")

		kilo := agent.Kilo(home, home)
		snap := snapshot(t, kilo, kind.Commands)

		Convey("When the surface reads the directories", func() {
			Convey("Then both are read and the nested id is skipped with a warning", func() {
				So(snap.Items, ShouldContainKey, "ship.md")
				So(snap.Items, ShouldContainKey, "old.md")
				So(snap.Items, ShouldNotContainKey, "nested.md")
				So(strings.Join(snap.Warnings, "\n"), ShouldContainSubstring, "nested command id")
			})
		})
	})
}

func TestCommandsPullNotes(t *testing.T) {
	Convey("Given a Pi template with literal shell text", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".pi", "agent", "prompts")

		writeFile(t, filepath.Join(dir, "log.md"), "---\ndescription: d\n---\nLogs: !`git log`\n")

		pi := agent.Pi(home, home)
		snap := snapshot(t, pi, kind.Commands)

		Convey("When the surface reads the file", func() {
			Convey("Then the literal shell text is reported", func() {
				So(strings.Join(snap.Warnings, "\n"), ShouldContainSubstring, "does not expand shell blocks")
			})
		})
	})

	Convey("Given an OpenCode template with a literal $NAME", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "opencode", "commands")

		writeFile(t, filepath.Join(dir, "named.md"), "---\ndescription: d\n---\nUse $FILE now.\n")

		snap := snapshot(t, agent.OpenCode(home, home), kind.Commands)

		Convey("When the surface reads the file", func() {
			Convey("Then the literal named placeholder is reported", func() {
				So(strings.Join(snap.Warnings, "\n"), ShouldContainSubstring, "does not expand $NAME")
			})
		})
	})

	Convey("Given a Pi template with a foreign all-args spelling", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".pi", "agent", "prompts")

		writeFile(t, filepath.Join(dir, "args.md"), "---\ndescription: d\n---\nUse {{args}} now.\n")

		pi := agent.Pi(home, home)
		snap := snapshot(t, pi, kind.Commands)

		Convey("When the surface reads the file", func() {
			Convey("Then the spelling stays literal and the note names it", func() {
				doc, err := command.Parse(snap.Items["args.md"])
				So(err, ShouldBeNil)
				So(doc.Body, ShouldEqual, "Use {{args}} now.\n")

				So(strings.Join(snap.Warnings, "\n"), ShouldContainSubstring, "does not expand {{args}}")
			})
		})
	})

	Convey("Given a canon with a foreign all-args spelling", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".claude", "commands")

		claude := agent.ClaudeCode(home, home)
		surface := surfaceOf(t, claude, kind.Commands)

		desired := kind.Items{"foreign.md": command.Render(command.Document{
			Name: "foreign", Description: "d", Body: "Use {{args}} now.\n",
		})}

		Convey("When the canon is written back", func() {
			So(surface.Write(t.Context(), desired), ShouldBeNil)

			Convey("Then the spelling stays literal with a doctor note", func() {
				So(readFile(t, filepath.Join(dir, "foreign.md")), ShouldContainSubstring, "Use {{args}} now.")

				notes := noticesForKind(t, claude, kind.Commands, "foreign.md", desired["foreign.md"])
				So(strings.Join(notes, "\n"), ShouldContainSubstring, "does not expand {{args}}")
			})
		})
	})

	Convey("Given a Gemini template with a literal $1", t, func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".gemini", "commands")

		writeFile(t, filepath.Join(dir, "pos.toml"), "prompt = \"\"\"\nDo $1 now.\n\"\"\"\n")

		snap := snapshot(t, agent.GeminiCLI(home, home), kind.Commands)

		Convey("When the surface reads the file", func() {
			Convey("Then the literal positional placeholder is reported", func() {
				So(strings.Join(snap.Warnings, "\n"), ShouldContainSubstring, "does not expand $N")
			})
		})
	})
}
