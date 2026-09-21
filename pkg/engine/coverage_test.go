package engine

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/skill"
)

func canonTrees() map[string]skill.Tree {
	return map[string]skill.Tree{
		"alpha": {"SKILL.md": []byte("# alpha\n")},
	}
}

func TestCoverageOf(t *testing.T) {
	Convey("Given a canon with one skill", t, func() {
		canon := canonTrees()

		Convey("When a foreign symlink holds the same bytes", func() {
			cov := coverageOf(canon, []skillCopy{{name: "alpha", provider: "/foreign/alpha", symlink: true, tree: canon["alpha"]}})

			Convey("Then the name is covered", func() {
				So(cov.Skills, ShouldContainKey, "alpha")
				So(cov.Warnings, ShouldBeEmpty)
			})
		})

		Convey("When a foreign copy drifted", func() {
			drifted := skill.Tree{"SKILL.md": []byte("# alpha v2\n")}
			cov := coverageOf(canon, []skillCopy{{name: "alpha", provider: "/foreign/alpha", symlink: true, tree: drifted}})

			Convey("Then it is a fork and the canon copy stays", func() {
				So(cov.Skills, ShouldBeEmpty)
				So(cov.Warnings, ShouldHaveLength, 1)
				So(cov.Warnings[0], ShouldContainSubstring, "differs between the canon")
			})
		})

		Convey("When the copy is a beadle-owned real directory", func() {
			cov := coverageOf(canon, []skillCopy{{name: "alpha", provider: "/own/alpha", owned: true, tree: canon["alpha"]}})

			Convey("Then it is not coverage", func() {
				So(cov.Skills, ShouldBeEmpty)
			})
		})

		Convey("When a read-only symlink is recorded in the base", func() {
			cov := coverageOf(canon, []skillCopy{{name: "alpha", provider: "/own/alpha", symlink: true, owned: true, tree: canon["alpha"]}})

			Convey("Then it still covers: beadle cannot write it", func() {
				So(cov.Skills, ShouldContainKey, "alpha")
			})
		})

		Convey("When the copy lives on the shared surface", func() {
			cov := coverageOf(canon, []skillCopy{{name: "alpha", provider: "/shared/alpha", shared: true, tree: canon["alpha"]}})

			Convey("Then it is not coverage", func() {
				So(cov.Skills, ShouldBeEmpty)
			})
		})

		Convey("When the copy is a farm link", func() {
			cov := coverageOf(canon, []skillCopy{{name: "alpha", provider: "/own/alpha", symlink: true, farm: true, tree: canon["alpha"]}})

			Convey("Then it is not coverage", func() {
				So(cov.Skills, ShouldBeEmpty)
			})
		})

		Convey("When an unmanaged real directory matches", func() {
			cov := coverageOf(canon, []skillCopy{{name: "alpha", provider: "/own/alpha", tree: canon["alpha"]}})

			Convey("Then it covers", func() {
				So(cov.Skills, ShouldContainKey, "alpha")
			})
		})

		Convey("When the copy is not a canon name", func() {
			cov := coverageOf(canon, []skillCopy{{name: "beta", provider: "/own/beta", symlink: true, tree: canon["alpha"]}})

			Convey("Then it is ignored", func() {
				So(cov.Skills, ShouldBeEmpty)
				So(cov.Warnings, ShouldBeEmpty)
			})
		})
	})
}

func TestWithdrawalDeliveredIncludesCovered(t *testing.T) {
	Convey("Given a bundle that renders one skill and a foreign copy covering another", t, func() {
		req := bundle.Request{Skills: map[string]map[string][]byte{"beta": {"SKILL.md": []byte("# beta\n")}}}
		cov := coverage{Skills: map[string]coveredSkill{"alpha": {Name: "alpha"}}}

		Convey("When the delivered set is computed", func() {
			delivered := withdrawalDelivered(kind.Skills, req, cov)

			Convey("Then it holds both", func() {
				So(delivered, ShouldContainKey, "alpha")
				So(delivered, ShouldContainKey, "beta")
			})

			Convey("And a covered name with a beadle-owned leftover withdraws", func() {
				snapshot := map[string][]byte{"alpha/SKILL.md": []byte("# alpha\n")}
				base := hashSet(map[string]string{"alpha/SKILL.md": "# alpha\n"})

				decisions := planWithdrawal(bundle.Claude, kind.Skills, nameSet("alpha"), delivered, snapshot, base, map[string]bool{"alpha": true})

				So(decisions, ShouldHaveLength, 1)
				So(decisions[0].Withdraws(), ShouldBeTrue)
			})
		})
	})
}
