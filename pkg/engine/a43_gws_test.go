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
)

func TestClaudeProjectAgentsRoundTrip(t *testing.T) {
	Convey("Given a Claude project with AGENTS.md and a CLAUDE.md twin", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		// Only Claude stays active: the round-trip must go through the Claude
		// project rules surface, not through another host reading AGENTS.md.
		f.config.Disable(agent.OpenCodeID)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.md"), "# project agents\n")
		write(t, filepath.Join(repo, "CLAUDE.md"), "# project agents\n")
		write(t, filepath.Join(repo, "KEEP.md"), "foreign\n")

		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")

		Convey("When the project syncs", func() {
			report := f.sync(t)

			Convey("Then the canon gets one element and the write-back reaches the file", func() {
				So(report.Errors(), ShouldBeEmpty)
				So(read(t, projectCanon(f, repo, "AGENTS.md")), ShouldEqual, "# project agents\n")

				write(t, projectCanon(f, repo, "AGENTS.md"), "# project agents v2\n")

				f.sync(t)
				So(read(t, filepath.Join(repo, "AGENTS.md")), ShouldEqual, "# project agents v2\n")

				Convey("When the canon element is dropped", func() {
					So(os.Remove(projectCanon(f, repo, "AGENTS.md")), ShouldBeNil)

					f.sync(t)

					Convey("Then the managed file goes and the CLAUDE.md twin stays", func() {
						_, err := os.Stat(filepath.Join(repo, "AGENTS.md"))
						So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)

						So(read(t, filepath.Join(repo, "CLAUDE.md")), ShouldEqual, "# project agents\n")
						So(read(t, filepath.Join(repo, "KEEP.md")), ShouldEqual, "foreign\n")
					})
				})
			})
		})
	})
}

func TestClaudeProjectSurfacesCoexist(t *testing.T) {
	Convey("Given a Claude project with three project surfaces", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.md"), "# agents\n")
		write(t, filepath.Join(repo, ".mcp.json"), `{"mcpServers":{"ctx":{"type":"http","url":"https://example.com/mcp"}}}`)
		write(t, filepath.Join(repo, ".claude", "rules", "style.md"), "# style\n")

		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md", ".mcp.json", ".claude/rules")

		report := f.sync(t)

		Convey("Then each surface lands in its own canon slot", func() {
			So(report.Errors(), ShouldBeEmpty)
			So(read(t, projectCanon(f, repo, "AGENTS.md")), ShouldEqual, "# agents\n")
			So(read(t, projectCanon(f, repo, ".mcp.json")), ShouldContainSubstring, "example.com")
			So(read(t, projectCanon(f, repo, ".claude/rules/style.md")), ShouldEqual, "# style\n")
		})
	})
}

func TestClaudeProjectAgentsPolicyOff(t *testing.T) {
	Convey("Given a project whose AGENTS.md is not enabled", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		// Only Claude stays active: the policy gate must be about the Claude
		// surface, not another host reading AGENTS.md.
		f.config.Disable(agent.OpenCodeID)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.md"), "# agents\n")

		f.useRepo(t, repo)
		f.enableProject(t, ".mcp.json")

		report := f.sync(t)

		Convey("Then the file is left alone", func() {
			So(report.Errors(), ShouldBeEmpty)

			_, err := os.Stat(projectCanon(f, repo, "AGENTS.md"))
			So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)

			So(read(t, filepath.Join(repo, "AGENTS.md")), ShouldEqual, "# agents\n")
		})
	})
}

// a43DoctorIssues runs the doctor for one fixture.
func a43DoctorIssues(t *testing.T, f *fixture) []engine.Issue {
	t.Helper()

	issues, err := f.engine.Doctor(t.Context())
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}

	return issues
}

func TestClaudeProjectRulesDoctor(t *testing.T) {
	Convey("Given a project CLAUDE.md without AGENTS.md", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		repo := newRepo(t)
		write(t, filepath.Join(repo, "CLAUDE.md"), "# legacy instructions\n")

		f.useRepo(t, repo)

		Convey("Then the gap is an Info with the real enable path", func() {
			issues := a43DoctorIssues(t, f)

			So(hasIssue(issues, engine.SeverityInfo, "project CLAUDE.md"), ShouldBeTrue)
			So(hasIssue(issues, engine.SeverityInfo, "run `beadle project enable AGENTS.md`"), ShouldBeTrue)
			So(hasIssue(issues, engine.SeverityWarn, "CLAUDE.md"), ShouldBeFalse)
		})
	})

	Convey("Given AGENTS.md and a different CLAUDE.md", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.md"), "# agents\n")
		write(t, filepath.Join(repo, "CLAUDE.md"), "# claude\n")

		f.useRepo(t, repo)

		Convey("Then the double-load risk is an Info", func() {
			issues := a43DoctorIssues(t, f)

			So(hasIssue(issues, engine.SeverityInfo, "Claude Code loads both"), ShouldBeTrue)
			So(hasIssue(issues, engine.SeverityWarn, "CLAUDE.md"), ShouldBeFalse)
		})
	})

	Convey("Given AGENTS.md and an identical CLAUDE.md", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.md"), "# same instructions\n")
		write(t, filepath.Join(repo, "CLAUDE.md"), "# same instructions\n")

		f.useRepo(t, repo)

		Convey("Then it stays silent: one source in two names", func() {
			issues := a43DoctorIssues(t, f)

			So(hasIssue(issues, engine.SeverityInfo, "project CLAUDE.md"), ShouldBeFalse)
		})
	})

	Convey("Given a project with only AGENTS.md", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.md"), "# agents\n")

		f.useRepo(t, repo)

		Convey("Then the check is silent", func() {
			issues := a43DoctorIssues(t, f)

			So(hasIssue(issues, engine.SeverityInfo, "CLAUDE.md"), ShouldBeFalse)
		})
	})
}

func TestClaudeProjectRulesDoctorLocal(t *testing.T) {
	Convey("Given only a project CLAUDE.local.md", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		repo := newRepo(t)
		write(t, filepath.Join(repo, "CLAUDE.local.md"), "# local instructions\n")

		f.useRepo(t, repo)

		Convey("Then the unmanaged local file is an Info", func() {
			issues := a43DoctorIssues(t, f)

			So(hasIssue(issues, engine.SeverityInfo, "project CLAUDE.local.md"), ShouldBeTrue)
			So(hasIssue(issues, engine.SeverityInfo, "keep one source"), ShouldBeTrue)
		})
	})

	Convey("Given identical twins and an unmanaged CLAUDE.local.md", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.md"), "# same instructions\n")
		write(t, filepath.Join(repo, "CLAUDE.md"), "# same instructions\n")
		write(t, filepath.Join(repo, "CLAUDE.local.md"), "# local instructions\n")

		f.useRepo(t, repo)

		Convey("Then the identical twin stays silent and the local file is reported", func() {
			issues := a43DoctorIssues(t, f)

			So(hasIssue(issues, engine.SeverityInfo, "project CLAUDE.md"), ShouldBeFalse)
			So(hasIssue(issues, engine.SeverityInfo, "project CLAUDE.local.md"), ShouldBeTrue)
		})
	})
}

func TestClaudeProjectDiagnosticsOnce(t *testing.T) {
	Convey("Given AGENTS.md read by two active hosts", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.md"), "# agents\n")

		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")

		Convey("Then the project diagnostics appear once", func() {
			issues := a43DoctorIssues(t, f)

			count := 0

			for _, issue := range issues {
				if strings.Contains(issue.Message, "project file AGENTS.md: enabled") {
					count++
				}
			}

			So(count, ShouldEqual, 1)
		})
	})
}
