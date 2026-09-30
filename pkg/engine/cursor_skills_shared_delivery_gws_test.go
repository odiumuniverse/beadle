package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// cursorAlphaSkill is the canon skill both Cursor scenarios deliver: one tree, one
// body, so the only variable in either scenario is the other host.
const cursorAlphaSkill = "---\nname: alpha\ndescription: Alpha skill.\n---\n\nAlpha body.\n"

// cursorSkillsFixture builds a fixture where Cursor writes its own skills
// surface and Claude is enabled with the skills mode asked for. Claude's
// enable is written here rather than inherited from the shared fixture, so the
// dependency these tests are about is stated in the test and not implied by
// whatever newFixture happens to turn on.
func cursorSkillsFixture(t *testing.T, claude config.Mode) *fixture {
	t.Helper()

	f := newFixture(t)
	f.emptyConfigs(t)

	cursorHost(t, f)

	f.config.Enable(agent.ClaudeCodeID)
	f.config.SetMode(agent.ClaudeCodeID, kind.Skills, claude)
	f.config.SetMode(agent.CursorID, kind.Skills, config.ModeSync)

	if err := f.config.Save(f.vault.ConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	write(t, f.vaultSkill("alpha"), cursorAlphaSkill)

	return f
}

// cursorSkillsKind returns the skills section of a sync report.
func cursorSkillsKind(t *testing.T, report *engine.Report) engine.KindReport {
	t.Helper()

	for _, k := range report.Kinds {
		if k.Kind == kind.Skills {
			return k
		}
	}

	t.Fatalf("no skills section in the report: %+v", report.Kinds)

	return engine.KindReport{}
}

// cursorSkillsAgent returns one agent's line in the skills section.
func cursorSkillsAgent(t *testing.T, section engine.KindReport, agentID string) engine.AgentResult {
	t.Helper()

	for _, a := range section.Agents {
		if a.Agent == agentID {
			return a
		}
	}

	t.Fatalf("no %s line in the skills section: %+v", agentID, section.Agents)

	return engine.AgentResult{}
}

// TestCursorSkillsReleaseWhenClaudeDelivers pins the one Cursor behaviour that
// genuinely depends on a second host: Cursor reads ~/.claude/skills natively
// (`pkg/agent/agents.go`, `alsoReads`), so a canon skill Claude has already
// delivered there is a foreign copy in Cursor's read area, and beadle releases
// the duplicate it would otherwise keep writing into ~/.cursor/skills.
//
// Both halves are asserted against the same canon skill. The first sync is the
// baseline (Cursor gets its own copy, because ~/.claude/skills is still empty
// when the visibility is resolved); the second sync is where Claude's delivery
// exists and Cursor's copy goes. Turning Claude's skills surface off restores
// the copy — that contrast is what makes this a dependency on the other host
// and not a fact about Cursor alone.
func TestCursorSkillsReleaseWhenClaudeDelivers(t *testing.T) {
	Convey("Given Cursor writing skills and Claude writing the same canon skill", t, func() {
		f := cursorSkillsFixture(t, config.ModeSync)

		first := f.sync(t)

		cursorCopy := filepath.Join(f.home, ".cursor", "skills", "alpha", "SKILL.md")
		claudeCopy := filepath.Join(f.home, ".claude", "skills", "alpha", "SKILL.md")

		Convey("Then the first sync gives Cursor its own copy, Claude having delivered nothing yet", func() {
			So(read(t, cursorCopy), ShouldEqual, cursorAlphaSkill)
			cursorFirst := cursorSkillsAgent(t, cursorSkillsKind(t, first), agent.CursorID).Changes

			So(cursorFirst, ShouldHaveLength, 1)
			So(cursorFirst[0].Key, ShouldEqual, "alpha/SKILL.md")
			So(cursorFirst[0].Op, ShouldEqual, engine.OpAdded)
		})

		second := f.sync(t)

		Convey("When the canon is synced again", func() {
			Convey("Then Claude's surface carries the skill Cursor reads", func() {
				So(read(t, claudeCopy), ShouldEqual, cursorAlphaSkill)
			})

			Convey("Then Cursor's duplicate is released, not left to diverge", func() {
				_, statErr := os.Stat(cursorCopy)
				So(os.IsNotExist(statErr), ShouldBeTrue)

				cursorSecond := cursorSkillsAgent(t, cursorSkillsKind(t, second), agent.CursorID).Changes

				So(cursorSecond, ShouldHaveLength, 1)
				So(cursorSecond[0].Key, ShouldEqual, "alpha/SKILL.md")
				So(cursorSecond[0].Op, ShouldEqual, engine.OpDeleted)
			})

			Convey("Then the run names the copy that already delivers it", func() {
				// The path is the half that carries the dependency: the copy
				// that releases Cursor's is Claude's, and the line says so.
				line := strings.Join(cursorSkillsKind(t, second).Warnings, "\n")

				So(line, ShouldContainSubstring, "skills: alpha is also delivered by ")
				So(line, ShouldContainSubstring, filepath.Join(".claude", "skills", "alpha"))
				So(line, ShouldContainSubstring, "the beadle copy is not written")
			})

			Convey("Then Claude's own delivery is untouched", func() {
				So(cursorSkillsAgent(t, cursorSkillsKind(t, second), agent.ClaudeCodeID).Changes, ShouldBeEmpty)
				So(read(t, claudeCopy), ShouldEqual, cursorAlphaSkill)
			})
		})
	})

	Convey("Given Cursor writing skills and Claude's skills surface off", t, func() {
		f := cursorSkillsFixture(t, config.ModeOff)

		f.sync(t)
		second := f.sync(t)

		cursorCopy := filepath.Join(f.home, ".cursor", "skills", "alpha", "SKILL.md")

		Convey("When the canon is synced twice more", func() {
			Convey("Then Cursor keeps its own copy and nothing is released", func() {
				// The same canon skill, the same Cursor surface, the same two
				// syncs: the second host's mode is the whole difference, so the
				// release above is a fact about both hosts together.
				So(read(t, cursorCopy), ShouldEqual, cursorAlphaSkill)
				So(cursorSkillsKind(t, second).Warnings, ShouldBeEmpty)
				So(cursorSkillsAgent(t, cursorSkillsKind(t, second), agent.CursorID).Changes, ShouldBeEmpty)
			})
		})
	})
}
