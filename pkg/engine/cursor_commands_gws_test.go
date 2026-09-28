package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/command"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// cursorWithClaude builds a fixture with Claude and Cursor both enabled, the
// combination the verifier found broken: Claude's surface always writes
// ~/.claude/commands, which Cursor's own loader also reads.
func cursorWithClaude(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t)
	f.emptyConfigs(t)

	cursorHost(t, f)

	return f
}

// canonCommand writes one expressible command into the vault.
func canonCommand(t *testing.T, f *fixture, name, body string) {
	t.Helper()

	write(t, filepath.Join(f.vault.CommandsDir(), name+".md"), string(command.Render(command.Document{
		Name: name, Description: "d", Body: body,
	})))
}

// TestCursorCommandsBothHostsOn pins the blocker's fix: with Claude and Cursor
// both enabled, each host gets its own copy in its own dialect, and a second
// sync is stable. Cursor's surface no longer reads ~/.claude/commands, so the
// write can never land in Claude's file.
func TestCursorCommandsBothHostsOn(t *testing.T) {
	Convey("Given a canon command and both hosts enabled", t, func() {
		f := cursorWithClaude(t)
		canonCommand(t, f, "greet", "Say $1 and $ARGUMENTS.\n")

		first := f.sync(t)

		Convey("When it is synced", func() {
			Convey("Then Cursor's own directory holds the plain-markdown copy", func() {
				content := read(t, filepath.Join(f.home, ".cursor", "commands", "greet.md"))
				So(strings.HasPrefix(content, "---"), ShouldBeFalse)
				So(content, ShouldContainSubstring, "Say $1 and $ARGUMENTS.")
			})

			Convey("Then Claude's directory holds its own copy, and neither run reports a loss", func() {
				claude := read(t, filepath.Join(f.home, ".claude", "commands", "greet.md"))
				So(claude, ShouldContainSubstring, "description: d")

				for _, k := range first.Kinds {
					for _, a := range k.Agents {
						So(a.Note, ShouldNotContainSubstring, "did not keep")
					}
				}
			})

			Convey("Then a second sync is stable", func() {
				second := f.sync(t)

				for _, k := range second.Kinds {
					if k.Kind != kind.Commands {
						continue
					}

					for _, a := range k.Agents {
						So(a.Changes, ShouldBeEmpty)
					}
				}

				So(read(t, filepath.Join(f.home, ".cursor", "commands", "greet.md")), ShouldContainSubstring, "Say $1")
			})
		})
	})
}

// TestCursorCommandsClaudeSurfaceOff pins the case the verifier found: turning
// Claude's command surface off must not take Cursor's copy with it.
func TestCursorCommandsClaudeSurfaceOff(t *testing.T) {
	Convey("Given Claude's command surface off and Cursor's on", t, func() {
		f := cursorWithClaude(t)
		f.config.SetMode(agent.ClaudeCodeID, kind.Commands, config.ModeOff)

		canonCommand(t, f, "greet", "Say $1 and $ARGUMENTS.\n")

		report := f.sync(t)

		Convey("When it is synced", func() {
			Convey("Then Cursor's copy is written even though Claude's surface is off", func() {
				content := read(t, filepath.Join(f.home, ".cursor", "commands", "greet.md"))
				So(content, ShouldContainSubstring, "Say $1 and $ARGUMENTS.")

				_, statErr := os.Stat(filepath.Join(f.home, ".claude", "commands", "greet.md"))
				So(os.IsNotExist(statErr), ShouldBeTrue)
			})

			Convey("Then Cursor's run reports no loss", func() {
				for _, k := range report.Kinds {
					if k.Kind != kind.Commands {
						continue
					}

					for _, a := range k.Agents {
						if a.Agent != agent.CursorID {
							continue
						}

						So(a.Note, ShouldNotContainSubstring, "did not keep")
						So(a.Changes, ShouldHaveLength, 1)
					}
				}
			})
		})
	})
}

// TestCursorCommandsSurfaceOff pins the switch: with Cursor's own surface off,
// nothing is written into its directory.
func TestCursorCommandsSurfaceOff(t *testing.T) {
	Convey("Given Cursor's command surface off", t, func() {
		f := cursorWithClaude(t)
		f.config.SetMode(agent.CursorID, kind.Commands, config.ModeOff)

		canonCommand(t, f, "greet", "Say $1 and $ARGUMENTS.\n")

		f.sync(t)

		Convey("When it is synced", func() {
			Convey("Then its directory stays empty while Claude still gets its copy", func() {
				_, statErr := os.Stat(filepath.Join(f.home, ".cursor", "commands", "greet.md"))
				So(os.IsNotExist(statErr), ShouldBeTrue)
				So(read(t, filepath.Join(f.home, ".claude", "commands", "greet.md")), ShouldContainSubstring, "description: d")
			})
		})
	})
}

// cursorOnly builds a fixture with Cursor as the only enabled agent, so a
// command can only land in Cursor's own directory.
func cursorOnly(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t)
	f.emptyConfigs(t)

	f.config.Disable(agent.ClaudeCodeID)
	f.config.Disable(agent.OpenCodeID)

	cursorHost(t, f)

	return f
}

// TestCursorCommandsDoctor pins the doctor side of the Cursor command
// surface: the old "not supported yet" note is gone, a host-only file is a
// pending canon change, and a command Cursor cannot express is skipped with
// the reason.
func TestCursorCommandsDoctor(t *testing.T) {
	Convey("Given a Cursor command file the vault does not know", t, func() {
		f := cursorOnly(t)

		write(t, filepath.Join(f.home, ".cursor", "commands", "greet.md"), "# Greet\n\nSay $1.\n")

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When doctor runs", func() {
			Convey("Then nothing claims Cursor commands are unsupported", func() {
				for _, issue := range issues {
					So(issue.Message, ShouldNotContainSubstring, "not supported yet")
				}
			})

			Convey("Then the file is a pending canon change", func() {
				So(hasIssue(issues, engine.SeverityWarn, "not in the vault yet"), ShouldBeTrue)
			})
		})
	})

	Convey("Given a canon command Cursor cannot expand", t, func() {
		f := cursorOnly(t)

		write(t, filepath.Join(f.vault.CommandsDir(), "bad.md"), string(command.Render(command.Document{
			Name: "bad", Description: "d", Body: "Use $NAME and ${1:-world}.\n",
		})))

		f.sync(t)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When doctor runs", func() {
			Convey("Then no file was written and the reason names the copy the host will still load", func() {
				_, statErr := os.Stat(filepath.Join(f.home, ".cursor", "commands", "bad.md"))
				So(os.IsNotExist(statErr), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityInfo, "placeholder cursor cannot express"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityInfo, "cursor also loads ~/.claude/commands"), ShouldBeTrue)
			})
		})
	})

	Convey("Given a canon command Cursor can express", t, func() {
		f := cursorOnly(t)

		write(t, filepath.Join(f.vault.CommandsDir(), "greet.md"), string(command.Render(command.Document{
			Name: "greet", Description: "Greet", Body: "Say $1 and $ARGUMENTS.\n",
		})))

		report := f.sync(t)

		Convey("When it is synced", func() {
			Convey("Then the file is plain markdown, with no frontmatter in the prompt", func() {
				content := read(t, filepath.Join(f.home, ".cursor", "commands", "greet.md"))
				So(strings.HasPrefix(content, "---"), ShouldBeFalse)
				So(content, ShouldContainSubstring, "Say $1 and $ARGUMENTS.")

				for _, k := range report.Kinds {
					for _, a := range k.Agents {
						So(a.Note, ShouldNotContainSubstring, "did not keep")
					}
				}
			})
		})
	})
}
