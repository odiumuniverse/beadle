package cli

import (
	"encoding/json"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// The three documents this round added --json to. Each one is compared as text,
// because the contract a script depends on is the field set and the envelope,
// not the values inside them.
func TestAgentsJSONDocument(t *testing.T) {
	Convey("Given an initialized vault", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		isolateTestRoots(t)

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		Convey("When agents --json runs", func() {
			stdout, _, err := runCLISplit(t, "agents", "--json")
			So(err, ShouldBeNil)

			var doc struct {
				Schema struct {
					Name    string `json:"name"`
					Version int    `json:"version"`
				} `json:"schema"`
				Agents []struct {
					ID        string            `json:"id"`
					State     string            `json:"state"`
					Installed bool              `json:"installed"`
					Modes     map[string]string `json:"modes"`
				} `json:"agents"`
				Kinds []struct {
					ID    string `json:"id"`
					State string `json:"state"`
				} `json:"kinds"`
			}

			So(json.Unmarshal([]byte(stdout), &doc), ShouldBeNil)
			So(doc.Schema.Name, ShouldEqual, "beadle.agents")
			So(doc.Schema.Version, ShouldEqual, SchemaVersion)

			Convey("Then the agent table is there, in the user's words", func() {
				So(doc.Agents, ShouldNotBeEmpty)
				So(doc.Agents[0].State, ShouldBeIn, "on", "off")
			})

			Convey("Then the resources are listed with a state", func() {
				So(doc.Kinds, ShouldNotBeEmpty)
				So(doc.Kinds[0].State, ShouldBeIn, "on", "off")
			})

			Convey("Then no human header shares the channel", func() {
				So(stdout, ShouldNotContainSubstring, "agents:")
			})
		})
	})
}

func TestHistoryJSONDocument(t *testing.T) {
	Convey("Given an initialized vault", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		isolateTestRoots(t)

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		Convey("When history is asked for as JSON", func() {
			stdout, _, err := runCLISplit(t, "history", "rules", "--json")
			So(err, ShouldBeNil)

			var doc struct {
				Schema struct {
					Name    string `json:"name"`
					Version int    `json:"version"`
				} `json:"schema"`
				Kind      string `json:"kind"`
				Snapshots []struct {
					At       string `json:"at"`
					Manifest string `json:"manifest"`
				} `json:"snapshots"`
			}

			So(json.Unmarshal([]byte(stdout), &doc), ShouldBeNil)
			So(doc.Schema.Name, ShouldEqual, "beadle.history")
			So(doc.Schema.Version, ShouldEqual, SchemaVersion)
			So(doc.Kind, ShouldEqual, "rules")

			Convey("Then the snapshot list is there, empty or not", func() {
				So(doc.Snapshots, ShouldNotBeNil)
			})
		})
	})
}

func TestConflictsJSONDocument(t *testing.T) {
	Convey("Given a vault with no conflicts", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		isolateTestRoots(t)

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		Convey("When conflicts --json runs", func() {
			stdout, _, err := runCLISplit(t, "conflicts", "--json")
			So(err, ShouldBeNil)

			var doc struct {
				Schema struct {
					Name    string `json:"name"`
					Version int    `json:"version"`
				} `json:"schema"`
				Conflicts []any `json:"conflicts"`
			}

			So(json.Unmarshal([]byte(stdout), &doc), ShouldBeNil)
			So(doc.Schema.Name, ShouldEqual, "beadle.conflicts")
			So(doc.Schema.Version, ShouldEqual, SchemaVersion)
			So(doc.Conflicts, ShouldBeEmpty)
		})
	})
}
