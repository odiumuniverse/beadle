package engine_test

import (
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/skill"
)

// TestOpenCodeShadowing pins the verified OpenCode v2.0.12 precedence: the
// host keeps one copy per skill id and the .agents copy shadows the .claude
// one (own config directory comes first, see the agent caps).
func TestOpenCodeShadowing(t *testing.T) {
	Convey("Given matching copies of one skill in the claude and agents read areas", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		setSkillMode(t, f, agent.ClaudeCodeID, config.ModeOff)

		write(t, f.vaultSkill("alpha"), "# alpha\n")
		write(t, f.sharedSkill("alpha"), "# alpha\n")

		foreignSkill(t, f, "alpha", "# alpha\n")

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the duplicate is not a fork and not covered by other tools", func() {
				So(opencodeForkWarnings(issues), ShouldBeEmpty)
				So(hasIssue(issues, engine.SeverityInfo, "covered by other tools"), ShouldBeFalse)
			})

			Convey("When the winner drifts", func() {
				write(t, f.sharedSkill("alpha"), "# alpha v3\n")

				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)

				Convey("Then the fork is warned on the winner only", func() {
					warnings := opencodeForkWarnings(issues)

					So(warnings, ShouldHaveLength, 1)
					So(warnings[0], ShouldContainSubstring, filepath.Join(".agents", "skills", "alpha"))
					So(warnings[0], ShouldNotContainSubstring, filepath.Join(".claude", "skills", "alpha"))
					So(hasIssue(issues, engine.SeverityWarn, "precedence is agent-defined"), ShouldBeFalse)
				})
			})
		})

		Convey("When explain runs", func() {
			rows, err := f.engine.Explain(t.Context(), "alpha")
			So(err, ShouldBeNil)

			digest := string(skill.TreeDigest(skill.Tree{"SKILL.md": []byte("# alpha\n")}))[:12]

			Convey("Then the agents copy wins and the claude copy is a loser", func() {
				var opencode []engine.Explanation

				for _, row := range rows {
					if row.Host == agent.OpenCodeID {
						opencode = append(opencode, row)
					}
				}

				So(opencode, ShouldResemble, []engine.Explanation{
					{Host: agent.OpenCodeID, Channel: "shared", Path: "~/.agents/skills/alpha", Digest: digest, Role: "winner"},
					{Host: agent.OpenCodeID, Channel: "foreign", Path: "~/.claude/skills/alpha", Digest: digest, Role: "loser"},
				})
			})
		})
	})
}

// opencodeForkWarnings filters the doctor's canon-fork warnings for OpenCode.
func opencodeForkWarnings(issues []engine.Issue) []string {
	var warnings []string

	for _, issue := range issues {
		if issue.Agent == agent.OpenCodeID && issue.Severity == engine.SeverityWarn &&
			strings.Contains(issue.Message, "differs between the canon") {
			warnings = append(warnings, issue.Message)
		}
	}

	return warnings
}
