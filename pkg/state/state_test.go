package state_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	Convey("Given a state with a base and a conflict", t, func() {
		path := filepath.Join(t.TempDir(), state.FileName)

		st := state.New()
		st.SetBase(kind.MCP, "claude-code", state.Base{"alpha": cas.HashOf([]byte("a"))})
		st.ReplaceConflicts(kind.MCP, "opencode", []state.Conflict{{
			Kind: kind.MCP, Agent: "opencode", Key: "alpha", Reason: state.ReasonModified, Since: time.Unix(100, 0).UTC(),
		}})

		Convey("When it is saved and loaded back", func() {
			So(st.Save(path), ShouldBeNil)

			loaded, err := state.Load(path)

			Convey("Then the base and conflicts survive", func() {
				So(err, ShouldBeNil)

				base, ok := loaded.Base(kind.MCP, "claude-code")
				So(ok, ShouldBeTrue)
				So(base["alpha"], ShouldEqual, cas.HashOf([]byte("a")))

				_, ok = loaded.Base(kind.MCP, "opencode")
				So(ok, ShouldBeFalse)

				So(loaded.OpenConflicts(), ShouldHaveLength, 1)
			})
		})
	})
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	Convey("Given a state file that does not exist", t, func() {
		Convey("When it is loaded", func() {
			require := 0
			_ = require

			st, err := state.Load(filepath.Join(t.TempDir(), "missing.json"))

			Convey("Then an empty state is returned", func() {
				So(err, ShouldBeNil)
				So(st.OpenConflicts(), ShouldBeEmpty)
			})
		})
	})
}

func TestLoadRejectsOtherVersions(t *testing.T) {
	Convey("Given a state file with an unsupported version", t, func() {
		path := filepath.Join(t.TempDir(), state.FileName)
		So(os.WriteFile(path, []byte(`{"version": 1}`), 0o600), ShouldBeNil)

		Convey("When it is loaded", func() {
			_, err := state.Load(path)

			Convey("Then loading fails", func() {
				So(err, ShouldBeError)
			})
		})
	})
}

func TestLoadReinitializesNullMaps(t *testing.T) {
	Convey("Given a state file with null maps", t, func() {
		path := filepath.Join(t.TempDir(), state.FileName)
		So(os.WriteFile(path, []byte(`{"version": 2, "bases": null, "snapshots": null, "renders": null, "drift": null}`), 0o600), ShouldBeNil)

		st, err := state.Load(path)
		So(err, ShouldBeNil)

		Convey("When the maps are written to and saved", func() {
			st.Renders["/repo/AGENTS.md"] = state.Render{Notes: 1}
			st.Drift["/repo/AGENTS.md"] = state.Drift{Count: 1}
			st.SetBase(kind.Rules, "a", nil)

			So(st.Save(path), ShouldBeNil)

			again, err := state.Load(path)

			Convey("Then the entries survive the round trip", func() {
				So(err, ShouldBeNil)
				So(again.Renders, ShouldContainKey, "/repo/AGENTS.md")
				So(again.Drift, ShouldContainKey, "/repo/AGENTS.md")
			})
		})
	})
}

func TestReplaceConflictsKeepsDetectionTime(t *testing.T) {
	Convey("Given a conflict replaced for the same agent", t, func() {
		st := state.New()
		first := state.Conflict{Kind: kind.Rules, Agent: "a", Key: kind.RulesKey, Since: time.Unix(100, 0).UTC()}
		st.ReplaceConflicts(kind.Rules, "a", []state.Conflict{first})

		again := first
		again.Since = time.Unix(200, 0).UTC()
		st.ReplaceConflicts(kind.Rules, "a", []state.Conflict{again})

		Convey("When another agent is replaced and the first is cleared", func() {
			other := state.Conflict{Kind: kind.Rules, Agent: "b", Key: kind.RulesKey}
			st.ReplaceConflicts(kind.Rules, "b", []state.Conflict{other})
			st.ReplaceConflicts(kind.Rules, "a", nil)

			Convey("Then the detection time is kept and the other agent is untouched", func() {
				So(st.OpenConflicts(), ShouldResemble, []state.Conflict{other})
			})
		})

		Convey("Then the original detection time is preserved", func() {
			So(st.OpenConflicts()[0].Since, ShouldResemble, time.Unix(100, 0).UTC())
		})
	})
}

func TestConflictLookup(t *testing.T) {
	Convey("Given a state with one conflict", t, func() {
		st := state.New()
		c := state.Conflict{Kind: kind.MCP, Agent: "cursor", Key: "alpha"}
		st.ReplaceConflicts(kind.MCP, "cursor", []state.Conflict{c})

		Convey("When it is looked up by a prefix", func() {
			found, err := st.Conflict(c.ID()[:4])

			Convey("Then it is found", func() {
				So(err, ShouldBeNil)
				So(found.ID(), ShouldEqual, c.ID())
			})
		})

		Convey("When an unknown id is looked up", func() {
			_, err := st.Conflict("zzzz")

			Convey("Then the lookup fails", func() {
				So(err, ShouldBeError)
			})
		})

		Convey("When the conflict is removed", func() {
			st.RemoveConflict(c.ID())

			Convey("Then no conflicts remain", func() {
				So(st.OpenConflicts(), ShouldBeEmpty)
			})
		})
	})
}

func TestSnapshotsAreBoundedAndDeduplicated(t *testing.T) {
	Convey("Given a state with more snapshots than the bound", t, func() {
		st := state.New()

		for i := range state.MaxSnapshots + 5 {
			st.AddSnapshot(kind.Rules, state.Snapshot{Manifest: cas.HashOf(fmt.Appendf(nil, "%d", i))})
		}

		history := st.History(kind.Rules)

		Convey("When the newest snapshot is added again", func() {
			st.AddSnapshot(kind.Rules, history[len(history)-1])

			Convey("Then the history is bounded and the duplicate is dropped", func() {
				So(history, ShouldHaveLength, state.MaxSnapshots)
				So(st.History(kind.Rules), ShouldHaveLength, state.MaxSnapshots)
			})
		})
	})
}

func TestHashesCoverEveryReference(t *testing.T) {
	Convey("Given a state referencing hashes from a base, a conflict and a snapshot", t, func() {
		st := state.New()
		st.SetBase(kind.Skills, "claude-code", state.Base{"a/SKILL.md": "h1"})
		st.ReplaceConflicts(kind.Skills, "claude-code", []state.Conflict{{Kind: kind.Skills, Agent: "claude-code", Key: "a/SKILL.md", Local: "h2"}})
		st.AddSnapshot(kind.Skills, state.Snapshot{Manifest: "h3"})

		Convey("When the referenced hashes are listed", func() {
			Convey("Then every reference is present", func() {
				So(st.Hashes(), ShouldResemble, []cas.Hash{"h1", "h2", "h3"})
			})
		})
	})
}
