package cli

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// Every --json command prints one object, and every one of them starts with the
// same envelope. A consumer that reads one of them and switches on the name
// must find the same shape in the other tool, so the rule is checked per
// command rather than once in a helper.
func TestEveryJSONCommandCarriesTheSchema(t *testing.T) {
	Convey("Given an initialized vault", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		isolateTestRoots(t)

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		cases := []struct {
			args []string
			name string
		}{
			{[]string{"status", "--json"}, "beadle.status"},
			{[]string{"sync", "--json"}, "beadle.sync"},
			{[]string{"plugins", "list", "--json"}, "beadle.plugins"},
			{[]string{"bundles", "status", "--json"}, "beadle.bundles"},
		}

		for _, tc := range cases {
			Convey("When "+strings.Join(tc.args, " ")+" runs", func() {
				stdout, stderr, err := runCLISplit(t, tc.args...)
				So(err, ShouldBeNil)

				Convey("Then stdout is one object carrying the shared envelope", func() {
					var doc map[string]json.RawMessage

					So(json.Unmarshal([]byte(stdout), &doc), ShouldBeNil)

					raw, ok := doc["schema"]
					So(ok, ShouldBeTrue)

					var schema schemaField

					So(json.Unmarshal(raw, &schema), ShouldBeNil)
					So(schema.Name, ShouldEqual, tc.name)
					So(schema.Version, ShouldEqual, SchemaVersion)
				})

				Convey("Then the schema is the first field on the wire", func() {
					So(strings.Index(stdout, `"schema"`), ShouldBeLessThan, strings.Index(stdout, `"`+firstDataKey(stdout)+`"`))
				})

				Convey("Then the document is the typed one, not the engine's own report", func() {
					// The engine's Report is a working shape: seventeen mostly
					// optional fields. What a command prints is a document with a
					// fixed set of fields, and its vocabulary is the user's.
					var doc map[string]json.RawMessage

					So(json.Unmarshal([]byte(stdout), &doc), ShouldBeNil)

					if tc.name == "beadle.sync" {
						for _, field := range []string{"dry_run", "direction", "kinds", "conflicts", "plugins", "bundles", "rulings", "adoptions", "notes", "warnings"} {
							So(doc, ShouldContainKey, field)
						}
					}

					// No internal action verb ever reaches the document.
					So(stdout, ShouldNotContainSubstring, "would-push")
					So(stdout, ShouldNotContainSubstring, `"action": "pushed"`)
				})

				Convey("Then nothing else shares the channel", func() {
					So(stderr, ShouldBeEmpty)
				})
			})
		}
	})
}

// firstDataKey is the first field after the envelope, so the ordering assertion
// above has something concrete to compare against.
func firstDataKey(document string) string {
	rest := strings.SplitN(document, "},\n", 2)
	if len(rest) < 2 {
		return ""
	}

	for line := range strings.SplitSeq(rest[1], "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "\"schema\"") {
			continue
		}

		name, _, found := strings.Cut(trimmed, ":")
		if found {
			return strings.Trim(name, `"`)
		}
	}

	return ""
}
