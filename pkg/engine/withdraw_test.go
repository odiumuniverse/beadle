package engine

import (
	"context"
	"errors"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
)

type failingSurface struct{ err error }

func (f failingSurface) Kind() kind.ID        { return kind.MCP }
func (f failingSurface) Path() string         { return "fake" }
func (f failingSurface) WatchPaths() []string { return nil }
func (f failingSurface) Read(context.Context) (agent.Snapshot, error) {
	return agent.Snapshot{}, nil
}

func (f failingSurface) Write(context.Context, kind.Items) error { return f.err }
func (f failingSurface) Traits() agent.Traits                    { return agent.Traits{} }

func TestWithdrawApplyKeepsRecordsOnFailure(t *testing.T) {
	Convey("Given a withdrawal plan whose write fails", t, func() {
		e := &Engine{}

		plan := withdrawPlan{
			kind: kind.MCP, surface: failingSurface{err: errors.New("boom")},
			records: []state.WithdrawnItem{{Kind: kind.MCP, Name: "plug"}},
		}

		records, warns, complete := e.applyBundleWithdrawal(t.Context(), []withdrawPlan{plan})

		Convey("When the withdrawal is applied", func() {
			Convey("Then nothing is recorded as withdrawn and the run is incomplete", func() {
				So(complete, ShouldBeFalse)
				So(records, ShouldBeEmpty)
				So(warns, ShouldHaveLength, 1)
				So(plannedWithdrawn([]withdrawPlan{plan}), ShouldHaveLength, 1)
			})
		})
	})
}

func hashSet(items map[string]string) map[string]cas.Hash {
	out := make(map[string]cas.Hash, len(items))

	for key, value := range items {
		out[key] = cas.HashOf([]byte(value))
	}

	return out
}

func nameSet(names ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))

	for _, name := range names {
		out[name] = struct{}{}
	}

	return out
}

func TestWithdrawPlanSkills(t *testing.T) {
	Convey("Given a canon skill owned by beadle and delivered by the bundle", t, func() {
		snapshot := map[string][]byte{"alpha/SKILL.md": []byte("# alpha\n"), "alpha/docs/note.txt": []byte("n\n")}
		base := hashSet(map[string]string{"alpha/SKILL.md": "# alpha\n", "alpha/docs/note.txt": "n\n"})

		decisions := planWithdrawal(bundle.Claude, kind.Skills, nameSet("alpha"), nameSet("alpha"), snapshot, base, map[string]bool{"alpha": true})

		Convey("When the withdrawal is planned", func() {
			Convey("Then the whole tree leaves and a digest is recorded", func() {
				So(decisions, ShouldHaveLength, 1)
				So(decisions[0].Name, ShouldEqual, "alpha")
				So(decisions[0].Withdraws(), ShouldBeTrue)
				So(decisions[0].Files, ShouldResemble, []string{"alpha/SKILL.md", "alpha/docs/note.txt"})
				So(decisions[0].Digest, ShouldNotBeEmpty)
			})
		})

		Convey("When the shared surface holds no copy", func() {
			decisions := planWithdrawal(bundle.Claude, kind.Skills, nameSet("alpha"), nameSet("alpha"), snapshot, base, map[string]bool{"alpha": false})

			Convey("Then the copy is kept for the other agents", func() {
				So(decisions[0].Withdraws(), ShouldBeFalse)
				So(decisions[0].KeepFor, ShouldEqual, withdrawKeepNoShared)
			})
		})

		Convey("When a file was edited by hand", func() {
			edited := map[string][]byte{"alpha/SKILL.md": []byte("# edited\n"), "alpha/docs/note.txt": []byte("n\n")}
			decisions := planWithdrawal(bundle.Claude, kind.Skills, nameSet("alpha"), nameSet("alpha"), edited, base, map[string]bool{"alpha": true})

			Convey("Then the tree is kept", func() {
				So(decisions[0].KeepFor, ShouldEqual, withdrawKeepModified)
			})
		})

		Convey("When the recorded base knows an extra file", func() {
			partial := hashSet(map[string]string{"alpha/SKILL.md": "# alpha\n"})
			decisions := planWithdrawal(bundle.Claude, kind.Skills, nameSet("alpha"), nameSet("alpha"), snapshot, partial, map[string]bool{"alpha": true})

			Convey("Then the partial tree is never shredded", func() {
				So(decisions[0].KeepFor, ShouldEqual, withdrawKeepModified)
			})
		})

		Convey("When the skill is not owned by beadle", func() {
			decisions := planWithdrawal(bundle.Claude, kind.Skills, nameSet("alpha"), nameSet("alpha"), snapshot, map[string]cas.Hash{}, map[string]bool{"alpha": true})

			Convey("Then it stays", func() {
				So(decisions[0].KeepFor, ShouldEqual, withdrawKeepUnmanaged)
			})
		})

		Convey("When the bundle did not deliver the skill", func() {
			decisions := planWithdrawal(bundle.Claude, kind.Skills, nameSet("alpha"), nameSet(), snapshot, base, map[string]bool{"alpha": true})

			Convey("Then it stays", func() {
				So(decisions[0].KeepFor, ShouldEqual, withdrawKeepUndelivered)
			})
		})

		Convey("When the skill is not in the canon", func() {
			decisions := planWithdrawal(bundle.Claude, kind.Skills, nameSet(), nameSet("alpha"), snapshot, base, map[string]bool{"alpha": true})

			Convey("Then it is not considered", func() {
				So(decisions, ShouldBeEmpty)
			})
		})
	})
}

func TestWithdrawPlanMCP(t *testing.T) {
	Convey("Given a canon server owned by beadle and delivered by the bundle", t, func() {
		snapshot := map[string][]byte{"plug": []byte(`{"command":["node"]}`)}
		base := hashSet(map[string]string{"plug": `{"command":["node"]}`})

		Convey("When the withdrawal is planned", func() {
			decisions := planWithdrawal(bundle.Claude, kind.MCP, nameSet("plug"), nameSet("plug"), snapshot, base, nil)

			Convey("Then the server leaves", func() {
				So(decisions, ShouldHaveLength, 1)
				So(decisions[0].Withdraws(), ShouldBeTrue)
				So(decisions[0].Files, ShouldResemble, []string{"plug"})
			})
		})

		Convey("When the server was edited by hand", func() {
			edited := map[string][]byte{"plug": []byte(`{"command":["other"]}`)}
			decisions := planWithdrawal(bundle.Claude, kind.MCP, nameSet("plug"), nameSet("plug"), edited, base, nil)

			Convey("Then it stays", func() {
				So(decisions[0].KeepFor, ShouldEqual, withdrawKeepModified)
			})
		})

		Convey("When the server is not delivered (unresolved secrets)", func() {
			decisions := planWithdrawal(bundle.Claude, kind.MCP, nameSet("plug"), nameSet(), snapshot, base, nil)

			Convey("Then it stays", func() {
				So(decisions[0].KeepFor, ShouldEqual, withdrawKeepUndelivered)
			})
		})
	})
}
