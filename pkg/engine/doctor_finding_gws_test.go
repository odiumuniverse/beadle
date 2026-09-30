package engine_test

import (
	"encoding/json"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/engine"
)

// A finding must carry everything W7-UX §3.1 requires, because a script
// matches on subject and runs fix, and a human reads the message.
func TestAFindingCarriesSubjectFixAndSafety(t *testing.T) {
	Convey("Given a finding with no explicit subject", t, func() {
		f := engine.Finding{Severity: engine.SeverityWarning, Agent: "claude", Kind: "skills"}

		Convey("Then the subject is derived and stable", func() {
			So(f.Scope(), ShouldEqual, "claude/skills")
			So(f.Scope(), ShouldEqual, f.Scope()) // stable, not order-dependent
		})

		Convey("Then it is not safe to autofix, because it named no fix", func() {
			So(f.CanAutoFix(), ShouldBeFalse)
		})
	})

	Convey("Given a finding with an explicit subject", t, func() {
		f := engine.Finding{Subject: "conflict.abc", Agent: "claude", Kind: "rules"}

		Convey("Then the explicit subject wins over the derived one", func() {
			So(f.Scope(), ShouldEqual, "conflict.abc")
		})
	})

	Convey("Given a finding with a fix that is not safe", t, func() {
		f := engine.Finding{
			Subject: "conflict.abc",
			Fix:     [][]string{{"beadle", "resolve", "abc"}},
		}

		Convey("Then --fix will not apply it, because the choice is the user's", func() {
			So(f.SafeToAutofix, ShouldBeFalse)
			So(f.CanAutoFix(), ShouldBeFalse)
		})
	})

	Convey("Given a finding marked safe with a fix", t, func() {
		f := engine.Finding{
			Subject:       "daemon.installed",
			Fix:           [][]string{{"beadle", "daemon", "install"}},
			SafeToAutofix: true,
		}

		Convey("Then --fix will apply it", func() {
			So(f.CanAutoFix(), ShouldBeTrue)
		})
	})

	Convey("Given a finding marked safe with no fix", t, func() {
		f := engine.Finding{Subject: "daemon.installed", SafeToAutofix: true}

		Convey("Then it is not applicable: a fix is what would be run", func() {
			So(f.CanAutoFix(), ShouldBeFalse)
		})
	})
}

// The severity vocabulary is unified: warn is gone, warning is the word.
func TestSeverityIsWarningNotWarn(t *testing.T) {
	Convey("Given the severity constants", t, func() {
		Convey("Then the old warn name resolves to the new word", func() {
			So(engine.SeverityWarn, ShouldEqual, "warning")
			So(engine.SeverityWarning, ShouldEqual, "warning")
			So(engine.SeverityError, ShouldEqual, "error")
			So(engine.SeverityInfo, ShouldEqual, "info")
		})
	})
}

// The JSON shape is the contract a script reads, so it is pinned field by
// field rather than by a golden blob that would not catch a renamed key.
func TestFindingJSONHasTheSchema(t *testing.T) {
	Convey("Given a fully populated finding", t, func() {
		f := engine.Finding{
			Severity:      engine.SeverityWarning,
			Subject:       "daemon.installed",
			Message:       "the beadle watcher is not installed",
			Fix:           [][]string{{"beadle", "daemon", "install"}},
			SafeToAutofix: true,
		}

		encoded, err := json.Marshal(f)
		So(err, ShouldBeNil)

		var doc map[string]any
		So(json.Unmarshal(encoded, &doc), ShouldBeNil)

		Convey("Then every §3.1 field is present under its own key", func() {
			So(doc, ShouldContainKey, "severity")
			So(doc, ShouldContainKey, "subject")
			So(doc, ShouldContainKey, "message")
			So(doc, ShouldContainKey, "fix")
			So(doc, ShouldContainKey, "safe_to_autofix")
		})

		Convey("Then fix is a list of argv arrays, not a string", func() {
			fix, ok := doc["fix"].([]any)
			So(ok, ShouldBeTrue)
			So(fix, ShouldHaveLength, 1)

			step, ok := fix[0].([]any)
			So(ok, ShouldBeTrue)
			So(step, ShouldResemble, []any{"beadle", "daemon", "install"})
		})

		Convey("Then safe_to_autofix is a real boolean, not a string", func() {
			So(doc["safe_to_autofix"], ShouldBeTrue)
		})
	})

	Convey("Given a finding with no fix", t, func() {
		encoded, err := json.Marshal(engine.Finding{Subject: "x", Message: "y"})
		So(err, ShouldBeNil)

		var doc map[string]any
		So(json.Unmarshal(encoded, &doc), ShouldBeNil)

		Convey("Then fix is omitted rather than a placeholder", func() {
			_, present := doc["fix"]
			So(present, ShouldBeFalse)
		})
	})
}

// Only findings are reported: a check that passes contributes nothing, so a
// clean doctor is one line rather than a list of successes.
func TestOnlyFindingsAreReported(t *testing.T) {
	Convey("Given an engine over a healthy vault", t, func() {
		e := newFixture(t).engine

		issues, err := e.Doctor(t.Context())

		Convey("Then every issue is a finding, never a success line", func() {
			So(err, ShouldBeNil)

			for _, issue := range issues {
				So(issue.Severity, ShouldNotEqual, "ok")
				So(issue.Message, ShouldNotBeBlank)
				So(issue.Scope(), ShouldNotBeBlank)
			}
		})
	})
}
