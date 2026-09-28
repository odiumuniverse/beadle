package state_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// v2State is a state.json written by the build that still used the historical
// agent ids: per-kind bases, conflicts, refusals and adoptions all carry one.
const v2State = `{
  "version": 2,
  "bases": {
    "mcp": {"claude-code": {"alpha": "h1"}, "opencode": {"alpha": "h2"}},
    "skills": {"gemini-cli": {"beta": "h3"}}
  },
  "conflicts": [
    {"kind": "mcp", "agent": "claude-code", "key": "alpha", "reason": "modified", "since": "2026-01-01T00:00:00Z"},
    {"kind": "skills", "agent": "gemini-cli", "key": "beta", "reason": "modified", "since": "2026-01-01T00:00:00Z"}
  ],
  "refusals": [
    {"at": "2026-01-01T00:00:00Z", "id": "abc", "kind": "mcp", "agent": "antigravity-cli", "key": "gamma", "code": "risky-change", "message": "no"}
  ],
  "adoptions": [
    {"host": "claude-code", "name": "alpha", "provider": "/home/u/.claude/skills/alpha", "at": "2026-01-01T00:00:00Z"},
    {"host": "deepseek-harness", "name": "delta", "provider": "/home/u/.dsh/skills/delta", "at": "2026-01-01T00:00:00Z"}
  ],
  "bundles": {"claude": {"enabled": true}, "antigravity": {"enabled": false}}
}
`

func TestLoadMigratesStateV2AgentIDs(t *testing.T) {
	Convey("Given a v2 state keyed by the historical agent ids", t, func() {
		path := filepath.Join(t.TempDir(), state.FileName)
		So(os.WriteFile(path, []byte(v2State), 0o600), ShouldBeNil)

		Convey("When it is loaded", func() {
			st, err := state.Load(path)
			So(err, ShouldBeNil)

			Convey("Then the per-agent bases are keyed by the canonical id", func() {
				base, ok := st.Base(kind.MCP, "claude")
				So(ok, ShouldBeTrue)
				So(base["alpha"], ShouldEqual, cas.Hash("h1"))

				base, ok = st.Base(kind.Skills, "gemini")
				So(ok, ShouldBeTrue)
				So(base["beta"], ShouldEqual, cas.Hash("h3"))

				base, ok = st.Base(kind.MCP, "opencode")
				So(ok, ShouldBeTrue)
				So(base["alpha"], ShouldEqual, cas.Hash("h2"))
			})

			Convey("And the historical base keys are gone", func() {
				_, ok := st.Base(kind.MCP, "claude-code")
				So(ok, ShouldBeFalse)

				_, ok = st.Base(kind.Skills, "gemini-cli")
				So(ok, ShouldBeFalse)
			})

			Convey("And the conflicts, refusals and adoptions name the canonical id", func() {
				So(st.OpenConflicts(), ShouldHaveLength, 2)
				So(st.Conflicts[0].Agent, ShouldEqual, "claude")
				So(st.Conflicts[1].Agent, ShouldEqual, "gemini")

				So(st.Refusals[0].Agent, ShouldEqual, "agy")

				_, ok := st.AdoptionFor("claude", "alpha")
				So(ok, ShouldBeTrue)

				_, ok = st.AdoptionFor("dsh", "delta")
				So(ok, ShouldBeTrue)

				_, ok = st.AdoptionFor("claude-code", "alpha")
				So(ok, ShouldBeFalse)
			})

			Convey("And the bundle host map is untouched", func() {
				So(st.Bundles["claude"].Enabled, ShouldBeTrue)
				So(st.Bundles["antigravity"].Enabled, ShouldBeFalse)
			})

			Convey("And one note is reported per renamed id", func() {
				notes := strings.Join(st.MigrationNotes(), "\n")
				So(st.MigrationNotes(), ShouldHaveLength, 4)
				So(notes, ShouldContainSubstring, "claude-code")
				So(notes, ShouldContainSubstring, "claude")
				So(notes, ShouldContainSubstring, "gemini-cli")
				So(notes, ShouldContainSubstring, "gemini")
				So(notes, ShouldContainSubstring, "antigravity-cli")
				So(notes, ShouldContainSubstring, "agy")
				So(notes, ShouldContainSubstring, "deepseek-harness")
				So(notes, ShouldContainSubstring, "dsh")
			})

			Convey("And the read alone writes nothing", func() {
				data, readErr := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
				So(readErr, ShouldBeNil)
				So(string(data), ShouldEqual, v2State)
			})

			Convey("When the command saves it, the file carries the canonical ids", func() {
				So(st.Save(path), ShouldBeNil)

				data, readErr := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
				So(readErr, ShouldBeNil)
				So(string(data), ShouldContainSubstring, `"version": 3`)
				So(string(data), ShouldNotContainSubstring, "claude-code")
				So(string(data), ShouldNotContainSubstring, "gemini-cli")
				So(string(data), ShouldNotContainSubstring, "deepseek-harness")

				Convey("And a second load is a no-op without notes or writes", func() {
					info, statErr := os.Stat(path)
					So(statErr, ShouldBeNil)

					beforeTime := info.ModTime()

					again, err := state.Load(path)
					So(err, ShouldBeNil)
					So(again.MigrationNotes(), ShouldBeEmpty)
					So(again.Migrated(), ShouldBeFalse)

					after, readErr := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
					So(readErr, ShouldBeNil)
					So(string(after), ShouldEqual, string(data))

					afterInfo, statErr := os.Stat(path)
					So(statErr, ShouldBeNil)
					So(afterInfo.ModTime().Equal(beforeTime), ShouldBeTrue)
				})
			})
		})
	})
}

func TestLoadMigratesStateIdempotently(t *testing.T) {
	Convey("Given a v2 state that was already migrated once", t, func() {
		path := filepath.Join(t.TempDir(), state.FileName)
		So(os.WriteFile(path, []byte(v2State), 0o600), ShouldBeNil)

		st, err := state.Load(path)
		So(err, ShouldBeNil)
		So(st.Save(path), ShouldBeNil)

		Convey("When it is loaded again", func() {
			again, err := state.Load(path)
			So(err, ShouldBeNil)

			Convey("Then the second pass changes nothing and reports nothing", func() {
				So(again.MigrationNotes(), ShouldBeEmpty)
				So(again.Migrated(), ShouldBeFalse)

				base, ok := again.Base(kind.MCP, "claude")
				So(ok, ShouldBeTrue)
				So(base["alpha"], ShouldEqual, cas.Hash("h1"))

				So(again.OpenConflicts(), ShouldHaveLength, 2)
			})
		})
	})
}

func TestLoadKeepsV3StateUntouched(t *testing.T) {
	Convey("Given a v3 state already keyed by the canonical ids", t, func() {
		path := filepath.Join(t.TempDir(), state.FileName)

		st := state.New()
		st.SetBase(kind.MCP, "claude", state.Base{"alpha": cas.HashOf([]byte("a"))})
		st.Conflicts = []state.Conflict{{
			Kind: kind.MCP, Agent: "claude", Key: "alpha", Reason: state.ReasonModified, Since: time.Unix(100, 0).UTC(),
		}}
		So(st.Save(path), ShouldBeNil)

		Convey("When it is loaded", func() {
			loaded, err := state.Load(path)
			So(err, ShouldBeNil)

			Convey("Then nothing is migrated and nothing is reported", func() {
				So(loaded.Migrated(), ShouldBeFalse)
				So(loaded.MigrationNotes(), ShouldBeEmpty)
				So(loaded.Conflicts[0].Agent, ShouldEqual, "claude")
			})
		})
	})
}

func TestLoadRejectsStateNewerThanCurrent(t *testing.T) {
	Convey("Given a state.json written by a newer beadle", t, func() {
		path := filepath.Join(t.TempDir(), state.FileName)
		So(os.WriteFile(path, []byte(`{"version":4}`), 0o600), ShouldBeNil)

		Convey("When it is loaded", func() {
			loaded, err := state.Load(path)

			Convey("Then it is refused instead of misread", func() {
				So(loaded, ShouldBeNil)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "version 4")
			})
		})
	})
}
