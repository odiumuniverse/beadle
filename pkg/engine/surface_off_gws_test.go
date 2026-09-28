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

// surfaceOffCase is one kind's canon item and the host file beadle writes for
// it, so the same scenario can be pinned for commands, skills and subagents.
type surfaceOffCase struct {
	kind      kind.ID
	vaultPath func(f *fixture) string
	vaultBody string
	hostPath  func(f *fixture) string
	hostDir   func(f *fixture) string
}

// surfaceOffCases lists one case per kind the task names.
func surfaceOffCases() []surfaceOffCase {
	return []surfaceOffCase{
		{
			kind:      kind.Commands,
			vaultPath: func(f *fixture) string { return filepath.Join(f.vault.CommandsDir(), "greet.md") },
			vaultBody: "---\ndescription: Greet\n---\nSay $1.\n",
			hostPath:  func(f *fixture) string { return filepath.Join(f.home, ".claude", "commands", "greet.md") },
			hostDir:   func(f *fixture) string { return filepath.Join(f.home, ".claude", "commands") },
		},
		{
			kind:      kind.Skills,
			vaultPath: func(f *fixture) string { return f.vaultSkill("alpha") },
			vaultBody: "---\nname: alpha\ndescription: Alpha skill.\n---\n\nAlpha body.\n",
			hostPath:  func(f *fixture) string { return f.claudeSkill("alpha") },
			hostDir:   func(f *fixture) string { return filepath.Join(f.home, ".claude", "skills") },
		},
		{
			kind:      kind.Subagents,
			vaultPath: func(f *fixture) string { return filepath.Join(f.vault.SubagentsDir(), "reviewer.md") },
			vaultBody: "---\nname: reviewer\ndescription: Reviews diffs.\n---\n\nReview the diff.\n",
			hostPath:  func(f *fixture) string { return filepath.Join(f.home, ".claude", "agents", "reviewer.md") },
			hostDir:   func(f *fixture) string { return filepath.Join(f.home, ".claude", "agents") },
		},
	}
}

// surfaceOffLine returns the doctor line of one agent and kind, if any.
func surfaceOffLine(issues []engine.Issue, agentID string, k kind.ID) engine.Issue {
	for _, issue := range issues {
		if issue.Agent == agentID && issue.Kind == k && strings.Contains(issue.Message, "surface is off") {
			return issue
		}
	}

	return engine.Issue{}
}

// exists reports whether path is on disk.
func exists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}

// TestSurfaceOffNote pins the W4-SURFACE-OFF-NOTE contract: turning a surface
// off never deletes the files beadle synced earlier, and the doctor says so —
// once per agent and kind, counting only beadle's own files.
func TestSurfaceOffNote(t *testing.T) {
	for _, c := range surfaceOffCases() {
		Convey("Given a synced "+string(c.kind)+" surface that is then turned off", t, func() {
			f := newFixture(t)
			f.emptyConfigs(t)

			write(t, c.vaultPath(f), c.vaultBody)

			f.sync(t)

			hostFile := c.hostPath(f)

			Convey("Then the file is on disk after the sync", func() {
				So(exists(hostFile), ShouldBeTrue)
			})

			f.config.SetMode(agent.ClaudeCodeID, c.kind, config.ModeOff)

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("When doctor runs", func() {
				Convey("Then one Info line names the agent, the kind, the count and the directory", func() {
					line := surfaceOffLine(issues, agent.ClaudeCodeID, c.kind)

					So(line.Severity, ShouldEqual, engine.SeverityInfo)
					So(line.Message, ShouldContainSubstring, "1 file(s) beadle synced earlier remain in "+c.hostDir(f))
					So(line.Message, ShouldContainSubstring,
						"beadle agents mode "+agent.ClaudeCodeID+" "+string(c.kind)+" sync")
				})

				Convey("Then nothing was deleted", func() {
					So(exists(hostFile), ShouldBeTrue)
				})
			})

			Convey("When the surface is turned back on", func() {
				f.config.SetMode(agent.ClaudeCodeID, c.kind, config.ModeSync)

				again, againErr := f.engine.Doctor(t.Context())

				Convey("Then the line is gone", func() {
					So(againErr, ShouldBeNil)
					So(surfaceOffLine(again, agent.ClaudeCodeID, c.kind).Message, ShouldBeEmpty)
				})
			})
		})

		Convey("Given the "+string(c.kind)+" surface is off and its files are gone", t, func() {
			f := newFixture(t)
			f.emptyConfigs(t)

			write(t, c.vaultPath(f), c.vaultBody)

			f.sync(t)

			f.config.SetMode(agent.ClaudeCodeID, c.kind, config.ModeOff)

			So(os.Remove(c.hostPath(f)), ShouldBeNil)

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("When doctor runs", func() {
				Convey("Then nothing is reported as remaining", func() {
					So(surfaceOffLine(issues, agent.ClaudeCodeID, c.kind).Message, ShouldBeEmpty)
				})
			})
		})
	}

	Convey("Given an off surface holding beadle's file and a foreign one", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, filepath.Join(f.vault.CommandsDir(), "greet.md"), "---\ndescription: Greet\n---\nSay $1.\n")

		f.sync(t)

		f.config.SetMode(agent.ClaudeCodeID, kind.Commands, config.ModeOff)

		write(t, filepath.Join(f.home, ".claude", "commands", "foreign.md"), "# Foreign\n\nMine.\n")

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When doctor runs", func() {
			Convey("Then only beadle's own file is counted", func() {
				line := surfaceOffLine(issues, agent.ClaudeCodeID, kind.Commands)

				So(line.Message, ShouldContainSubstring, "1 file(s) beadle synced earlier remain")
				So(line.Message, ShouldNotContainSubstring, "2 file(s)")
			})
		})
	})

	Convey("Given an off surface holding only a file beadle never wrote", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		dir := filepath.Join(f.home, ".claude", "commands")

		write(t, filepath.Join(dir, "foreign.md"), "# Foreign\n\nMine.\n")

		f.config.SetMode(agent.ClaudeCodeID, kind.Commands, config.ModeOff)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When doctor runs", func() {
			Convey("Then a foreign file is never counted as beadle's", func() {
				So(surfaceOffLine(issues, agent.ClaudeCodeID, kind.Commands).Message, ShouldBeEmpty)
			})
		})
	})
}
