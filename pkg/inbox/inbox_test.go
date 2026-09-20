package inbox_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/digest"
	"github.com/odiumuniverse/beadle/pkg/inbox"
)

func writeInbox(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "inbox.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write inbox: %v", err)
	}

	return path
}

func hashOf(t *testing.T, text string) string {
	t.Helper()

	path := writeInbox(t, text+"\n")

	lines, err := inbox.Read(path)
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}

	if len(lines) != 1 {
		t.Fatalf("expected one line, got %d", len(lines))
	}

	return lines[0].Hash
}

func TestRead(t *testing.T) {
	Convey("Given a missing inbox", t, func() {
		Convey("When it is read", func() {
			lines, err := inbox.Read(filepath.Join(t.TempDir(), "missing.md"))

			Convey("Then it is empty", func() {
				So(err, ShouldBeNil)
				So(lines, ShouldBeEmpty)
			})
		})
	})

	Convey("Given an inbox with headers, comments and text", t, func() {
		path := writeInbox(t, "# Inbox\n\nremember the milk\r\n<!-- section: work -->\nuse "+strings.Repeat("x", 10)+"\n\n")

		Convey("When it is read", func() {
			lines, err := inbox.Read(path)

			Convey("Then only content lines are kept and hashed stably", func() {
				So(err, ShouldBeNil)
				So(lines, ShouldHaveLength, 2)
				So(lines[0].Text, ShouldEqual, "remember the milk")
				So(lines[1].Text, ShouldEqual, "use "+strings.Repeat("x", 10))
				So(lines[0].Hash, ShouldHaveLength, 8)
				So(lines[0].Hash, ShouldNotEqual, lines[1].Hash)
				So(lines[0].Hash, ShouldEqual, hashOf(t, "remember the milk"))
			})
		})
	})

	Convey("Given an inbox whose lines all start with #", t, func() {
		Convey("When it is read", func() {
			lines, err := inbox.Read(writeInbox(t, "## Section\n#note-like\n"))

			Convey("Then it is empty", func() {
				So(err, ShouldBeNil)
				So(lines, ShouldBeEmpty)
			})
		})
	})
}

func TestReadError(t *testing.T) {
	Convey("Given an unreadable inbox", t, func() {
		path := writeInbox(t, "x\n")
		So(os.Chmod(path, 0o000), ShouldBeNil)
		t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

		Convey("When it is read", func() {
			_, err := inbox.Read(path)

			Convey("Then it fails", func() {
				So(err, ShouldBeError)
			})
		})
	})
}

func TestNoteDeterministic(t *testing.T) {
	Convey("Given an inbox line", t, func() {
		line := inbox.Line{Text: "remember the milk", Hash: "abc12345"}

		key, data := inbox.Note("-Users-demo", "opencode", line)
		keyAgain, dataAgain := inbox.Note("-Users-demo", "opencode", line)

		Convey("When a note is rendered", func() {
			Convey("Then rendering is deterministic and well formed", func() {
				So(key, ShouldEqual, keyAgain)
				So(string(dataAgain), ShouldEqual, string(data))
				So(key, ShouldEqual, "-Users-demo/inbox-abc12345.md")

				text := string(data)
				So(strings.HasPrefix(text, "---\nname: \"remember the milk\"\ndescription: \"remember the milk\"\n"), ShouldBeTrue)
				So(text, ShouldContainSubstring, "metadata:\n  type: user\n  origin: inbox\n  agent: opencode\n  hash: abc12345\n---\n")
				So(text, ShouldContainSubstring, "remember the milk\n\n<!-- transcribed: opencode -->\n")
			})
		})
	})
}

func TestNoteFrontmatterParsesInDigest(t *testing.T) {
	Convey("Given an inbox note", t, func() {
		line := inbox.Line{Text: "use the staging cluster", Hash: "deadbeef"}
		key, data := inbox.Note("-Users-demo", "cursor", line)

		Convey("When it is rendered into the digest", func() {
			block, receipt := digest.Render("", map[string][]byte{key: data}, digest.DefaultBudget)

			Convey("Then the frontmatter drives the digest entry", func() {
				So(receipt.Notes, ShouldEqual, 1)
				So(string(block), ShouldContainSubstring, "- [use the staging cluster](")
				So(string(block), ShouldContainSubstring, "— use the staging cluster [user]")
			})
		})
	})
}

func TestNoteCapsLongLines(t *testing.T) {
	Convey("Given a long inbox line", t, func() {
		text := strings.Repeat("я", 200)
		line := inbox.Line{Text: text, Hash: "12345678"}

		_, data := inbox.Note("-Users-demo", "gemini-cli", line)

		Convey("When a note is rendered", func() {
			Convey("Then the frontmatter is capped but the body keeps the full line", func() {
				So(string(data), ShouldContainSubstring, "name: \""+strings.Repeat("я", 60)+"\"\n")
				So(string(data), ShouldContainSubstring, "description: \""+strings.Repeat("я", 120)+"\"\n")
				So(string(data), ShouldContainSubstring, "---\n"+text+"\n")
			})
		})
	})
}

func TestNoteName(t *testing.T) {
	Convey("Given a line hash", t, func() {
		Convey("When the note name is built", func() {
			Convey("Then it is inbox-<hash>.md", func() {
				So(inbox.NoteName(inbox.Line{Hash: "0011aabb"}), ShouldEqual, "inbox-0011aabb.md")
			})
		})
	})
}
