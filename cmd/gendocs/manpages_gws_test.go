package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// The release smoke step lints every generated page with mandoc, and a page
// mandoc complains about is a page the release flags. cobra renders the .TH
// header through go-md2man, which emits a date mandoc cannot parse and a title
// in lower case; nothing in doc.GenManHeader configures either, so gendocs
// rewrites the header itself. This test is the check that the rewrite still
// happens: without it, the only thing between a malformed header and a red
// release is a CI run on a macOS runner, and a contributor on Linux never finds
// out at all.
func TestGeneratedManPagesAreMandocCleanAtWarningLevel(t *testing.T) {
	// Skip, never pass: a machine with no mandoc has measured nothing, and a
	// green row here would be a claim nobody made.
	mandoc, err := exec.LookPath("mandoc")
	if err != nil {
		t.Skip("mandoc is not installed; the lint this test exists for cannot run here")
	}

	Convey("Given a freshly generated page set", t, func() {
		dir := t.TempDir()
		So(generate(dir, "beadle", "0.5.0", "Oct 2026"), ShouldBeNil)

		pages, globErr := filepath.Glob(filepath.Join(dir, "*.1"))
		So(globErr, ShouldBeNil)
		So(len(pages), ShouldBeGreaterThan, 0)

		Convey("Then mandoc reports nothing at warning level on any of them", func() {
			// One leaf for the whole set, with the offenders named: 70
			// separate leaves would re-run the generator 70 times to say the
			// same thing, and "mandoc said something" without a page name is
			// not a report.
			var dirty []string

			for _, page := range pages {
				// mandoc is the program under test here and the path came from
				// LookPath, not from a page: the only variable argument is a
				// file this test generated in its own temp dir.
				out, lintErr := exec.CommandContext(t.Context(), mandoc, "-T", "lint", "-W", "warning", page).CombinedOutput() //nolint:gosec // G204: mandoc by name, the only variable is a page this test wrote

				if lintErr != nil || strings.TrimSpace(string(out)) != "" {
					dirty = append(dirty, filepath.Base(page)+": "+firstLine(string(out)))
				}
			}

			So(dirty, ShouldBeEmpty)
		})

		Convey("Then every page carries the header mandoc and whatis can read", func() {
			// The positive half of the claim: the ISO date mandoc accepts, the
			// upper-case title a whatis parser matches, and no sixth field.
			// An empty lint would not tell us the header is the one we meant
			// to write, only that mandoc could live with it.
			for _, page := range pages {
				SoMsg(pageName(page), headerOf(page), ShouldEqual,
					`.TH "BEADLE" "1" "2026-10-01" "beadle 0.5.0" "User Commands"`)
			}
		})

		Convey("Then no page carries a tab at all", func() {
			// Stronger than the lint, on purpose. mandoc only objects to a tab
			// in filled text, so a tab in a block it tolerates would sit here
			// quietly and become the warning the next mandoc release prints on
			// all 70 pages. The normaliser replaces every tab; this is the row
			// that says it still does.
			for _, page := range pages {
				SoMsg(pageName(page), strings.Contains(pageBody(page), "\t"), ShouldBeFalse)
			}
		})
	})
}

// pageBody reads one generated page, and headerOf is its .TH line ("" when the
// page has none). Helpers rather than inline reads because a failure over 70
// files has to name the file it is about.
func pageBody(page string) string {
	data, err := os.ReadFile(page) //nolint:gosec // G304: a page gendocs just wrote into the test's own temp dir
	if err != nil {
		return ""
	}

	return string(data)
}

func headerOf(page string) string {
	for line := range strings.SplitSeq(pageBody(page), "\n") {
		if strings.HasPrefix(line, ".TH ") {
			return line
		}
	}

	return ""
}

func pageName(page string) string {
	return filepath.Base(page)
}

func firstLine(s string) string {
	if before, _, found := strings.Cut(s, "\n"); found {
		return before
	}

	return s
}
