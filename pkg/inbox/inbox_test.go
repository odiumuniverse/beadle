package inbox_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/digest"
	"github.com/odiumuniverse/agents-sync/pkg/inbox"
)

func writeInbox(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "inbox.md")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	return path
}

func TestRead(t *testing.T) {
	t.Parallel()

	lines, err := inbox.Read(filepath.Join(t.TempDir(), "missing.md"))
	require.NoError(t, err)
	require.Empty(t, lines)

	path := writeInbox(t, "# Inbox\n\nremember the milk\r\n<!-- section: work -->\nuse "+strings.Repeat("x", 10)+"\n\n")

	lines, err = inbox.Read(path)
	require.NoError(t, err)
	require.Len(t, lines, 2, "markdown headers and comments are skipped")
	require.Equal(t, "remember the milk", lines[0].Text)
	require.Equal(t, "use "+strings.Repeat("x", 10), lines[1].Text)
	require.Len(t, lines[0].Hash, 8)
	require.NotEqual(t, lines[0].Hash, lines[1].Hash, "different lines hash differently")
	require.Equal(t, lines[0].Hash, hashOf(t, "remember the milk"), "the hash is stable")

	lines, err = inbox.Read(writeInbox(t, "## Section\n#note-like\n"))
	require.NoError(t, err)
	require.Empty(t, lines, "any leading # marks a header")
}

func hashOf(t *testing.T, text string) string {
	t.Helper()

	path := writeInbox(t, text+"\n")

	lines, err := inbox.Read(path)
	require.NoError(t, err)
	require.Len(t, lines, 1)

	return lines[0].Hash
}

func TestReadError(t *testing.T) {
	t.Parallel()

	path := writeInbox(t, "x\n")
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	_, err := inbox.Read(path)
	require.Error(t, err)
}

func TestNoteDeterministic(t *testing.T) {
	t.Parallel()

	line := inbox.Line{Text: "remember the milk", Hash: "abc12345"}

	key, data := inbox.Note("-Users-demo", "opencode", line)
	keyAgain, dataAgain := inbox.Note("-Users-demo", "opencode", line)

	require.Equal(t, key, keyAgain)
	require.Equal(t, string(data), string(dataAgain), "note rendering is a pure function")
	require.Equal(t, "-Users-demo/inbox-abc12345.md", key)

	text := string(data)
	require.True(t, strings.HasPrefix(text, "---\nname: \"remember the milk\"\ndescription: \"remember the milk\"\n"))
	require.Contains(t, text, "metadata:\n  type: user\n  origin: inbox\n  agent: opencode\n  hash: abc12345\n---\n")
	require.Contains(t, text, "remember the milk\n\n<!-- transcribed: opencode -->\n")
}

func TestNoteFrontmatterParsesInDigest(t *testing.T) {
	t.Parallel()

	line := inbox.Line{Text: "use the staging cluster", Hash: "deadbeef"}
	key, data := inbox.Note("-Users-demo", "cursor", line)

	block, receipt := digest.Render("", map[string][]byte{key: data}, digest.DefaultBudget)

	require.Equal(t, 1, receipt.Notes)
	require.Contains(t, string(block), "- [use the staging cluster](", "the name comes from the frontmatter")
	require.Contains(t, string(block), "— use the staging cluster [user]", "the hook and type come from the frontmatter")
}

func TestNoteCapsLongLines(t *testing.T) {
	t.Parallel()

	text := strings.Repeat("я", 200)
	line := inbox.Line{Text: text, Hash: "12345678"}

	_, data := inbox.Note("-Users-demo", "gemini-cli", line)

	require.Contains(t, string(data), "name: \""+strings.Repeat("я", 60)+"\"\n")
	require.Contains(t, string(data), "description: \""+strings.Repeat("я", 120)+"\"\n")
	require.Contains(t, string(data), "---\n"+text+"\n", "the body keeps the full line")
}

func TestNoteName(t *testing.T) {
	t.Parallel()

	require.Equal(t, "inbox-0011aabb.md", inbox.NoteName(inbox.Line{Hash: "0011aabb"}))
}
