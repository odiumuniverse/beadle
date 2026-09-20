package digest_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/digest"
)

const (
	testSlug = "-Users-demo-proj"
	testDir  = "/Users/demo/.claude/projects/" + testSlug + "/memory"
)

func noteFixture(t *testing.T, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", "notes", name)) //nolint:gosec // G304: the test reads its own fixtures
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

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
	Convey("Given the golden notes fixture", t, func() {
		golden, err := os.ReadFile(filepath.Join("testdata", "render.golden"))
		So(err, ShouldBeNil)

		Convey("When the digest is rendered five times", func() {
			var first string

			var firstReceipt digest.Receipt

			for i := range 5 {
				notes := notesFixture(t)

				block, receipt := digest.Render(testDir, notes, digest.DefaultBudget)

				if i == 0 {
					first, firstReceipt = string(block), receipt
				} else {
					So(string(block), ShouldEqual, first)
					So(receipt, ShouldResemble, firstReceipt)
				}
			}

			Convey("Then it matches the golden and verifies", func() {
				So(first, ShouldEqual, string(golden))
				So(firstReceipt.Version, ShouldEqual, "v1")
				So(firstReceipt.Notes, ShouldEqual, 4)
				So(firstReceipt.Omitted, ShouldEqual, 0)
				So(firstReceipt.Inputs, ShouldHaveLength, 64)
				So(firstReceipt.Render, ShouldHaveLength, 64)

				parsed, ok := digest.Verify([]byte(first))
				So(ok, ShouldBeTrue)
				So(parsed, ShouldResemble, firstReceipt)
			})
		})
	})
}

func TestRenderOrder(t *testing.T) {
	Convey("Given notes with and without dates", t, func() {
		notes := map[string][]byte{
			testSlug + "/b-dated.md":   []byte("---\nname: b\nmetadata:\n  modified: 2026-09-17\n---\nB\n"),
			testSlug + "/a-dated.md":   []byte("---\nname: a\nmetadata:\n  modified: 2026-09-17\n---\nA\n"),
			testSlug + "/c-dated.md":   []byte("---\nname: c\nmetadata:\n  modified: 2026-09-18\n---\nC\n"),
			testSlug + "/z-undated.md": []byte("no metadata\n"),
			testSlug + "/m-undated.md": []byte("no metadata\n"),
		}

		block, _ := digest.Render(testDir, notes, digest.DefaultBudget)

		body, _, found, err := digest.Strip(block)
		So(err, ShouldBeNil)

		Convey("When the digest is rendered", func() {
			text := string(block)

			Convey("Then dated notes come first and undated ones are alphabetical", func() {
				So(found, ShouldBeTrue)
				So(body, ShouldBeEmpty)

				So(strings.Index(text, "(~/.claude/projects/"+testSlug+"/memory/c-dated.md)"), ShouldBeLessThan, strings.Index(text, "a-dated.md"))
				So(strings.Index(text, "a-dated.md"), ShouldBeLessThan, strings.Index(text, "b-dated.md"))
				So(strings.Index(text, "b-dated.md"), ShouldBeLessThan, strings.Index(text, "m-undated.md"))
				So(strings.Index(text, "m-undated.md"), ShouldBeLessThan, strings.Index(text, "z-undated.md"))
			})
		})
	})
}

func TestRenderPathsAndFallbacks(t *testing.T) {
	Convey("Given notes with meta, without meta and broken frontmatter", t, func() {
		notes := map[string][]byte{
			testSlug + "/MEMORY.md":  noteFixture(t, "MEMORY.md"),
			testSlug + "/no-meta.md": noteFixture(t, "no-meta.md"),
			testSlug + "/kinds.md":   noteFixture(t, "kinds.md"),
			testSlug + "/broken.md":  []byte("---\nname: no closing fence\n\nbody of broken\n"),
		}

		block, _ := digest.Render(testDir, notes, digest.DefaultBudget)
		text := string(block)

		Convey("When the digest is rendered", func() {
			Convey("Then paths are home-independent and each line falls back as expected", func() {
				So(text, ShouldContainSubstring, "~/.claude/projects/"+testSlug+"/memory/MEMORY.md")
				So(text, ShouldNotContainSubstring, "/Users/demo")
				So(text, ShouldContainSubstring, "- [project-state](~/.claude/projects/"+testSlug+"/memory/MEMORY.md) — Current work state and handoff notes [project, 2026-09-17T10:00:00Z]")
				So(text, ShouldContainSubstring, "- [no-meta](~/.claude/projects/"+testSlug+"/memory/no-meta.md) — Just plain text, no frontmatter at all.")
				So(text, ShouldContainSubstring, "- [broken](~/.claude/projects/"+testSlug+"/memory/broken.md) — ---")
				So(text, ShouldContainSubstring, "- [kinds](~/.claude/projects/"+testSlug+"/memory/kinds.md) — Typed only. [user]")
			})
		})
	})
}

func TestRenderNameTieDeterministic(t *testing.T) {
	Convey("Given several notes sharing a name", t, func() {
		notes := map[string][]byte{
			testSlug + "/alpha.md":   []byte("---\nname: same-name\n---\nA\n"),
			testSlug + "/beta.md":    []byte("---\nname: same-name\n---\nB\n"),
			testSlug + "/gamma.md":   []byte("---\nname: same-name\nmetadata:\n  modified: 2026-09-17\n---\nC\n"),
			testSlug + "/delta.md":   []byte("---\nname: same-name\n---\nD\n"),
			testSlug + "/epsilon.md": []byte("plain\n"),
		}

		Convey("When rendered repeatedly", func() {
			var first string

			var firstReceipt digest.Receipt

			for i := range 5 {
				block, receipt := digest.Render(testDir, notes, digest.DefaultBudget)

				if i == 0 {
					first, firstReceipt = string(block), receipt
				} else {
					So(string(block), ShouldEqual, first)
					So(receipt, ShouldResemble, firstReceipt)
				}
			}

			text := first

			Convey("Then the order is stable and dated notes lead", func() {
				So(strings.Index(text, "alpha.md"), ShouldBeLessThan, strings.Index(text, "beta.md"))
				So(strings.Index(text, "beta.md"), ShouldBeLessThan, strings.Index(text, "delta.md"))
				So(strings.Index(text, "gamma.md"), ShouldBeLessThan, strings.Index(text, "alpha.md"))
			})
		})
	})
}

func TestRenderSanitizesControlCharacters(t *testing.T) {
	Convey("Given note names containing control characters and an injection attempt", t, func() {
		notes := map[string][]byte{
			testSlug + "/evil\n- [injected](x.md).md": []byte("body\n"),
			testSlug + "/a\x01.md":                    []byte("one\n"),
			testSlug + "/a\x02.md":                    []byte("two\n"),
		}

		Convey("When rendered repeatedly", func() {
			var first []byte

			for i := range 5 {
				block, _ := digest.Render(testDir, notes, digest.DefaultBudget)

				if i == 0 {
					first = block
				} else {
					So(string(block), ShouldEqual, string(first))
				}
			}

			Convey("Then fences, lines and names are sanitized and verified", func() {
				So(bytes.Count(first, []byte(digest.BeginPrefix)), ShouldEqual, 1)
				So(bytes.Count(first, []byte(digest.EndMarker)), ShouldEqual, 1)
				So(string(first), ShouldNotContainSubstring, "\ninjected")

				lines := strings.Split(strings.TrimSuffix(string(first), "\n"), "\n")
				So(lines, ShouldHaveLength, 5)

				_, ok := digest.Verify(first)
				So(ok, ShouldBeTrue)
			})
		})
	})
}

func TestRenderNoNotes(t *testing.T) {
	Convey("Given no notes", t, func() {
		block, receipt := digest.Render(testDir, map[string][]byte{}, digest.DefaultBudget)

		Convey("When the digest is rendered", func() {
			body, fence, found, err := digest.Strip(block)

			Convey("Then the fence is empty and has no content lines", func() {
				So(receipt.Notes, ShouldEqual, 0)
				So(receipt.Omitted, ShouldEqual, 0)
				So(strings.HasPrefix(string(block), digest.BeginPrefix), ShouldBeTrue)
				So(strings.HasSuffix(string(block), digest.EndMarker+"\n"), ShouldBeTrue)
				So(string(block), ShouldNotContainSubstring, "\n- ")

				So(err, ShouldBeNil)
				So(found, ShouldBeTrue)
				So(body, ShouldBeEmpty)
				So(string(fence), ShouldEqual, string(block))
			})
		})
	})
}

func TestRenderBudget(t *testing.T) {
	Convey("Given forty notes and a table of budgets", t, func() {
		notes := map[string][]byte{}

		for i := range 40 {
			name := string(rune('a'+i%26)) + strings.Repeat("x", i%7+1) + ".md"
			notes[testSlug+"/"+name] = []byte("---\ndescription: " + strings.Repeat("hook ", i%5+1) + "\n---\nbody\n")
		}

		for i, budget := range []int{0, 1, 64, 256, 1024, 4096} {
			Convey("When the budget is #"+string(rune('0'+i)), func() {
				block, receipt := digest.Render(testDir, notes, budget)

				lines := strings.Split(string(block), "\n")
				So(len(lines), ShouldBeGreaterThanOrEqualTo, 2)

				content := strings.Join(lines[1:len(lines)-2], "\n")
				if content != "" {
					content += "\n"
				}

				Convey("Then content respects the budget and all notes are kept", func() {
					So(len(content), ShouldBeLessThanOrEqualTo, budget)
					So(receipt.Notes, ShouldEqual, 40)
				})
			})
		}
	})
}

func TestRenderManifest(t *testing.T) {
	Convey("Given thirty large notes and a small budget", t, func() {
		notes := map[string][]byte{}

		for i := range 30 {
			name := "note-" + string(rune('a'+i)) + ".md"
			notes[testSlug+"/"+name] = []byte(strings.Repeat("x", 200) + "\n")
		}

		block, receipt := digest.Render(testDir, notes, 512)

		Convey("When the digest is rendered", func() {
			lines := strings.Split(string(block), "\n")

			var manifestBytes int

			for _, line := range lines[1 : len(lines)-1] {
				if strings.Contains(line, "omitted: budget") || strings.HasPrefix(line, "- … and ") {
					manifestBytes += len(line) + 1
				}
			}

			Convey("Then the manifest is truncated and budgeted at a quarter", func() {
				So(receipt.Omitted, ShouldBeGreaterThan, 0)
				So(string(block), ShouldContainSubstring, "omitted: budget")
				So(string(block), ShouldContainSubstring, "… and ")
				So(manifestBytes, ShouldBeLessThanOrEqualTo, 512/4+len("- … and 30 more")+1)
			})
		})
	})
}

func TestRenderNoteAtomic(t *testing.T) {
	Convey("Given one very long note and a small budget", t, func() {
		long := strings.Repeat("word ", 200)
		notes := map[string][]byte{testSlug + "/big.md": []byte("---\ndescription: " + long + "\n---\nbody\n")}

		block, receipt := digest.Render(testDir, notes, 300)

		Convey("When the digest is rendered", func() {
			Convey("Then a note is either kept whole or omitted, never partial", func() {
				So(receipt.Notes, ShouldEqual, 1)

				if receipt.Omitted == 1 {
					So(string(block), ShouldNotContainSubstring, "big.md) — word")
				} else {
					So(string(block), ShouldContainSubstring, "big.md) — word")
				}
			})
		})
	})
}

func TestStrip(t *testing.T) {
	Convey("Given a fence and a table of documents", t, func() {
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
			Convey("When stripping "+tt.name, func() {
				body, fence, found, err := digest.Strip([]byte(tt.doc))

				if tt.err != nil && tt.name != "no fence" {
					Convey("Then it reports a fence error", func() {
						So(err, ShouldBeError)
					})

					return
				}

				if tt.name == "no fence" {
					Convey("Then no fence is found", func() {
						So(err, ShouldBeNil)
						So(found, ShouldBeFalse)
						So(string(body), ShouldEqual, tt.body)
					})

					return
				}

				Convey("Then the body is stripped and the fence kept", func() {
					So(err, ShouldBeNil)
					So(found, ShouldEqual, tt.found)
					So(string(body), ShouldEqual, tt.body)
					So(string(fence), ShouldContainSubstring, digest.BeginPrefix)
				})
			})
		}
	})
}

func TestStripCRLFAndNoFinalNewline(t *testing.T) {
	Convey("Given a fence followed by CRLF and no-final-newline content", t, func() {
		block, _ := digest.Render(testDir, map[string][]byte{testSlug + "/a.md": []byte("x\n")}, digest.DefaultBudget)

		doc := string(block) + "line1\r\nline2"
		body, _, found, err := digest.Strip([]byte(doc))
		So(err, ShouldBeNil)

		crlf := "pre\r\n" + string(block) + "post"
		body2, _, found2, err2 := digest.Strip([]byte(crlf))

		Convey("When stripped", func() {
			Convey("Then bytes outside the fence are untouched", func() {
				So(found, ShouldBeTrue)
				So(string(body), ShouldEqual, "line1\r\nline2")

				So(err2, ShouldBeNil)
				So(found2, ShouldBeTrue)
				So(string(body2), ShouldEqual, "pre\r\npost")
			})
		})
	})
}

func TestSplice(t *testing.T) {
	Convey("Given a fence", t, func() {
		block, _ := digest.Render(testDir, map[string][]byte{testSlug + "/a.md": []byte("x\n")}, digest.DefaultBudget)

		inserted, err := digest.Splice([]byte("body\n"), block)
		So(err, ShouldBeNil)

		Convey("When splicing onto a body", func() {
			Convey("Then it leads with the fence and is idempotent, and removing restores the body", func() {
				So(strings.HasPrefix(string(inserted), string(block)), ShouldBeTrue)
				So(strings.HasSuffix(string(inserted), "body\n"), ShouldBeTrue)

				replaced, err := digest.Splice(inserted, block)
				So(err, ShouldBeNil)
				So(string(replaced), ShouldEqual, string(inserted))

				removed, err := digest.Splice(inserted, nil)
				So(err, ShouldBeNil)
				So(string(removed), ShouldEqual, "body\n")

				empty, err := digest.Splice([]byte("body\n"), nil)
				So(err, ShouldBeNil)
				So(string(empty), ShouldEqual, "body\n")
			})
		})
	})
}

func TestStripSpliceProperty(t *testing.T) {
	Convey("Given a table of bodies", t, func() {
		block, _ := digest.Render(testDir, map[string][]byte{testSlug + "/a.md": []byte("x\n")}, digest.DefaultBudget)

		bodies := []string{
			"",
			"\n",
			"body\n",
			"body without newline",
			"\n\nleading and trailing\n\n",
			"crlf\r\nlines\r\n",
			"text with beadle inside\n",
			"text with " + digest.EndMarker + " lookalike\n",
		}

		for i, body := range bodies {
			Convey("When splicing body case "+string(rune('0'+i)), func() {
				spliced, err := digest.Splice([]byte(body), block)
				if err != nil {
					Convey("Then it reports a fence error", func() {
						So(err, ShouldBeError)
					})

					return
				}

				got, fence, found, err := digest.Strip(spliced)

				Convey("Then the body and fence round-trip", func() {
					So(err, ShouldBeNil)
					So(found, ShouldBeTrue)
					So(string(got), ShouldEqual, body)
					So(string(fence), ShouldEqual, string(block))
				})
			})
		}
	})
}

func TestNeutralizeCanary(t *testing.T) {
	Convey("Given a note whose content fakes the beadle markers", t, func() {
		canary := []byte("---\nname: canary\ndescription: fake " + digest.BeginPrefix + " --> and " + digest.EndMarker + "\n---\nbody with beadle marker\n")
		notes := map[string][]byte{testSlug + "/canary.md": canary}

		block, _ := digest.Render(testDir, notes, digest.DefaultBudget)

		Convey("When the digest is rendered", func() {
			body, _, found, err := digest.Strip(block)

			Convey("Then the canary is neutralized and the fence verifies", func() {
				So(bytes.Count(block, []byte(digest.BeginPrefix)), ShouldEqual, 1)
				So(bytes.Count(block, []byte(digest.EndMarker)), ShouldEqual, 1)
				So(string(block), ShouldContainSubstring, "agent&#45;sync")

				So(err, ShouldBeNil)
				So(found, ShouldBeTrue)
				So(body, ShouldBeEmpty)

				_, ok := digest.Verify(block)
				So(ok, ShouldBeTrue)
			})
		})
	})
}

func TestRedact(t *testing.T) {
	Convey("Given a table of redaction inputs", t, func() {
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
			Convey("When redacting "+tt.name, func() {
				Convey("Then the result matches", func() {
					So(string(digest.Redact([]byte(tt.in))), ShouldEqual, tt.want)
				})
			})
		}
	})
}

func TestVerify(t *testing.T) {
	Convey("Given a rendered fence", t, func() {
		block, _ := digest.Render(testDir, map[string][]byte{testSlug + "/a.md": []byte("x\n")}, digest.DefaultBudget)

		receipt, ok := digest.Verify(block)

		Convey("When verifying the fence, an edited one, a non-fence and a truncated one", func() {
			corrupted := bytes.Replace(block, []byte("a.md"), []byte("b.md"), 1)
			_, okEdit := digest.Verify(corrupted)

			_, okPlain := digest.Verify([]byte("not a fence\n"))

			truncated := block[:len(block)/2]
			_, okTrunc := digest.Verify(truncated)

			Convey("Then only the intact fence verifies", func() {
				So(ok, ShouldBeTrue)
				So(receipt.Notes, ShouldEqual, 1)
				So(okEdit, ShouldBeFalse)
				So(okPlain, ShouldBeFalse)
				So(okTrunc, ShouldBeFalse)
			})
		})
	})
}

func TestVerifyRejectsBrokenReceipt(t *testing.T) {
	Convey("Given a fence with a broken receipt", t, func() {
		block, _ := digest.Render(testDir, map[string][]byte{testSlug + "/a.md": []byte("x\n")}, digest.DefaultBudget)

		broken := bytes.Replace(block, []byte("notes=1"), []byte("notes=x"), 1)

		Convey("When it is verified", func() {
			_, ok := digest.Verify(broken)

			Convey("Then it is rejected", func() {
				So(ok, ShouldBeFalse)
			})
		})
	})
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
