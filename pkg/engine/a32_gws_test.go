package engine_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func TestProjectClaudeRulesRoundTrip(t *testing.T) {
	Convey("Given a Claude project with .claude/rules and foreign files", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.ClaudeCodeID)

		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, ".claude/rules")

		write(t, filepath.Join(repo, ".claude", "rules", "style.md"), "# style\n")
		write(t, filepath.Join(repo, ".claude", "rules", "notes.txt"), "foreign\n")
		write(t, filepath.Join(repo, ".claude", "rules", "legacy.mdc"), "# cursor dialect\n")

		report := f.sync(t)

		Convey("When canon edits flow out and a canon deletion removes a file", func() {
			So(report.Errors(), ShouldBeEmpty)
			So(read(t, projectCanon(f, repo, ".claude/rules/style.md")), ShouldEqual, "# style\n")
			So(read(t, filepath.Join(repo, ".claude", "rules", "style.md")), ShouldEqual, "# style\n")
			So(read(t, filepath.Join(repo, ".claude", "rules", "notes.txt")), ShouldEqual, "foreign\n")
			So(read(t, filepath.Join(repo, ".claude", "rules", "legacy.mdc")), ShouldEqual, "# cursor dialect\n")

			write(t, projectCanon(f, repo, ".claude/rules/style.md"), "# style v2\n")

			f.sync(t)
			So(read(t, filepath.Join(repo, ".claude", "rules", "style.md")), ShouldEqual, "# style v2\n")

			So(os.Remove(projectCanon(f, repo, ".claude/rules/style.md")), ShouldBeNil)

			f.sync(t)

			_, styleErr := os.Stat(filepath.Join(repo, ".claude", "rules", "style.md"))
			_, notesErr := os.Stat(filepath.Join(repo, ".claude", "rules", "notes.txt"))
			_, legacyErr := os.Stat(filepath.Join(repo, ".claude", "rules", "legacy.mdc"))

			So(errors.Is(styleErr, fs.ErrNotExist), ShouldBeTrue)
			So(notesErr, ShouldBeNil)
			So(legacyErr, ShouldBeNil)
		})

		Convey("Then doctor does not flag the rules directory itself", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)
			So(a32NonInfoIssues(issues, ".claude/rules"), ShouldBeEmpty)
		})
	})
}

func TestProjectClaudeRulesPolicyGate(t *testing.T) {
	Convey("Given a project where .claude/rules is not enabled", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.ClaudeCodeID)

		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, ".mcp.json")

		write(t, filepath.Join(repo, ".claude", "rules", "style.md"), "# style\n")

		Convey("When the project syncs", func() {
			report := f.sync(t)

			Convey("Then the disabled surface stays untouched", func() {
				So(report.Errors(), ShouldBeEmpty)

				_, canonErr := os.Stat(projectCanon(f, repo, ".claude/rules/style.md"))
				So(errors.Is(canonErr, fs.ErrNotExist), ShouldBeTrue)

				So(read(t, filepath.Join(repo, ".claude", "rules", "style.md")), ShouldEqual, "# style\n")

				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(a32NonInfoIssues(issues, ".claude/rules"), ShouldBeEmpty)
			})
		})
	})
}

func TestClaudeUserRulesDoctor(t *testing.T) {
	Convey("Given a non-empty user-level ~/.claude/rules", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, filepath.Join(f.home, ".claude", "rules", "user.md"), "# user rule\n")

		Convey("When a sync and doctor run", func() {
			f.sync(t)

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the user-level file is untouched and the non-goal is a scoped Info", func() {
				So(read(t, filepath.Join(f.home, ".claude", "rules", "user.md")), ShouldEqual, "# user rule\n")
				So(hasIssue(issues, engine.SeverityInfo, "user-level ~/.claude/rules are not synced"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityWarn, "user-level ~/.claude/rules"), ShouldBeFalse)

				scoped := false

				for _, issue := range issues {
					if strings.Contains(issue.Message, "user-level ~/.claude/rules are not synced") {
						scoped = issue.Kind == kind.Rules && issue.Agent == agent.ClaudeCodeID
					}
				}

				So(scoped, ShouldBeTrue)
			})
		})

		Convey("When claude-code is disabled", func() {
			f.config.Disable(agent.ClaudeCodeID)
			So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the out-of-scope rules are silent", func() {
				So(hasIssue(issues, engine.SeverityInfo, "user-level ~/.claude/rules"), ShouldBeFalse)
			})
		})
	})

	Convey("Given an empty user-level ~/.claude/rules", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		So(os.MkdirAll(filepath.Join(f.home, ".claude", "rules"), 0o700), ShouldBeNil)

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then it is silent", func() {
				So(hasIssue(issues, engine.SeverityInfo, "user-level ~/.claude/rules"), ShouldBeFalse)
			})
		})
	})

	Convey("Given a regular file at ~/.claude/rules", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, filepath.Join(f.home, ".claude", "rules"), "not a directory\n")

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then it is silent", func() {
				So(hasIssue(issues, engine.SeverityInfo, "user-level ~/.claude/rules"), ShouldBeFalse)
			})
		})
	})

	Convey("Given a ~/.claude/rules with only dotfiles and subdirectories", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, filepath.Join(f.home, ".claude", "rules", ".DS_Store"), "junk\n")
		So(os.MkdirAll(filepath.Join(f.home, ".claude", "rules", "nested"), 0o700), ShouldBeNil)

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then it is silent", func() {
				So(hasIssue(issues, engine.SeverityInfo, "user-level ~/.claude/rules"), ShouldBeFalse)
			})
		})
	})
}

// a32NonInfoIssues returns the error/warn issues mentioning substr.

func TestProjectClaudeAndCursorRulesCoexist(t *testing.T) {
	Convey("Given a project enabling both rules surfaces", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		cursorHome(t, f)
		f.enableAgent(t, agent.ClaudeCodeID)
		f.enableAgent(t, agent.CursorID)

		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, ".claude/rules", ".cursor/rules")

		write(t, filepath.Join(repo, ".claude", "rules", "claude.md"), "# claude\n")
		write(t, filepath.Join(repo, ".cursor", "rules", "cursor.mdc"), "# cursor\n")

		report := f.sync(t)

		Convey("Then each file lands in its own canon slot", func() {
			So(report.Errors(), ShouldBeEmpty)
			So(read(t, projectCanon(f, repo, ".claude/rules/claude.md")), ShouldEqual, "# claude\n")
			So(read(t, projectCanon(f, repo, ".cursor/rules/cursor.mdc")), ShouldEqual, "# cursor\n")

			_, claudeAsMdc := os.Stat(projectCanon(f, repo, ".cursor/rules/claude.md"))
			_, cursorAsMd := os.Stat(projectCanon(f, repo, ".claude/rules/cursor.mdc"))

			So(errors.Is(claudeAsMdc, fs.ErrNotExist), ShouldBeTrue)
			So(errors.Is(cursorAsMd, fs.ErrNotExist), ShouldBeTrue)
		})

		Convey("Then doctor does not flag either rules directory", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)
			So(a32NonInfoIssues(issues, ".claude/rules"), ShouldBeEmpty)
			So(a32NonInfoIssues(issues, ".cursor/rules"), ShouldBeEmpty)
		})
	})
}

func TestProjectSingleFileDirStillFlagged(t *testing.T) {
	Convey("Given a project where a disabled single-file rel is a directory", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.ClaudeCodeID)

		repo := newRepo(t)
		f.useRepo(t, repo)

		So(os.MkdirAll(filepath.Join(repo, ".mcp.json"), 0o750), ShouldBeNil)

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the non-regular target is still an error", func() {
				So(hasIssue(issues, engine.SeverityError, "refusing to touch a non-regular file"), ShouldBeTrue)
			})
		})
	})
}

// a32NonInfoIssues returns the error/warn issues mentioning substr.
func a32NonInfoIssues(issues []engine.Issue, substr string) []engine.Issue {
	var out []engine.Issue

	for _, issue := range issues {
		if issue.Severity != engine.SeverityInfo && strings.Contains(issue.Message, substr) {
			out = append(out, issue)
		}
	}

	return out
}
