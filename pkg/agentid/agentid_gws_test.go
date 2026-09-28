package agentid_test

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agentid"
)

func TestCanonicalMapsHistoricalIDs(t *testing.T) {
	Convey("Given the historical beadle agent ids", t, func() {
		Convey("Then each maps to the canonical id beadle and verger share", func() {
			So(agentid.Canonical("claude-code"), ShouldEqual, "claude")
			So(agentid.Canonical("gemini-cli"), ShouldEqual, "gemini")
			So(agentid.Canonical("antigravity-cli"), ShouldEqual, "agy")
			So(agentid.Canonical("deepseek-harness"), ShouldEqual, "dsh")
		})
	})
}

func TestCanonicalKeepsCanonicalAndUnknown(t *testing.T) {
	Convey("Given an id that is already canonical", t, func() {
		Convey("Then it is returned unchanged", func() {
			So(agentid.Canonical("claude"), ShouldEqual, "claude")
			So(agentid.Canonical("agy"), ShouldEqual, "agy")
			So(agentid.Canonical("omp"), ShouldEqual, "omp")
		})
	})

	Convey("Given an id beadle does not know", t, func() {
		Convey("Then it is returned unchanged so the caller can report it", func() {
			So(agentid.Canonical("nope"), ShouldEqual, "nope")
		})
	})
}

func TestCanonicalIsIdempotent(t *testing.T) {
	Convey("Given every id in the alias table", t, func() {
		Convey("Then canonicalizing twice yields the canonical id", func() {
			for _, alias := range agentid.Aliases() {
				once := agentid.Canonical(alias)
				So(agentid.Canonical(once), ShouldEqual, once)
			}
		})
	})

	Convey("Given the alias table", t, func() {
		Convey("Then no alias is its own canonical form and no target is an alias", func() {
			targets := map[string]bool{}

			for alias, canonical := range agentid.Aliases() {
				So(canonical, ShouldNotEqual, alias)
				So(targets[canonical], ShouldBeFalse)
				targets[canonical] = true
			}
		})
	})
}

func TestCanonicalTable(t *testing.T) {
	Convey("Given the alias table", t, func() {
		Convey("Then it is the four historical ids of the rename", func() {
			So(agentid.Aliases(), ShouldResemble, map[string]string{
				"claude-code":      "claude",
				"gemini-cli":       "gemini",
				"antigravity-cli":  "agy",
				"deepseek-harness": "dsh",
			})
		})
	})
}

func TestAliasesAreACopy(t *testing.T) {
	Convey("Given the alias table", t, func() {
		table := agentid.Aliases()
		delete(table, "claude-code")

		Convey("Then the package table is untouched", func() {
			So(agentid.Canonical("claude-code"), ShouldEqual, "claude")
		})
	})
}
