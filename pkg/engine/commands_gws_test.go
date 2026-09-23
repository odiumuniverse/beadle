package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/command"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func TestCommandsEngineRoundTrip(t *testing.T) {
	Convey("Given a Claude command and a Gemini command", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.GeminiCLIID)

		So(os.MkdirAll(filepath.Join(f.home, ".gemini"), 0o750), ShouldBeNil)

		claudeFile := filepath.Join(f.home, ".claude", "commands", "deploy.md")
		geminiFile := filepath.Join(f.home, ".gemini", "commands", "ship.toml")

		write(t, claudeFile, "---\ndescription: Deploys\n---\nDeploy $ARGUMENTS now.\n")
		write(t, geminiFile, "prompt = \"\"\"\nShip {{args}}.\n\"\"\"\ndescription = \"Ships\"\n")

		report := f.sync(t)

		Convey("Then both hosts carry both commands", func() {
			So(read(t, filepath.Join(f.vault.CommandsDir(), "deploy.md")), ShouldContainSubstring, "Deploy $ARGUMENTS now.")
			So(read(t, filepath.Join(f.vault.CommandsDir(), "ship.md")), ShouldContainSubstring, "Ship $ARGUMENTS.")

			So(read(t, claudeFile), ShouldContainSubstring, "Deploy $ARGUMENTS now.")
			So(read(t, filepath.Join(f.home, ".gemini", "commands", "deploy.toml")), ShouldContainSubstring, "Deploy {{args}} now.")
			So(read(t, geminiFile), ShouldContainSubstring, "Ship {{args}}.")

			So(report.Kind(kind.Commands).Warnings, ShouldBeEmpty)
		})

		Convey("When sync runs again", func() {
			claudeBefore := read(t, claudeFile)
			geminiBefore := read(t, geminiFile)

			report2 := f.sync(t)

			Convey("Then the second sync is a byte-for-byte no-op", func() {
				for _, result := range report2.Kind(kind.Commands).Agents {
					So(result.Action, ShouldEqual, engine.ActionNoop)
					So(result.Note, ShouldEqual, "")
				}

				So(read(t, claudeFile), ShouldEqual, claudeBefore)
				So(read(t, geminiFile), ShouldEqual, geminiBefore)
			})
		})
	})
}

func TestCommandsEngineSkipsInexpressible(t *testing.T) {
	Convey("Given a canon command with a positional argument", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.GeminiCLIID)

		So(os.MkdirAll(filepath.Join(f.home, ".gemini"), 0o750), ShouldBeNil)
		write(t, filepath.Join(f.vault.CommandsDir(), "pos.md"), string(command.Render(command.Document{
			Name: "pos", Description: "d", Body: "Do $1 now.\n",
		})))

		f.sync(t)

		Convey("Then Gemini does not get the file and the doctor explains why", func() {
			_, err := os.Stat(filepath.Join(f.home, ".gemini", "commands", "pos.toml"))
			So(err, ShouldNotBeNil)

			So(read(t, filepath.Join(f.home, ".claude", "commands", "pos.md")), ShouldContainSubstring, "Do $0 now.")

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)
			So(hasIssue(issues, engine.SeverityInfo, "placeholder gemini cannot express"), ShouldBeTrue)
		})
	})
}

func TestCommandsEngineCodexPullOnly(t *testing.T) {
	Convey("Given a deprecated Codex prompt", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.CodexID)
		f.enableAgent(t, agent.GeminiCLIID)

		So(os.MkdirAll(filepath.Join(f.home, ".gemini"), 0o750), ShouldBeNil)

		prompt := filepath.Join(f.home, ".codex", "prompts", "ship.md")
		write(t, prompt, "---\ndescription: Ships\n---\nShip $1 with $FILE.\n")

		f.sync(t)

		Convey("Then the prompt reaches the canon and other hosts, but the file stays", func() {
			So(read(t, filepath.Join(f.vault.CommandsDir(), "ship.md")), ShouldContainSubstring, "Ship $1 with $FILE.")

			// The Codex surface never writes: the file keeps its exact bytes.
			So(read(t, prompt), ShouldEqual, "---\ndescription: Ships\n---\nShip $1 with $FILE.\n")

			Convey("And the doctor marks the prompts deprecated", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityInfo, "codex prompts are deprecated"), ShouldBeTrue)
			})
		})
	})
}

func TestDoctorCommandIssues(t *testing.T) {
	Convey("Given a canon command that collides with a skill and holds a secret", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, filepath.Join(f.vault.CommandsDir(), "review.md"), string(command.Render(command.Document{
			Name: "review", Description: "d", Body: "token = \"sk-proj-abcdefghijklmnopqrstuvwxyz\"\n",
		})))
		write(t, filepath.Join(f.vault.SkillsDir(), "review", "SKILL.md"), "---\ndescription: d\n---\nBody.\n")

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("Then the skill precedence and the secret are warnings", func() {
			So(hasIssue(issues, engine.SeverityWarn, "prefers the skill"), ShouldBeTrue)
			So(hasIssue(issues, engine.SeverityWarn, "secret-like line"), ShouldBeTrue)
		})
	})

	Convey("Given a Cursor commands directory", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		So(os.MkdirAll(filepath.Join(f.home, ".cursor", "commands"), 0o750), ShouldBeNil)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("Then it is reported as unmanaged", func() {
			So(hasIssue(issues, engine.SeverityInfo, "~/.cursor/commands exists"), ShouldBeTrue)
		})
	})
}

func TestCommandsEngineKeepsHostExtras(t *testing.T) {
	Convey("Given an OpenCode command with host extras", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		file := filepath.Join(f.home, ".config", "opencode", "commands", "build.md")
		write(t, file, "---\ndescription: Builds\nagent: build\nsubtask: true\n---\nBuild $1.\n")

		f.sync(t)

		Convey("Then the extras stay in the host file and never reach the canon", func() {
			data := read(t, file)
			So(data, ShouldContainSubstring, "agent: build")
			So(data, ShouldContainSubstring, "subtask: true")

			canon := read(t, filepath.Join(f.vault.CommandsDir(), "build.md"))
			So(canon, ShouldNotContainSubstring, "subtask")
		})
	})
}

func TestCommandsNestedSkipWarns(t *testing.T) {
	Convey("Given a nested OpenCode command", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, filepath.Join(f.home, ".config", "opencode", "commands", "team", "review.md"),
			"---\ndescription: Reviews\n---\nReview.\n")

		report := f.sync(t)

		Convey("Then the nested id is skipped with a warning", func() {
			So(strings.Join(report.Kind(kind.Commands).Warnings, "\n"), ShouldContainSubstring, "nested command id")
			So(read(t, filepath.Join(f.home, ".config", "opencode", "commands", "team", "review.md")), ShouldContainSubstring, "Review.")
		})
	})
}

func TestCommandsEngineKeepsInexpressibleHostFile(t *testing.T) {
	Convey("Given a command delivered to Gemini and one pulled from OpenCode", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.GeminiCLIID)

		So(os.MkdirAll(filepath.Join(f.home, ".gemini"), 0o750), ShouldBeNil)

		geminiFile := filepath.Join(f.home, ".gemini", "commands", "pos.toml")
		ocFile := filepath.Join(f.home, ".config", "opencode", "commands", "named.md")

		write(t, filepath.Join(f.vault.CommandsDir(), "pos.md"), string(command.Render(command.Document{
			Name: "pos", Description: "d", Body: "Do $ARGUMENTS now.\n",
		})))
		write(t, ocFile, "---\ndescription: Named\n---\nUse $FILE now.\n")

		f.sync(t)

		delivered := read(t, geminiFile)
		So(delivered, ShouldContainSubstring, "{{args}}")
		So(read(t, ocFile), ShouldContainSubstring, "$FILE")

		Convey("When the canon gains a placeholder the host cannot express", func() {
			write(t, filepath.Join(f.vault.CommandsDir(), "pos.md"), string(command.Render(command.Document{
				Name: "pos", Description: "d", Body: "Do $1 now.\n",
			})))

			f.sync(t)

			Convey("Then both host files survive untouched", func() {
				So(read(t, geminiFile), ShouldEqual, delivered)
				So(read(t, ocFile), ShouldContainSubstring, "$FILE")
			})
		})
	})
}

func TestCommandsEngineGeminiDescriptionClears(t *testing.T) {
	Convey("Given a command delivered to Gemini with a description", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.GeminiCLIID)

		So(os.MkdirAll(filepath.Join(f.home, ".gemini"), 0o750), ShouldBeNil)

		canon := filepath.Join(f.vault.CommandsDir(), "ship.md")
		file := filepath.Join(f.home, ".gemini", "commands", "ship.toml")

		write(t, canon, string(command.Render(command.Document{
			Name: "ship", Description: "Ships", Body: "Ship $ARGUMENTS.\n",
		})))

		f.sync(t)

		So(read(t, file), ShouldContainSubstring, `description = "Ships"`)

		Convey("When the canon clears the description", func() {
			write(t, canon, string(command.Render(command.Document{
				Name: "ship", Body: "Ship $ARGUMENTS.\n",
			})))

			report := f.sync(t)

			Convey("Then the key is removed and the next sync is clean", func() {
				So(read(t, file), ShouldNotContainSubstring, "description")
				So(read(t, file), ShouldContainSubstring, "{{args}}")

				for _, result := range report.Kind(kind.Commands).Agents {
					So(result.Note, ShouldEqual, "")
				}

				report2 := f.sync(t)

				for _, result := range report2.Kind(kind.Commands).Agents {
					So(result.Action, ShouldEqual, engine.ActionNoop)
					So(result.Note, ShouldEqual, "")
				}
			})
		})
	})
}

func TestCommandsEngineGeminiRequiresPrompt(t *testing.T) {
	Convey("Given a canon command without a body", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.GeminiCLIID)

		So(os.MkdirAll(filepath.Join(f.home, ".gemini"), 0o750), ShouldBeNil)

		write(t, filepath.Join(f.vault.CommandsDir(), "empty.md"), string(command.Render(command.Document{
			Name: "empty", Description: "d",
		})))

		f.sync(t)

		Convey("Then Gemini gets no invalid TOML and the doctor explains", func() {
			_, err := os.Stat(filepath.Join(f.home, ".gemini", "commands", "empty.toml"))
			So(err, ShouldNotBeNil)

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)
			So(hasIssue(issues, engine.SeverityInfo, "gemini requires a prompt"), ShouldBeTrue)
		})
	})
}

func TestCommandsEnginePullNotes(t *testing.T) {
	Convey("Given a Pi prompt with literal shell text", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.PiID)

		write(t, filepath.Join(f.home, ".pi", "agent", "prompts", "log.md"),
			"---\ndescription: d\n---\nLogs: !`git log`\n")

		report := f.sync(t)

		Convey("Then the sync warns and the doctor explains", func() {
			So(strings.Join(report.Kind(kind.Commands).Warnings, "\n"), ShouldContainSubstring, "does not expand shell blocks")

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)
			So(hasIssue(issues, engine.SeverityInfo, "does not expand shell blocks"), ShouldBeTrue)
		})
	})
}
