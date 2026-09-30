package cli

import (
	"encoding/json"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// The JSON contract a machine depends on: one envelope, first, and a document
// that is the whole of stdout - a human header or a log line in front of it
// breaks every consumer that pipes the command.
func TestStatusJSONDocument(t *testing.T) {
	Convey("Given an initialized vault", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		isolateTestRoots(t)

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		Convey("When status --json runs", func() {
			stdout, stderr, err := runCLISplit(t, "status", "--json")
			So(err, ShouldBeNil)

			Convey("Then stdout is one parseable document with the shared envelope", func() {
				var doc struct {
					Schema struct {
						Name    string `json:"name"`
						Version int    `json:"version"`
					} `json:"schema"`
					Root   string `json:"root"`
					Agents []struct {
						ID        string            `json:"id"`
						State     string            `json:"state"`
						Installed bool              `json:"installed"`
						Modes     map[string]string `json:"modes"`
					} `json:"agents"`
					Conflicts []struct {
						Kind string `json:"kind"`
					} `json:"conflicts"`
				}

				So(json.Unmarshal([]byte(stdout), &doc), ShouldBeNil)
				So(doc.Schema.Name, ShouldEqual, "beadle.status")
				So(doc.Schema.Version, ShouldEqual, 1)
				So(doc.Root, ShouldNotBeEmpty)
				So(doc.Agents, ShouldNotBeEmpty)
				So(doc.Conflicts, ShouldBeEmpty)
			})

			Convey("Then no human header reaches that channel", func() {
				So(stdout, ShouldNotContainSubstring, "vault:")
				So(stdout, ShouldNotContainSubstring, "agents:")
			})

			Convey("Then the document is the only thing on stdout", func() {
				So(stderr, ShouldBeEmpty)
			})

			Convey("Then the agent words are the user's, not beadle's internals", func() {
				So(stdout, ShouldNotContainSubstring, "surface")
				So(stdout, ShouldNotContainSubstring, "pivot")
				So(stdout, ShouldNotContainSubstring, "canon")
			})
		})
	})
}
