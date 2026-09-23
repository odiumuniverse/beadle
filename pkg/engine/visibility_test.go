package engine

import (
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/skill"
)

func canonTrees() map[string]skill.Tree {
	return map[string]skill.Tree{
		"alpha": {"SKILL.md": []byte("# alpha\n")},
	}
}

func driftedTree() skill.Tree {
	return skill.Tree{"SKILL.md": []byte("# alpha v2\n")}
}

// alphaProvider builds one "alpha" copy for the pure resolver tests.
func alphaProvider(class providerClass, dir string, digest cas.Hash) provider {
	return provider{class: class, name: "alpha", dir: dir, path: filepath.Join(dir, "alpha"), digest: digest}
}

func forkWarnings(vis visibility) []string {
	var warnings []string

	for _, warn := range vis.Warnings {
		if strings.Contains(warn, "differs between the canon") {
			warnings = append(warnings, warn)
		}
	}

	return warnings
}

type visibilityCase struct {
	name      string
	providers []provider
	caps      agent.SkillCaps
	ch        channels
	covered   bool
	released  bool
	forks     int
	chosen    providerClass
}

// visibilityCases is the resolver truth table: classes, digests, caps and
// channels.
func visibilityCases(alpha, drifted cas.Hash) []visibilityCase {
	return []visibilityCase{
		{
			name:      "a foreign symlink holds the same bytes",
			providers: []provider{alphaProvider(classForeign, "/foreign", alpha)},
			covered:   true,
			released:  true,
		},
		{
			name:      "a foreign copy drifted",
			providers: []provider{alphaProvider(classForeign, "/foreign", drifted)},
			forks:     1,
		},
		{
			name:      "a beadle-owned real directory matches",
			providers: []provider{alphaProvider(classOwn, "/own", alpha)},
			covered:   true,
			chosen:    classBundle,
			ch:        channels{bundle: true},
		},
		{
			name:      "a beadle-owned copy covers only a live bundle",
			providers: []provider{alphaProvider(classOwn, "/own", alpha)},
			covered:   false, // enable withdraws the copy; the bundle must keep the name
		},
		{
			name:      "a read-only symlink recorded in the base matches",
			providers: []provider{alphaProvider(classForeign, "/own", alpha)},
			covered:   true,
			released:  true,
		},
		{
			name:      "a shared copy matches",
			providers: []provider{alphaProvider(classShared, "/shared", alpha)},
			covered:   true,
		},
		{
			name:      "a farm link matches",
			providers: []provider{alphaProvider(classFarm, "/own", alpha)},
			covered:   true,
		},
		{
			name:      "the rendered bundle copy matches",
			providers: []provider{alphaProvider(classBundle, "/bundle", alpha)},
			covered:   false, // a bundle copy never covers itself
			chosen:    classBundle,
			ch:        channels{bundle: true},
		},
		{
			name:      "the rendered bundle copy is stale",
			providers: []provider{alphaProvider(classBundle, "/bundle", drifted)},
			forks:     0, // staleness is reported by the freshness check, not as a fork
			chosen:    classBundle,
			ch:        channels{bundle: true},
		},
		{
			name:      "a copy of another name is ignored",
			providers: []provider{{class: classForeign, name: "beta", dir: "/foreign", path: "/foreign/beta", digest: alpha}},
		},
		{
			name: "a shadowing host shows the matching winner",
			providers: []provider{
				alphaProvider(classOwn, "/own", alpha),
				alphaProvider(classForeign, "/foreign", drifted),
			},
			caps:     agent.SkillCaps{Shadowing: true, ReadOrder: []string{"/own", "/foreign"}},
			covered:  true,
			released: false,
			forks:    0, // the drifting copy is not visible
			chosen:   classBundle,
			ch:       channels{bundle: true},
		},
		{
			name: "a shadowing host hides the drift of the loser",
			providers: []provider{
				alphaProvider(classOwn, "/own", drifted),
				alphaProvider(classForeign, "/foreign", alpha),
			},
			caps:     agent.SkillCaps{Shadowing: true, ReadOrder: []string{"/own", "/foreign"}},
			covered:  false, // the visible winner drifts
			released: false, // the matching foreign copy is not visible
			forks:    1,
		},
		{
			name: "a namespaced bundle stays visible next to the collapse",
			providers: []provider{
				alphaProvider(classBundle, "/bundle", alpha),
				alphaProvider(classForeign, "/own", drifted),
			},
			caps:    agent.SkillCaps{Shadowing: true, NamespacedBundle: true, ReadOrder: []string{"/own"}},
			covered: false,
			forks:   1,
			chosen:  classBundle,
			ch:      channels{bundle: true},
		},
		{
			name: "a shadowing host keeps a hidden foreign copy from releasing the own copy",
			providers: []provider{
				alphaProvider(classOwn, "/own", alpha),
				alphaProvider(classForeign, "/foreign", alpha),
			},
			caps:     agent.SkillCaps{Shadowing: true, ReadOrder: []string{"/own", "/foreign"}},
			covered:  true,
			released: false, // the foreign copy is shadowed by the own winner
		},
		{
			name:      "no copies at all",
			providers: nil,
			ch:        channels{},
		},
	}
}

func TestResolveVisibility(t *testing.T) {
	canon := canonTrees()

	alpha := skill.TreeDigest(canon["alpha"])
	drifted := skill.TreeDigest(driftedTree())

	for _, tc := range visibilityCases(alpha, drifted) {
		Convey("Given "+tc.name, t, func() {
			vis := resolveVisibility(canon, tc.providers, tc.caps, tc.ch)

			Convey("Then the visibility decision matches", func() {
				_, covered := vis.bundleCoverage().Skills["alpha"]
				So(covered, ShouldEqual, tc.covered)

				_, released := vis.foreignCoverage()["alpha"]
				So(released, ShouldEqual, tc.released)

				So(forkWarnings(vis), ShouldHaveLength, tc.forks)

				So(vis.Skills["alpha"].Chosen, ShouldEqual, tc.chosen)
			})
		})
	}
}

func TestVisibleBundleFallback(t *testing.T) {
	canon := canonTrees()
	digest := skill.TreeDigest(canon["alpha"])

	caps := agent.SkillCaps{Shadowing: true, NamespacedBundle: false}

	Convey("Given a shadowing host with only an un-namespaced bundle copy", t, func() {
		vis := resolveVisibility(canon, []provider{alphaProvider(classBundle, "/bundle", digest)}, caps, channels{bundle: true})

		Convey("When the visible set is computed", func() {
			visible := vis.Skills["alpha"].visible(caps, vis.order())

			Convey("Then the bundle copy is what the host shows", func() {
				So(visible, ShouldHaveLength, 1)
				So(visible[0].class, ShouldEqual, classBundle)
			})
		})
	})

	Convey("Given a shadowing host with a file copy next to the bundle copy", t, func() {
		vis := resolveVisibility(canon, []provider{
			alphaProvider(classOwn, "/own", digest),
			alphaProvider(classBundle, "/bundle", digest),
		}, caps, channels{bundle: true})

		Convey("When the visible set is computed", func() {
			visible := vis.Skills["alpha"].visible(caps, vis.order())

			Convey("Then the file winner shadows the un-namespaced bundle copy", func() {
				So(visible, ShouldHaveLength, 1)
				So(visible[0].class, ShouldEqual, classOwn)
			})
		})
	})
}

func TestResolveVisibilityChannels(t *testing.T) {
	cases := []struct {
		name   string
		ch     channels
		chosen providerClass
	}{
		{"the bundle channel wins the priority", channels{bundle: true, own: true, shared: true}, classBundle},
		{"own wins over shared", channels{own: true, shared: true}, classOwn},
		{"shared is the last channel", channels{shared: true}, classShared},
		{"no channel is enabled", channels{}, ""},
	}

	for _, tc := range cases {
		Convey("Given "+tc.name, t, func() {
			vis := resolveVisibility(canonTrees(), nil, agent.SkillCaps{}, tc.ch)

			Convey("Then the delivery channel matches", func() {
				So(vis.Skills["alpha"].Chosen, ShouldEqual, tc.chosen)
			})
		})
	}
}

func TestResolveVisibilityForkWarningListsProviders(t *testing.T) {
	Convey("Given two drifting copies of one canon skill", t, func() {
		vis := resolveVisibility(canonTrees(), []provider{
			alphaProvider(classForeign, "/a", skill.TreeDigest(driftedTree())),
			alphaProvider(classForeign, "/b", skill.TreeDigest(driftedTree())),
		}, agent.SkillCaps{}, channels{})

		Convey("When the warnings are rendered", func() {
			So(forkWarnings(vis), ShouldHaveLength, 1)

			Convey("Then one warning names both providers", func() {
				So(vis.Warnings[0], ShouldContainSubstring, "/a/alpha")
				So(vis.Warnings[0], ShouldContainSubstring, "/b/alpha")
				So(vis.Warnings[0], ShouldContainSubstring, "alpha")
			})
		})
	})
}

func TestGroupMatchesBase(t *testing.T) {
	base := map[string][]byte{
		"alpha/SKILL.md":     []byte("# alpha\n"),
		"alpha/docs/one.txt": []byte("one\n"),
	}

	cases := []struct {
		name     string
		snapshot map[string][]byte
		base     map[string][]byte
		want     bool
	}{
		{"an untouched copy matches", base, base, true},
		{
			"an edited file does not match",
			map[string][]byte{"alpha/SKILL.md": []byte("# edited\n"), "alpha/docs/one.txt": []byte("one\n")},
			base, false,
		},
		{
			"a partial copy does not match the full base",
			map[string][]byte{"alpha/SKILL.md": []byte("# alpha\n")},
			base, false,
		},
		{
			"a copy without a base never matches",
			base, nil, false,
		},
	}

	for _, tc := range cases {
		Convey("Given "+tc.name, t, func() {
			Convey("When the copy is compared with the base", func() {
				files := groupFiles(tc.snapshot, "alpha")
				So(groupMatchesBase(tc.snapshot, tc.base, "alpha", files), ShouldEqual, tc.want)
			})
		})
	}
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
