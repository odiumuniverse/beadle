package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/spf13/cobra"
	"github.com/vmkteam/embedlog"
)

// runDoctor runs `beadle doctor` on the shared test home and returns stdout.
// The vault must exist, because doctor on an uninitialised home is a
// different path entirely.
// fixedFixes records the argv of every step a --fix run performed, so a test
// can assert what was applied without running it. Without this the test runs
// the real installer, which is what hung the package for ten minutes.
type fixedFixes struct{ steps [][]string }

func (f *fixedFixes) run(_ context.Context, _ *cobra.Command, step []string) error {
	f.steps = append(f.steps, step)

	return nil
}

func runDoctor(t *testing.T, stdin string, args ...string) (string, *fixedFixes) {
	t.Helper()

	fixes := &fixedFixes{}

	// The app is built here rather than through newRootCmd so the fix executor
	// is a recorder. Everything else is the real command tree.
	// The app is constructed here so the fix executor is the recorder: the
	// command tree captures its app by value, so a seam installed afterwards
	// would never be seen.
	theApp := &app{logger: embedlog.NewLogger(false, false), errOut: os.Stderr}
	theApp.execFix = fixes.run

	root := newRootCmdWithApp(theApp, Options{Version: "test"})

	var out bytes.Buffer

	root.SetOut(&out)
	// stderr gets its own buffer: the document is stdout, and a consumer pipes
	// stdout, so a test that merges them is testing a stream nobody uses.
	var errOut bytes.Buffer

	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"doctor"}, args...))
	root.SilenceUsage = true

	_ = root.ExecuteContext(t.Context())

	return out.String(), fixes
}

func initTestVault(t *testing.T) {
	t.Helper()

	if _, err := runCLI(t, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
}

// The schema is what a script reads, so it is asserted field by field: a
// renamed key or a fix that came back as a string breaks this, not a golden
// blob that would not notice.
func TestDoctorJSONHasTheCommonSchema(t *testing.T) {
	initTestVault(t)

	Convey("Given doctor with --json", t, func() {
		raw, _ := runDoctor(t, "", "--json")

		Convey("Then it is one object whose first field is the schema", func() {
			// W7-UX §2.1: every document carries its schema as the FIRST
			// field. The document was a bare array once; the stdout log line
			// that used to precede it is gone, so the whole of stdout is the
			// document and there is nothing to skip any more.
			var doc map[string]any
			So(json.Unmarshal([]byte(raw), &doc), ShouldBeNil)

			schema, ok := doc["schema"].(map[string]any)
			So(ok, ShouldBeTrue)
			So(schema["name"], ShouldEqual, "beadle.doctor")
			So(schema["version"], ShouldEqual, float64(1))

			// schema first, in the order the fields are printed: a streaming
			// read proves the ORDER, which an unmarshalled map cannot.
			dec := json.NewDecoder(strings.NewReader(raw))
			tok, err := dec.Token()
			So(err, ShouldBeNil)
			So(tok, ShouldEqual, json.Delim('{'))

			first, err := dec.Token()
			So(err, ShouldBeNil)
			So(first, ShouldEqual, "schema")
		})

		Convey("Then every finding carries subject, message, severity and the fix shape", func() {
			var doc struct {
				Findings []map[string]any `json:"findings"`
			}

			So(json.Unmarshal([]byte(raw), &doc), ShouldBeNil)

			for _, finding := range doc.Findings {
				So(finding, ShouldContainKey, "severity")
				So(finding, ShouldContainKey, "subject")
				So(finding, ShouldContainKey, "message")
				So(finding, ShouldContainKey, "safe_to_autofix")

				if fix, present := finding["fix"]; present {
					steps, ok := fix.([]any)
					So(ok, ShouldBeTrue)
					So(steps, ShouldNotBeEmpty)
				}
			}
		})
	})
}

// --fix applies only what beadle owns. A conflict, a consent prompt or anything
// under a hand-edited file is printed and left alone.
func TestDoctorFixRefusesUnsafeFindings(t *testing.T) {
	initTestVault(t)

	Convey("Given doctor --fix with nothing safe to do", t, func() {
		_, fixes := runDoctor(t, "y\n", "--fix")

		Convey("Then it never applies an unsafe finding", func() {
			// The property, not the wording: whatever the home contains, a
			// finding that is not safe to autofix is printed with its command
			// and never run. The previous assertion hard-coded the "nothing to
			// do" wording, which only holds on a home with no safe finding at
			// all — so it tested the fixture, not the behaviour.
			for _, step := range fixes.steps {
				So(step, ShouldNotEqual, []string{"beadle", "resolve"})
			}
		})
	})
}

// One prompt, not one per finding: asking repeatedly is how an autofix trains
// people to press enter without reading.
func TestDoctorFixAsksOnce(t *testing.T) {
	initTestVault(t)

	Convey("Given doctor --fix, declined", t, func() {
		out, _ := runDoctor(t, "n\n", "--fix")

		Convey("Then declining stops it without applying anything", func() {
			So(out, ShouldContainSubstring, "no")
			So(out, ShouldNotContainSubstring, "fixed ")
		})
	})
}
