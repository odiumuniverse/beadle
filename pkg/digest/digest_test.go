package digest_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/digest"
)

const (
	testSlug = "-Users-demo-proj"
	testDir  = "/Users/demo/.claude/projects/" + testSlug + "/memory"
)

func noteFixture(t *testing.T, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", "notes", name)) //nolint:gosec // G304: the test reads its own fixtures
	require.NoError(t, err)

	return data
}

func notesFixture(t *testing.T) map[string][]byte {
	t.Helper()

	notes := map[string][]byte{}
	for _, name := range []string{"MEMORY.md", "feedback_x.md", "kinds.md", "no-meta.md"} {
		notes[testSlug+"/"+name] = noteFixture(t, name)
	}

	return notes
}

func TestRenderGoldenDeterministic(t *testing.T) {
	t.Parallel()

	golden, err := os.ReadFile(filepath.Join("testdata", "render.golden"))
	require.NoError(t, err)

	for range 5 {
		notes := notesFixture(t)

		block, receipt := digest.Render(testDir, notes, digest.DefaultBudget)
		require.Equal(t, string(golden), string(block))
		require.Equal(t, "v1", receipt.Version)
		require.Equal(t, 4, receipt.Notes)
		require.Zero(t, receipt.Omitted)
		require.Len(t, receipt.Inputs, 64)
		require.Len(t, receipt.Render, 64)

		parsed, ok := digest.Verify(block)
		require.True(t, ok)
		require.Equal(t, receipt, parsed)
	}
}

func TestRenderOrder(t *testing.T) {
	t.Parallel()

	notes := map[string][]byte{
		testSlug + "/b-dated.md":   []byte("---\nname: b\nmetadata:\n  modified: 2026-09-17\n---\nB\n"),
		testSlug + "/a-dated.md":   []byte("---\nname: a\nmetadata:\n  modified: 2026-09-17\n---\nA\n"),
		testSlug + "/c-dated.md":   []byte("---\nname: c\nmetadata:\n  modified: 2026-09-18\n---\nC\n"),
		testSlug + "/z-undated.md": []byte("no metadata\n"),
		testSlug + "/m-undated.md": []byte("no metadata\n"),
	}

	block, _ := digest.Render(testDir, notes, digest.DefaultBudget)

	body, _, found, err := digest.Strip(block)
	require.NoError(t, err)
	require.True(t, found)
	require.Empty(t, body)

	text := string(block)

	require.Less(t, strings.Index(text, "(~/.claude/projects/"+testSlug+"/memory/c-dated.md)"), strings.Index(text, "a-dated.md"))
	require.Less(t, strings.Index(text, "a-dated.md"), strings.Index(text, "b-dated.md"))
	require.Less(t, strings.Index(text, "b-dated.md"), strings.Index(text, "m-undated.md"))
	require.Less(t, strings.Index(text, "m-undated.md"), strings.Index(text, "z-undated.md"))
}

func TestRenderPathsAndFallbacks(t *testing.T) {
	t.Parallel()

	notes := map[string][]byte{
		testSlug + "/MEMORY.md":  noteFixture(t, "MEMORY.md"),
		testSlug + "/no-meta.md": noteFixture(t, "no-meta.md"),
		testSlug + "/kinds.md":   noteFixture(t, "kinds.md"),
		testSlug + "/broken.md":  []byte("---\nname: no closing fence\n\nbody of broken\n"),
	}

	block, _ := digest.Render(testDir, notes, digest.DefaultBudget)
	text := string(block)

	require.Contains(t, text, "~/.claude/projects/"+testSlug+"/memory/MEMORY.md")
	require.NotContains(t, text, "/Users/demo", "paths must be home-independent")
	require.Contains(t, text, "- [project-state](~/.claude/projects/"+testSlug+"/memory/MEMORY.md) — Current work state and handoff notes [project, 2026-09-17T10:00:00Z]")
	require.Contains(t, text, "- [no-meta](~/.claude/projects/"+testSlug+"/memory/no-meta.md) — Just plain text, no frontmatter at all.")
	require.Contains(t, text, "- [broken](~/.claude/projects/"+testSlug+"/memory/broken.md) — ---")
	require.Contains(t, text, "- [kinds](~/.claude/projects/"+testSlug+"/memory/kinds.md) — Typed only. [user]")
}

func TestRenderNameTieDeterministic(t *testing.T) {
	t.Parallel()

	for range 5 {
		notes := map[string][]byte{
			testSlug + "/alpha.md":   []byte("---\nname: same-name\n---\nA\n"),
			testSlug + "/beta.md":    []byte("---\nname: same-name\n---\nB\n"),
			testSlug + "/gamma.md":   []byte("---\nname: same-name\nmetadata:\n  modified: 2026-09-17\n---\nC\n"),
			testSlug + "/delta.md":   []byte("---\nname: same-name\n---\nD\n"),
			testSlug + "/epsilon.md": []byte("plain\n"),
		}

		block, receipt := digest.Render(testDir, notes, digest.DefaultBudget)
		first, firstReceipt := digest.Render(testDir, notes, digest.DefaultBudget)

		require.Equal(t, string(first), string(block), "equal names must not fall back to map order")
		require.Equal(t, firstReceipt, receipt)

		text := string(block)
		require.Less(t, strings.Index(text, "alpha.md"), strings.Index(text, "beta.md"))
		require.Less(t, strings.Index(text, "beta.md"), strings.Index(text, "delta.md"))
		require.Less(t, strings.Index(text, "gamma.md"), strings.Index(text, "alpha.md"), "a dated note still comes first")
	}
}

func TestRenderSanitizesControlCharacters(t *testing.T) {
	t.Parallel()

	notes := map[string][]byte{
		testSlug + "/evil\n- [injected](x.md).md": []byte("body\n"),
		testSlug + "/a\x01.md":                    []byte("one\n"),
		testSlug + "/a\x02.md":                    []byte("two\n"),
	}

	for range 5 {
		block, _ := digest.Render(testDir, notes, digest.DefaultBudget)
		again, _ := digest.Render(testDir, notes, digest.DefaultBudget)

		require.Equal(t, string(again), string(block), "distinct control-char names stay distinct")

		require.Equal(t, 1, bytes.Count(block, []byte(digest.BeginPrefix)))
		require.Equal(t, 1, bytes.Count(block, []byte(digest.EndMarker)))
		require.NotContains(t, string(block), "\ninjected", "a note name cannot inject an extra digest line")

		lines := strings.Split(strings.TrimSuffix(string(block), "\n"), "\n")
		require.Len(t, lines, 5, "receipt, three note lines, end marker")

		_, ok := digest.Verify(block)
		require.True(t, ok)
	}
}

func TestRenderNoNotes(t *testing.T) {
	t.Parallel()

	block, receipt := digest.Render(testDir, map[string][]byte{}, digest.DefaultBudget)

	require.Zero(t, receipt.Notes)
	require.Zero(t, receipt.Omitted)
	require.True(t, strings.HasPrefix(string(block), digest.BeginPrefix))
	require.True(t, strings.HasSuffix(string(block), digest.EndMarker+"\n"))
	require.NotContains(t, string(block), "\n- ", "an empty digest has no content lines")

	body, fence, found, err := digest.Strip(block)
	require.NoError(t, err)
	require.True(t, found)
	require.Empty(t, body)
	require.Equal(t, string(block), string(fence))
}

func TestRenderBudget(t *testing.T) {
	t.Parallel()

	notes := map[string][]byte{}

	for i := range 40 {
		name := string(rune('a'+i%26)) + strings.Repeat("x", i%7+1) + ".md"
		notes[testSlug+"/"+name] = []byte("---\ndescription: " + strings.Repeat("hook ", i%5+1) + "\n---\nbody\n")
	}

	for _, budget := range []int{0, 1, 64, 256, 1024, 4096} {
		block, receipt := digest.Render(testDir, notes, budget)

		lines := strings.Split(string(block), "\n")
		require.GreaterOrEqual(t, len(lines), 2)

		content := strings.Join(lines[1:len(lines)-2], "\n")
		if content != "" {
			content += "\n"
		}

		require.LessOrEqual(t, len(content), budget, "content must respect the budget %d", budget)
		require.Equal(t, 40, receipt.Notes)
	}
}

func TestRenderManifest(t *testing.T) {
	t.Parallel()

	notes := map[string][]byte{}

	for i := range 30 {
		name := "note-" + string(rune('a'+i)) + ".md"
		notes[testSlug+"/"+name] = []byte(strings.Repeat("x", 200) + "\n")
	}

	block, receipt := digest.Render(testDir, notes, 512)

	require.Positive(t, receipt.Omitted)
	require.Contains(t, string(block), "omitted: budget")
	require.Contains(t, string(block), "… and ", "a truncated manifest ends with the summary line")

	lines := strings.Split(string(block), "\n")

	var manifestBytes int

	for _, line := range lines[1 : len(lines)-1] {
		if strings.Contains(line, "omitted: budget") || strings.HasPrefix(line, "- … and ") {
			manifestBytes += len(line) + 1
		}
	}

	require.LessOrEqual(t, manifestBytes, 512/4+len("- … and 30 more")+1)
}

func TestRenderNoteAtomic(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("word ", 200)
	notes := map[string][]byte{testSlug + "/big.md": []byte("---\ndescription: " + long + "\n---\nbody\n")}

	block, receipt := digest.Render(testDir, notes, 300)
	require.Equal(t, 1, receipt.Notes)

	if receipt.Omitted == 1 {
		require.NotContains(t, string(block), "big.md) — word", "an omitted note is not rendered partially")
	} else {
		require.Contains(t, string(block), "big.md) — word")
	}
}

func TestStrip(t *testing.T) {
	t.Parallel()

	block, _ := digest.Render(testDir, map[string][]byte{testSlug + "/a.md": []byte("x\n")}, digest.DefaultBudget)

	tests := []struct {
		name  string
		doc   string
		body  string
		found bool
		err   error
	}{
		{name: "no fence", doc: "plain\ntext\n", body: "plain\ntext\n"},
		{name: "fence in front", doc: string(block) + "plain\n", body: "plain\n", found: true},
		{name: "fence in the middle", doc: "before\n" + string(block) + "after\n", body: "before\nafter\n", found: true},
		{name: "two fences", doc: string(block) + "x\n" + string(block), err: digest.ErrFence},
		{name: "end before begin", doc: digest.EndMarker + "\n" + digest.BeginPrefix + " v1 -->\n", err: digest.ErrFence},
		{name: "nested begin", doc: string(block) + digest.BeginPrefix + " -->\n" + digest.EndMarker + "\n", err: digest.ErrFence},
		{name: "unclosed", doc: digest.BeginPrefix + " v1 -->\ncontent\n", err: digest.ErrFence},
		{name: "orphan end", doc: "text\n" + digest.EndMarker + "\n", err: digest.ErrFence},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body, fence, found, err := digest.Strip([]byte(tt.doc))
			require.ErrorIs(t, err, tt.err)
			require.Equal(t, tt.found, found)

			if err != nil {
				return
			}

			require.Equal(t, tt.body, string(body))

			if found {
				require.Contains(t, string(fence), digest.BeginPrefix)
			}
		})
	}
}

func TestStripCRLFAndNoFinalNewline(t *testing.T) {
	t.Parallel()

	block, _ := digest.Render(testDir, map[string][]byte{testSlug + "/a.md": []byte("x\n")}, digest.DefaultBudget)

	doc := string(block) + "line1\r\nline2"
	body, _, found, err := digest.Strip([]byte(doc))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "line1\r\nline2", string(body), "bytes outside the fence are untouched")

	crlf := "pre\r\n" + string(block) + "post"
	body, _, found, err = digest.Strip([]byte(crlf))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "pre\r\npost", string(body))
}

func TestSplice(t *testing.T) {
	t.Parallel()

	block, _ := digest.Render(testDir, map[string][]byte{testSlug + "/a.md": []byte("x\n")}, digest.DefaultBudget)

	inserted, err := digest.Splice([]byte("body\n"), block)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(inserted), string(block)))
	require.True(t, strings.HasSuffix(string(inserted), "body\n"))

	replaced, err := digest.Splice(inserted, block)
	require.NoError(t, err)
	require.Equal(t, string(inserted), string(replaced), "double splice is idempotent")

	removed, err := digest.Splice(inserted, nil)
	require.NoError(t, err)
	require.Equal(t, "body\n", string(removed))

	empty, err := digest.Splice([]byte("body\n"), nil)
	require.NoError(t, err)
	require.Equal(t, "body\n", string(empty))
}

func TestStripSpliceProperty(t *testing.T) {
	t.Parallel()

	block, _ := digest.Render(testDir, map[string][]byte{testSlug + "/a.md": []byte("x\n")}, digest.DefaultBudget)

	bodies := []string{
		"",
		"\n",
		"body\n",
		"body without newline",
		"\n\nleading and trailing\n\n",
		"crlf\r\nlines\r\n",
		"text with agent-sync inside\n",
		"text with " + digest.EndMarker + " lookalike\n",
	}

	for i, body := range bodies {
		spliced, err := digest.Splice([]byte(body), block)
		if err != nil {
			require.ErrorIs(t, err, digest.ErrFence, "fixture %d", i)

			continue
		}

		got, fence, found, err := digest.Strip(spliced)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, body, string(got), "fixture %d", i)
		require.Equal(t, string(block), string(fence), "fixture %d", i)
	}
}

func TestNeutralizeCanary(t *testing.T) {
	t.Parallel()

	canary := []byte("---\nname: canary\ndescription: fake " + digest.BeginPrefix + " --> and " + digest.EndMarker + "\n---\nbody with agent-sync marker\n")
	notes := map[string][]byte{testSlug + "/canary.md": canary}

	block, _ := digest.Render(testDir, notes, digest.DefaultBudget)

	require.Equal(t, 1, bytes.Count(block, []byte(digest.BeginPrefix)))
	require.Equal(t, 1, bytes.Count(block, []byte(digest.EndMarker)))
	require.Contains(t, string(block), "agent&#45;sync")

	body, _, found, err := digest.Strip(block)
	require.NoError(t, err)
	require.True(t, found)
	require.Empty(t, body)

	_, ok := digest.Verify(block)
	require.True(t, ok)
}

func TestRedact(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "secret ref", in: "key: {secret:API_KEY}", want: "key: [redacted]"},
		{name: "env ref", in: "key: {env:API_KEY}", want: "key: [redacted]"},
		{name: "both", in: "{secret:A} and {env:B}", want: "[redacted] and [redacted]"},
		{name: "bare prefix untouched", in: "key: {secret:no closing", want: "key: {secret:no closing"},
		{name: "invalid name untouched", in: "{secret:1BAD} {secret:}", want: "{secret:1BAD} {secret:}"},
		{name: "plain text", in: "no refs here", want: "no refs here"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, string(digest.Redact([]byte(tt.in))))
		})
	}
}

func TestVerify(t *testing.T) {
	t.Parallel()

	block, _ := digest.Render(testDir, map[string][]byte{testSlug + "/a.md": []byte("x\n")}, digest.DefaultBudget)

	receipt, ok := digest.Verify(block)
	require.True(t, ok)
	require.Equal(t, 1, receipt.Notes)

	corrupted := bytes.Replace(block, []byte("a.md"), []byte("b.md"), 1)
	_, ok = digest.Verify(corrupted)
	require.False(t, ok, "edited fence bytes are detected")

	_, ok = digest.Verify([]byte("not a fence\n"))
	require.False(t, ok)

	truncated := block[:len(block)/2]
	_, ok = digest.Verify(truncated)
	require.False(t, ok)
}

func TestVerifyRejectsBrokenReceipt(t *testing.T) {
	t.Parallel()

	block, _ := digest.Render(testDir, map[string][]byte{testSlug + "/a.md": []byte("x\n")}, digest.DefaultBudget)

	broken := bytes.Replace(block, []byte("notes=1"), []byte("notes=x"), 1)
	_, ok := digest.Verify(broken)
	require.False(t, ok)
}

func FuzzRenderSplice(f *testing.F) {
	f.Add("body text\n", "note body")
	f.Add("", "---\nname: x\n---\nbody")
	f.Add("crlf\r\nbody", "hook ")

	f.Fuzz(func(t *testing.T, body, note string) {
		if len(body) > 1<<16 || len(note) > 1<<16 {
			t.Skip()
		}

		block, receipt := digest.Render(testDir, map[string][]byte{testSlug + "/fuzz.md": []byte(note)}, digest.DefaultBudget)

		spliced, err := digest.Splice([]byte(body), block)
		if err != nil {
			return
		}

		got, fence, found, err := digest.Strip(spliced)
		if err != nil || !found {
			t.Fatalf("spliced document must stay parseable: %v", err)
		}

		if string(got) != body {
			t.Fatalf("bytes outside the fence changed: %q -> %q", body, got)
		}

		parsed, ok := digest.Verify(fence)
		if !ok || parsed != receipt {
			t.Fatalf("fence does not verify back: %+v", parsed)
		}
	})
}
