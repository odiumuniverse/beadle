package engine_test

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// A vault with no agent on it is not a vault whose kinds were delivered: there
// was nobody to deliver to. The report has to say which of the two it was, or
// the reader is told a file was written that was not.
func TestSyncOnAVaultWithNoAgentsSaysSo(t *testing.T) {
	Convey("Given a vault with every agent disabled", t, func() {
		f := newFixture(t)

		for id := range f.config.Agents {
			f.config.Disable(id)
		}

		Convey("When it syncs", func() {
			report := f.sync(t)

			Convey("Then the report says no agent was on for the vault", func() {
				So(report.NoActiveAgents, ShouldBeTrue)
			})

			Convey("Then no kind claims a write", func() {
				for _, kr := range report.Kinds {
					for _, result := range kr.Agents {
						So(result.Action, ShouldNotEqual, engine.ActionPushed)
						So(result.Action, ShouldNotEqual, engine.ActionWouldPush)
					}
				}
			})
		})

		Convey("When a vault with an agent on it syncs", func() {
			f.emptyConfigs(t)
			f.config.Enable(agent.ClaudeCodeID)

			report := f.sync(t)

			Convey("Then the report does not claim the vault is agentless", func() {
				So(report.NoActiveAgents, ShouldBeFalse)
			})
		})
	})
}

// A second sync over a vault that has not changed wrote nothing. "delivered"
// for it is the same lie as above, in the other direction: the reader is told a
// write happened when the run was a no-op.
func TestASecondSyncOverAnUnchangedVaultWroteNothing(t *testing.T) {
	Convey("Given a vault with an agent on it and one sync already done", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, f.vaultSkill("alpha"), "# alpha\n")

		first := f.sync(t)

		Convey("When it syncs again", func() {
			second := f.sync(t)

			Convey("Then the first run wrote and the second reports no change", func() {
				So(pushedKinds(first), ShouldNotBeEmpty)
				So(pushedKinds(second), ShouldBeEmpty)
			})

			Convey("Then the no-op run still reports the agent it asked", func() {
				// A no-op is not a silent run: the agent is named, with the
				// action that means "nothing to write", so the table has a row
				// the reader can act on.
				commands := second.Kind(kind.Commands)
				So(commands, ShouldNotBeNil)

				claude, ok := commands.Agent(agent.ClaudeCodeID)
				So(ok, ShouldBeTrue)
				So(claude.Action, ShouldEqual, engine.ActionNoop)
				So(claude.Changes, ShouldBeEmpty)
			})
		})
	})
}

// pushedKinds names the kinds that reported at least one real write, which is
// what "delivered" is supposed to mean.
func pushedKinds(report *engine.Report) []kind.ID {
	var out []kind.ID

	for _, kr := range report.Kinds {
		for _, result := range kr.Agents {
			if result.Action == engine.ActionPushed || result.Action == engine.ActionWouldPush {
				out = append(out, kr.Kind)

				break
			}
		}
	}

	return out
}
