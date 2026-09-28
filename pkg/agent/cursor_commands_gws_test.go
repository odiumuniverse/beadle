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

// cursorNotices renders the command notices of one cursor command.
func cursorNotices(a *agent.Agent, key string, value []byte) string {
	var out strings.Builder

	for _, notice := range a.Notices(kind.Commands, key, value) {
		out.WriteString(notice.Message)
		out.WriteString("\n")
	}

	return out.String()
}

// TestCursorCommands pins the Cursor command surface against the host's own
// reader: cursor-agent 2026.06.15 (`custom-commands.ts`) parses plain markdown
// — the file stem is the id, the first line the title, the whole text the
// prompt — and expands only `$ARGUMENTS` and 1-based `$1..`.
func TestCursorCommands(t *testing.T) {
	Convey("Given a Cursor agent", t, func() {
		home := t.TempDir()
		a := agent.Cursor(home, t.TempDir())
		dir := filepath.Join(home, ".cursor", "commands")

		Convey("Then the command surface is on by default and writes Cursor's own directory", func() {
			surface := surfaceOf(t, a, kind.Commands)

			So(surface.Path(), ShouldEqual, dir)
			So(surface.Traits().DefaultMode, ShouldEqual, config.ModeSync)
			So(surface.Traits().Creatable, ShouldBeTrue)
			// Own directory only: Cursor's loader also reads
			// ~/.claude/commands, but listing it here would make the surface
			// write into Claude's file instead of Cursor's (locate returns the
			// first read dir that holds the name).
			So(surface.WatchPaths(), ShouldResemble, []string{dir})
		})

		Convey("When a plain markdown command is read", func() {
			writeFile(t, filepath.Join(dir, "greet.md"), "# Greet\n\nSay $1 and $ARGUMENTS.\n")

			snap := snapshot(t, a, kind.Commands)

			Convey("Then the first line becomes the description and the body keeps its placeholders", func() {
				item := string(snap.Items["greet.md"])

				So(item, ShouldContainSubstring, "description: Greet")
				So(item, ShouldContainSubstring, "Say $1 and $ARGUMENTS.")
				So(snap.Warnings, ShouldBeEmpty)
			})
		})

		Convey("When the first line is not a heading", func() {
			writeFile(t, filepath.Join(dir, "plain.md"), "Plain title\n\nRun $1.\n")

			Convey("Then the whole first line is the title, exactly as the host shows it", func() {
				snap := snapshot(t, a, kind.Commands)
				So(string(snap.Items["plain.md"]), ShouldContainSubstring, "description: Plain title")
			})
		})
	})
}

// TestCursorCommandsWrite pins the write side: Cursor gets plain markdown,
// the keys it has no schema for are reported, and a placeholder it cannot
// expand skips the host with a note.
func TestCursorCommandsWrite(t *testing.T) {
	Convey("Given a Cursor agent", t, func() {
		home := t.TempDir()
		a := agent.Cursor(home, t.TempDir())
		dir := filepath.Join(home, ".cursor", "commands")

		Convey("When a canonical command is written", func() {
			canon := []byte("---\ndescription: Greet\n---\nSay $1 and $ARGUMENTS.\n")

			So(surfaceOf(t, a, kind.Commands).Write(t.Context(), kind.Items{"greet.md": canon}), ShouldBeNil)

			Convey("Then the file is plain markdown: no frontmatter reaches the host's prompt", func() {
				So(readFile(t, filepath.Join(dir, "greet.md")), ShouldEqual, "Say $1 and $ARGUMENTS.\n")
			})

			Convey("Then the description is reported as not written", func() {
				So(cursorNotices(a, "greet.md", canon), ShouldContainSubstring,
					"cursor takes the command title from the file's first line")
			})

			Convey("Then writing it again is byte-stable", func() {
				before := readFile(t, filepath.Join(dir, "greet.md"))
				So(surfaceOf(t, a, kind.Commands).Write(t.Context(), kind.Items{"greet.md": canon}), ShouldBeNil)
				So(readFile(t, filepath.Join(dir, "greet.md")), ShouldEqual, before)
			})
		})

		Convey("When the canon carries keys Cursor has no schema for", func() {
			value := []byte("---\ndescription: greet\nargument-hint: \"<name>\"\narguments:\n  - name\nmodel: opus\ndisable-model-invocation: true\n---\nSay $1.\n")

			So(surfaceOf(t, a, kind.Commands).Write(t.Context(), kind.Items{"greet.md": value}), ShouldBeNil)

			Convey("Then every one of them is reported, and the file is still written", func() {
				notice := cursorNotices(a, "greet.md", value)

				So(notice, ShouldContainSubstring, "argument-hint is not expressible for cursor")
				So(notice, ShouldContainSubstring, "arguments is not expressible for cursor")
				So(notice, ShouldContainSubstring, "model is not expressible for cursor")
				So(notice, ShouldContainSubstring, "disable-model-invocation is not expressible for cursor")
				So(readFile(t, filepath.Join(dir, "greet.md")), ShouldEqual, "Say $1.\n")
			})
		})

		Convey("When the canon uses a placeholder Cursor cannot expand", func() {
			value := []byte("---\ndescription: greet\n---\nUse $NAME, ${1:-world}, !`git status` and @src/file.go.\n")

			Convey("Then no file is written and the doctor says why", func() {
				So(surfaceOf(t, a, kind.Commands).Write(t.Context(), kind.Items{"greet.md": value}), ShouldBeNil)

				_, statErr := os.Stat(filepath.Join(dir, "greet.md"))
				So(os.IsNotExist(statErr), ShouldBeTrue)
				So(cursorNotices(a, "greet.md", value), ShouldContainSubstring,
					"the template uses a placeholder cursor cannot express")
			})
		})

		Convey("When a cursor file uses constructs the host does not expand", func() {
			writeFile(t, filepath.Join(dir, "extra.md"), "# Extra\n\nUse !`git status` and @src/file.go.\n")

			Convey("Then the read reports them and the file stays in place", func() {
				snap := snapshot(t, a, kind.Commands)

				So(strings.Join(snap.Warnings, "\n"), ShouldContainSubstring, "cursor does not expand shell blocks")
				So(strings.Join(snap.Warnings, "\n"), ShouldContainSubstring, "cursor does not expand file references")
				So(snap.Items, ShouldContainKey, "extra.md")
			})
		})

		Convey("When a file in Cursor's own directory carries frontmatter anyway", func() {
			writeFile(t, filepath.Join(dir, "legacy.md"), "---\ndescription: legacy\n---\nRun it.\n")

			Convey("Then it is not read as a canon item, because this codec would never have written it", func() {
				// Cursor's command files are plain markdown; a leading `---`
				// block is another dialect's. Adopting it would let a lookup
				// hand a write to a file this host does not own, which is the
				// blocker this fix round exists to close.
				snap := snapshot(t, a, kind.Commands)
				So(snap.Items, ShouldNotContainKey, "legacy.md")

				// A delivery under that name is not located on it: the write
				// falls back to Cursor's own directory, where the codec may
				// write it.
				canon := command.Render(command.Document{Name: "legacy", Description: "legacy", Body: "Run it.\n"})
				So(surfaceOf(t, a, kind.Commands).Write(t.Context(), kind.Items{"legacy.md": canon}), ShouldBeNil)
				So(readFile(t, filepath.Join(dir, "legacy.md")), ShouldEqual, "Run it.\n")
			})

			Convey("Then the same document under another host's directory is never a write target", func() {
				// The blocker itself: a Claude-shaped file in ~/.claude/commands
				// must not become the place a Cursor delivery lands.
				claudePath := filepath.Join(home, ".claude", "commands", "shared.md")
				writeFile(t, claudePath, "---\ndescription: shared\n---\nFrom claude.\n")

				canon := command.Render(command.Document{Name: "shared", Description: "shared", Body: "From cursor.\n"})
				So(surfaceOf(t, a, kind.Commands).Write(t.Context(), kind.Items{"shared.md": canon}), ShouldBeNil)

				So(readFile(t, claudePath), ShouldEqual, "---\ndescription: shared\n---\nFrom claude.\n")
				So(readFile(t, filepath.Join(dir, "shared.md")), ShouldEqual, "From cursor.\n")
			})
		})

		Convey("When Cursor's own copy and the Claude copy share a name", func() {
			writeFile(t, filepath.Join(dir, "greet.md"), "# Cursor greet\n\nFrom cursor.\n")
			writeFile(t, filepath.Join(home, ".claude", "commands", "greet.md"), "# Claude greet\n\nFrom claude.\n")

			Convey("Then the surface owns only its own copy, which is the one the host prefers", func() {
				snap := snapshot(t, a, kind.Commands)
				So(string(snap.Items["greet.md"]), ShouldContainSubstring, "From cursor.")

				// The Claude copy is Claude's surface's business; the host
				// loads both and keeps the later one (cursor's).
				So(snap.Items, ShouldHaveLength, 1)
			})
		})

		Convey("When only the Claude commands directory exists", func() {
			writeFile(t, filepath.Join(home, ".claude", "commands", "shared.md"), "# Shared\n\nFrom claude.\n")

			Convey("Then the surface ignores it: reading it would make the next write land there", func() {
				snap := snapshot(t, a, kind.Commands)
				So(snap.Items, ShouldBeEmpty)
			})
		})
	})
}
