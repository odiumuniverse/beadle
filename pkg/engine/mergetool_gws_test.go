package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
)

func mergetoolRulesConflict(t *testing.T, gitInit bool) (*fixture, state.Conflict) {
	t.Helper()

	f := newFixture(t)
	f.emptyConfigs(t)

	if gitInit {
		gitIn(t, f.vault.Root(), "init", "-q")
	}

	write(t, f.claudeRules(), "# v1\n")
	write(t, f.openCodeRules(), "# v1\n")
	f.sync(t)

	write(t, f.vault.RulesPath(), "# vault v2\n")
	write(t, f.claudeRules(), "# claude v2\n")
	f.sync(t)

	return f, f.conflict(t, kind.Rules, agent.ClaudeCodeID)
}

func TestMergetoolMaterializesConflict(t *testing.T) {
	Convey("Given an open rules conflict in a git vault", t, func() {
		f, c := mergetoolRulesConflict(t, true)

		result, warnings, err := f.engine.Mergetool(t.Context(), c.ID())
		So(err, ShouldBeNil)
		So(warnings, ShouldBeEmpty)
		So(result.State, ShouldEqual, engine.MergetoolActive)
		So(result.Path, ShouldEqual, f.vault.RulesPath())

		Convey("When it is materialized", func() {
			Convey("Then git shows an unmerged path with conflict markers", func() {
				So(gitIn(t, f.vault.Root(), "status", "--porcelain"), ShouldContainSubstring, "UU rules/base.md")

				content := read(t, f.vault.RulesPath())
				So(content, ShouldContainSubstring, "<<<<<<< vault")
				So(content, ShouldContainSubstring, ">>>>>>> agent/"+c.ID())

				head := read(t, filepath.Join(f.vault.Root(), ".git", "MERGE_HEAD"))
				So(strings.TrimSpace(head), ShouldNotBeEmpty)
			})

			Convey("Then every side is audited under its own ref", func() {
				for side, want := range map[string]string{"base": "# v1\n", "vault": "# vault v2\n", "local": "# claude v2\n"} {
					ref := "refs/beadle/mergetool/" + c.ID() + "/" + side
					So(result.Refs, ShouldContain, ref)
					So(gitIn(t, f.vault.Root(), "show", ref+":rules/base.md"), ShouldEqual, want)
				}
			})

			Convey("And a second materialization is refused as mergetool-active", func() {
				_, _, err := f.engine.Mergetool(t.Context(), c.ID())
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "mergetool-active")
				So(gitIn(t, f.vault.Root(), "status", "--porcelain"), ShouldContainSubstring, "UU rules/base.md")
			})
		})
	})
}

func TestMergetoolAbortRestoresAndKeepsRefs(t *testing.T) {
	Convey("Given a materialized rules conflict", t, func() {
		f, c := mergetoolRulesConflict(t, true)

		_, _, err := f.engine.Mergetool(t.Context(), c.ID())
		So(err, ShouldBeNil)

		result, err := f.engine.MergetoolAbort(t.Context(), c.ID())
		So(err, ShouldBeNil)
		So(result.State, ShouldEqual, engine.MergetoolAborted)

		Convey("When it is aborted", func() {
			Convey("Then the index, the working tree and MERGE_HEAD are restored", func() {
				So(read(t, f.vault.RulesPath()), ShouldEqual, "# vault v2\n")
				So(gitIn(t, f.vault.Root(), "status", "--porcelain"), ShouldNotContainSubstring, "UU")
				So(gitIn(t, f.vault.Root(), "ls-files", "-u"), ShouldBeEmpty)

				_, statErr := os.Stat(filepath.Join(f.vault.Root(), ".git", "MERGE_HEAD"))
				So(os.IsNotExist(statErr), ShouldBeTrue)

				_, stateErr := os.Stat(f.vault.MergetoolPath())
				So(os.IsNotExist(stateErr), ShouldBeTrue)
			})

			Convey("Then the audit refs stay and a new materialization works", func() {
				So(gitIn(t, f.vault.Root(), "show-ref"), ShouldContainSubstring, "refs/beadle/mergetool/"+c.ID()+"/base")

				_, _, err := f.engine.Mergetool(t.Context(), c.ID())
				So(err, ShouldBeNil)
				So(gitIn(t, f.vault.Root(), "status", "--porcelain"), ShouldContainSubstring, "UU rules/base.md")

				_, err = f.engine.MergetoolAbort(t.Context(), c.ID())
				So(err, ShouldBeNil)

				_, err = f.engine.MergetoolAbort(t.Context(), c.ID())
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "mergetool-not-active")
			})
		})
	})
}

func TestMergetoolResolveClearsMergeState(t *testing.T) {
	Convey("Given a materialized rules conflict", t, func() {
		f, c := mergetoolRulesConflict(t, true)

		_, _, err := f.engine.Mergetool(t.Context(), c.ID())
		So(err, ShouldBeNil)

		file, err := f.engine.ConflictFile(c)
		So(err, ShouldBeNil)

		write(t, file, "# resolved\n")

		Convey("When it is resolved through the normal take-file path", func() {
			report, err := f.engine.Resolve(t.Context(), []string{c.ID()}, engine.Resolution{Take: engine.TakeFile})
			So(err, ShouldBeNil)
			So(report.Resolved, ShouldResemble, []string{c.ID()})
			So(report.Errors(), ShouldBeEmpty)

			Convey("Then the merge state is gone and the resolution stands", func() {
				So(read(t, f.vault.RulesPath()), ShouldEqual, "# resolved\n")
				So(gitIn(t, f.vault.Root(), "status", "--porcelain"), ShouldNotContainSubstring, "UU")
				So(gitIn(t, f.vault.Root(), "ls-files", "-u"), ShouldBeEmpty)

				_, statErr := os.Stat(filepath.Join(f.vault.Root(), ".git", "MERGE_HEAD"))
				So(os.IsNotExist(statErr), ShouldBeTrue)

				So(f.conflicts(t, kind.Rules, agent.ClaudeCodeID), ShouldBeEmpty)

				f.sync(t)

				So(f.conflicts(t, kind.Rules, agent.ClaudeCodeID), ShouldBeEmpty)
			})
		})
	})
}

func TestMergetoolTakeVaultRestoresVaultSide(t *testing.T) {
	Convey("Given a materialized rules conflict", t, func() {
		f, c := mergetoolRulesConflict(t, true)

		_, _, err := f.engine.Mergetool(t.Context(), c.ID())
		So(err, ShouldBeNil)

		Convey("When it is resolved with --take vault", func() {
			report, err := f.engine.Resolve(t.Context(), []string{c.ID()}, engine.Resolution{Take: engine.TakeVault})
			So(err, ShouldBeNil)
			So(report.Resolved, ShouldResemble, []string{c.ID()})
			So(report.Errors(), ShouldBeEmpty)

			Convey("Then nobody keeps conflict markers", func() {
				So(read(t, f.vault.RulesPath()), ShouldEqual, "# vault v2\n")
				So(read(t, f.claudeRules()), ShouldEqual, "# vault v2\n")
				So(read(t, f.openCodeRules()), ShouldEqual, "# vault v2\n")

				So(gitIn(t, f.vault.Root(), "status", "--porcelain"), ShouldNotContainSubstring, "UU")

				_, statErr := os.Stat(filepath.Join(f.vault.Root(), ".git", "MERGE_HEAD"))
				So(os.IsNotExist(statErr), ShouldBeTrue)
			})
		})
	})
}

func TestMergetoolSyncIsBlockedWhileActive(t *testing.T) {
	Convey("Given a materialized rules conflict", t, func() {
		f, c := mergetoolRulesConflict(t, true)

		_, _, err := f.engine.Mergetool(t.Context(), c.ID())
		So(err, ShouldBeNil)

		claude := read(t, f.claudeRules())
		openCode := read(t, f.openCodeRules())

		Convey("When a plain sync runs", func() {
			_, err := f.engine.Sync(t.Context(), engine.SyncOptions{})

			Convey("Then it refuses to read the marker file as canon", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "mergetool-active")

				So(read(t, f.claudeRules()), ShouldEqual, claude)
				So(read(t, f.openCodeRules()), ShouldEqual, openCode)

				Convey("And abort still works", func() {
					_, err := f.engine.MergetoolAbort(t.Context(), c.ID())
					So(err, ShouldBeNil)
					So(read(t, f.vault.RulesPath()), ShouldEqual, "# vault v2\n")
				})
			})
		})
	})
}

func TestMergetoolAbortRestoresTrackedIndex(t *testing.T) {
	Convey("Given a tracked target path", t, func() {
		f, c := mergetoolRulesConflict(t, true)

		gitIn(t, f.vault.Root(), "-c", "user.name=test", "-c", "user.email=test@example.com", "add", "rules/base.md")
		gitIn(t, f.vault.Root(), "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "baseline")

		tracked := gitIn(t, f.vault.Root(), "ls-files", "-s", "--", "rules/base.md")
		So(tracked, ShouldNotBeEmpty)

		_, _, err := f.engine.Mergetool(t.Context(), c.ID())
		So(err, ShouldBeNil)
		So(gitIn(t, f.vault.Root(), "ls-files", "-u"), ShouldNotBeEmpty)

		_, err = f.engine.MergetoolAbort(t.Context(), c.ID())
		So(err, ShouldBeNil)

		Convey("When it is aborted", func() {
			Convey("Then the captured stage-0 entry is back", func() {
				So(gitIn(t, f.vault.Root(), "ls-files", "-s", "--", "rules/base.md"), ShouldEqual, tracked)
				So(gitIn(t, f.vault.Root(), "status", "--porcelain"), ShouldNotContainSubstring, "UU")
			})
		})
	})
}

func TestMergetoolUnsupportedKind(t *testing.T) {
	Convey("Given an open MCP conflict", t, func() {
		f := newFixture(t)
		f.emptyConfigs(t)

		gitIn(t, f.vault.Root(), "init", "-q")

		write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "v1"}}}`)
		write(t, f.openCodeConfig(), `{"mcp": {"alpha": {"type": "local", "command": ["v2"]}}}`)
		f.sync(t)

		c := f.conflict(t, kind.MCP, agent.OpenCodeID)

		Convey("When mergetool is attempted", func() {
			_, _, err := f.engine.Mergetool(t.Context(), c.ID())
			So(err, ShouldBeError)
			So(err.Error(), ShouldContainSubstring, "mergetool-unsupported")

			Convey("Then nothing is created", func() {
				_, stateErr := os.Stat(f.vault.MergetoolPath())
				So(os.IsNotExist(stateErr), ShouldBeTrue)

				_, headErr := os.Stat(filepath.Join(f.vault.Root(), ".git", "MERGE_HEAD"))
				So(os.IsNotExist(headErr), ShouldBeTrue)

				So(gitIn(t, f.vault.Root(), "status", "--porcelain"), ShouldNotContainSubstring, "UU")
			})
		})
	})
}

func TestMergetoolNeedsGitRepo(t *testing.T) {
	Convey("Given a vault that is not a git repository", t, func() {
		f, c := mergetoolRulesConflict(t, false)

		_, _, err := f.engine.Mergetool(t.Context(), c.ID())
		So(err, ShouldBeError)
		So(err.Error(), ShouldContainSubstring, "git repository")

		Convey("Then the conflict stays untouched", func() {
			So(read(t, f.vault.RulesPath()), ShouldEqual, "# vault v2\n")
			So(f.conflicts(t, kind.Rules, agent.ClaudeCodeID), ShouldHaveLength, 1)
		})
	})
}
