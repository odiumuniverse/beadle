package engine_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
)

func claudeMemory(f *fixture, slug, note string) string {
	return filepath.Join(f.home, ".claude", "projects", slug, "memory", note)
}

func claudeSlug(f *fixture, slug string) string {
	return filepath.Join(f.home, ".claude", "projects", slug)
}

func claudeProjects(f *fixture) string {
	return filepath.Join(f.home, ".claude", "projects")
}

func vaultMemory(f *fixture, slug, note string) string {
	return filepath.Join(f.vault.MemoryDir(), slug, note)
}

func noFile(t *testing.T, path string) {
	t.Helper()

	_, err := os.Stat(path)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s should not exist (err=%v)", path, err)
	}
}

func TestMemorySyncRoundTrip(t *testing.T) {
	Convey("Given Claude memory notes and junk in the projects tree", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")
		write(t, claudeMemory(f, "-Users-a", "feedback_x.md"), "# x\n")
		write(t, claudeMemory(f, "-Users-b", "MEMORY.md"), "# b\n")
		write(t, filepath.Join(claudeSlug(f, "-Users-a"), "s.jsonl"), "{}\n")

		report := f.sync(t)

		Convey("When it syncs again and edits flow both ways", func() {
			So(report.Kind(kind.Memory).VaultChanged, ShouldBeTrue)
			So(read(t, vaultMemory(f, "-Users-a", "MEMORY.md")), ShouldEqual, "# a\n")
			So(read(t, vaultMemory(f, "-Users-a", "feedback_x.md")), ShouldEqual, "# x\n")
			So(read(t, vaultMemory(f, "-Users-b", "MEMORY.md")), ShouldEqual, "# b\n")

			report = f.sync(t)
			So(report.Kind(kind.Memory).VaultChanged, ShouldBeFalse)
			So(report.Action(kind.Memory, agent.ClaudeCodeID), ShouldEqual, engine.ActionNoop)

			write(t, vaultMemory(f, "-Users-a", "MEMORY.md"), "# a2\n")
			f.sync(t)
			So(read(t, claudeMemory(f, "-Users-a", "MEMORY.md")), ShouldEqual, "# a2\n")
			So(read(t, filepath.Join(claudeSlug(f, "-Users-a"), "s.jsonl")), ShouldEqual, "{}\n")

			Convey("Then an agent deletion reaches the canon and unchanged slugs are untouched", func() {
				So(os.Remove(claudeMemory(f, "-Users-a", "feedback_x.md")), ShouldBeNil)
				f.sync(t)

				noFile(t, vaultMemory(f, "-Users-a", "feedback_x.md"))
				noFile(t, claudeMemory(f, "-Users-a", "feedback_x.md"))

				_, dirErr := os.Stat(filepath.Join(f.vault.MemoryDir(), "-Users-a"))
				So(dirErr, ShouldBeNil)

				past := time.Now().Add(-time.Hour)
				So(os.Chtimes(vaultMemory(f, "-Users-b", "MEMORY.md"), past, past), ShouldBeNil)

				write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a3\n")
				f.sync(t)

				info, err := os.Stat(vaultMemory(f, "-Users-b", "MEMORY.md"))
				So(err, ShouldBeNil)
				So(info.ModTime().Equal(past), ShouldBeTrue)
				So(read(t, vaultMemory(f, "-Users-a", "MEMORY.md")), ShouldEqual, "# a3\n")
			})
		})
	})
}

func TestMemorySecretGateKeepsOrdinaryText(t *testing.T) {
	Convey("Given a memory note with dates, a commit trailer and a session id", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		note := "---\nname: db-notes\ndescription: schema notes\n---\n" +
			"Audit columns: authorizedBy = \"user-42\", authorizedAt = \"2026-01-01\".\n" +
			"Commit trailer: Co-Authored-By: Someone <x@example.com>\n" +
			"Session field: originSessionId = \"9f1c2d3e-aaaa-bbbb-cccc-ddddeeeeffff\"\n"

		write(t, claudeMemory(f, "-Users-a", "note.md"), note)

		Convey("When sync runs", func() {
			f.sync(t)

			Convey("Then the canon keeps the text verbatim and no secret is stored", func() {
				So(read(t, vaultMemory(f, "-Users-a", "note.md")), ShouldEqual, note)
				So(f.engine.Secrets().Names(), ShouldBeEmpty)
			})
		})
	})
}

func TestMemoryCanonForeignEntriesSurvive(t *testing.T) {
	Convey("Given foreign canon entries and a symlinked slug", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")
		f.sync(t)

		write(t, vaultMemory(f, "-Users-a", "junk.txt"), "junk\n")
		write(t, filepath.Join(f.vault.MemoryDir(), "-Users-a", "memory", "nested", "deep.md"), "deep\n")
		write(t, filepath.Join(f.vault.MemoryDir(), ".hidden-slug", "note.md"), "hidden\n")

		external := t.TempDir()
		write(t, filepath.Join(external, "note.md"), "outer\n")
		So(os.Symlink(external, filepath.Join(f.vault.MemoryDir(), "-Users-link")), ShouldBeNil)

		Convey("When sync runs", func() {
			f.sync(t)

			info, err := os.Lstat(filepath.Join(f.vault.MemoryDir(), "-Users-link"))
			So(err, ShouldBeNil)

			Convey("Then foreign entries and the symlink survive", func() {
				So(read(t, vaultMemory(f, "-Users-a", "junk.txt")), ShouldEqual, "junk\n")
				So(read(t, filepath.Join(f.vault.MemoryDir(), "-Users-a", "memory", "nested", "deep.md")), ShouldEqual, "deep\n")
				So(read(t, filepath.Join(f.vault.MemoryDir(), ".hidden-slug", "note.md")), ShouldEqual, "hidden\n")
				So(read(t, filepath.Join(external, "note.md")), ShouldEqual, "outer\n")
				So(info.Mode()&os.ModeSymlink, ShouldNotBeZeroValue)
			})
		})
	})
}

func TestMemorySlugDirDisappears(t *testing.T) {
	Convey("Given one slug whose directory disappears", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")
		write(t, claudeMemory(f, "-Users-b", "MEMORY.md"), "# b\n")
		f.sync(t)

		So(os.RemoveAll(claudeSlug(f, "-Users-b")), ShouldBeNil)

		Convey("When sync runs", func() {
			report := f.sync(t)

			_, dirErr := os.Stat(filepath.Join(f.vault.MemoryDir(), "-Users-b"))

			Convey("Then the slug is dropped without a conflict", func() {
				So(report.ConflictsOf(kind.Memory), ShouldBeEmpty)
				So(errors.Is(dirErr, fs.ErrNotExist), ShouldBeTrue)
				So(read(t, vaultMemory(f, "-Users-a", "MEMORY.md")), ShouldEqual, "# a\n")
			})
		})
	})
}

func TestMemoryMassDeletionGuard(t *testing.T) {
	Convey("Given all slugs deleted at once", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")
		write(t, claudeMemory(f, "-Users-b", "MEMORY.md"), "# b\n")
		f.sync(t)

		So(os.RemoveAll(claudeSlug(f, "-Users-a")), ShouldBeNil)
		So(os.RemoveAll(claudeSlug(f, "-Users-b")), ShouldBeNil)

		Convey("When sync runs", func() {
			report := f.sync(t)

			Convey("Then it waits for confirmation with a mass-delete conflict each", func() {
				So(report.ConflictsOf(kind.Memory), ShouldHaveLength, 2)
				So(report.ConflictsOf(kind.Memory)[0].Reason, ShouldEqual, state.ReasonMassDelete)
				So(read(t, vaultMemory(f, "-Users-a", "MEMORY.md")), ShouldEqual, "# a\n")
				So(read(t, vaultMemory(f, "-Users-b", "MEMORY.md")), ShouldEqual, "# b\n")
			})
		})
	})
}

func TestMemoryConflictResolvedInEditor(t *testing.T) {
	Convey("Given a memory conflict resolved in the editor file", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "v1\n")
		f.sync(t)

		write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "agent-edit\n")
		write(t, vaultMemory(f, "-Users-a", "MEMORY.md"), "vault-edit\n")

		report := f.sync(t)
		So(report.ConflictsOf(kind.Memory), ShouldHaveLength, 1)

		c := f.conflict(t, kind.Memory, agent.ClaudeCodeID)

		file, err := f.engine.ConflictFile(c)
		So(err, ShouldBeNil)
		So(filepath.Ext(file), ShouldEqual, ".md")
		So(filepath.Base(file), ShouldContainSubstring, "memory-claude-code-")
		So(read(t, file), ShouldContainSubstring, ">>>>>>> agent:claude-code")

		write(t, file, "# resolved\n")

		Convey("When the edited file is taken", func() {
			_, err = f.engine.Resolve(t.Context(), []string{c.ID()}, engine.Resolution{Take: engine.TakeFile})
			So(err, ShouldBeNil)

			_, fileErr := os.Stat(file)

			Convey("Then it lands on both sides and the file is gone", func() {
				So(read(t, vaultMemory(f, "-Users-a", "MEMORY.md")), ShouldEqual, "# resolved\n")
				So(read(t, claudeMemory(f, "-Users-a", "MEMORY.md")), ShouldEqual, "# resolved\n")
				So(errors.Is(fileErr, fs.ErrNotExist), ShouldBeTrue)
				So(f.conflicts(t, kind.Memory, agent.ClaudeCodeID), ShouldBeEmpty)
			})
		})
	})
}

func TestMemoryProjectorHidesMissingSlug(t *testing.T) {
	Convey("Given a canon note for a slug that has no directory", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")
		f.sync(t)

		write(t, vaultMemory(f, "-Users-ghost", "MEMORY.md"), "# g\n")

		Convey("When sync runs", func() {
			report := f.sync(t)

			_, dirErr := os.Stat(claudeSlug(f, "-Users-ghost"))

			Convey("Then the hidden note stays in the canon and no slug is fabricated", func() {
				So(report.Kind(kind.Memory).VaultChanged, ShouldBeFalse)
				So(report.Action(kind.Memory, agent.ClaudeCodeID), ShouldEqual, engine.ActionNoop)
				So(errors.Is(dirErr, fs.ErrNotExist), ShouldBeTrue)
				So(read(t, vaultMemory(f, "-Users-ghost", "MEMORY.md")), ShouldEqual, "# g\n")

				for _, result := range report.Kind(kind.Memory).Agents {
					So(result.Note, ShouldNotContainSubstring, "did not keep")
				}
			})
		})
	})
}

func TestMemorySymlinkedNoteUntouched(t *testing.T) {
	Convey("Given a symlinked memory note", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")
		f.sync(t)

		target := filepath.Join(t.TempDir(), "external.md")
		write(t, target, "outer\n")

		link := claudeMemory(f, "-Users-a", "linked.md")
		So(os.Symlink(target, link), ShouldBeNil)

		f.sync(t)

		Convey("When the same-named canon note is removed", func() {
			noFile(t, vaultMemory(f, "-Users-a", "linked.md"))
			So(read(t, target), ShouldEqual, "outer\n")

			So(os.Remove(vaultMemory(f, "-Users-a", "MEMORY.md")), ShouldBeNil)
			f.sync(t)

			info, err := os.Lstat(link)

			Convey("Then the symlinked note is never adopted or deleted", func() {
				noFile(t, claudeMemory(f, "-Users-a", "MEMORY.md"))
				So(err, ShouldBeNil)
				So(info.Mode()&os.ModeSymlink, ShouldNotBeZeroValue)
				So(read(t, target), ShouldEqual, "outer\n")
			})
		})
	})
}

func TestMemoryRestore(t *testing.T) {
	Convey("Given canon versions of a memory note", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# v1\n")
		f.sync(t)

		write(t, vaultMemory(f, "-Users-a", "MEMORY.md"), "# v2\n")
		f.sync(t)

		write(t, vaultMemory(f, "-Users-a", "extra.md"), "# extra\n")
		f.sync(t)

		_, err := f.engine.Restore(t.Context(), kind.Memory, -2)
		So(err, ShouldBeNil)

		Convey("When the previous version is restored", func() {
			Convey("Then the canon and the agent roll back", func() {
				So(read(t, vaultMemory(f, "-Users-a", "MEMORY.md")), ShouldEqual, "# v2\n")
				noFile(t, vaultMemory(f, "-Users-a", "extra.md"))
				So(read(t, claudeMemory(f, "-Users-a", "MEMORY.md")), ShouldEqual, "# v2\n")
				noFile(t, claudeMemory(f, "-Users-a", "extra.md"))
			})
		})
	})
}

func TestMemoryKindOff(t *testing.T) {
	Convey("Given the memory kind disabled", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		f.config.SetKind(kind.Memory, config.ModeOff)

		write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")

		Convey("When sync runs", func() {
			report := f.sync(t)

			_, dirErr := os.Stat(filepath.Join(f.vault.MemoryDir(), "-Users-a"))

			Convey("Then nothing is read or written", func() {
				So(report.Kind(kind.Memory), ShouldBeNil)
				So(errors.Is(dirErr, fs.ErrNotExist), ShouldBeTrue)
				So(read(t, claudeMemory(f, "-Users-a", "MEMORY.md")), ShouldEqual, "# a\n")
			})
		})
	})
}

func TestMemoryKindFilter(t *testing.T) {
	Convey("Given a sync filtered to the memory kind", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")
		write(t, f.claudeRules(), "# r\n")

		Convey("When the filtered sync runs", func() {
			report := f.run(t, engine.SyncOptions{Kinds: []kind.ID{kind.Memory}})

			Convey("Then only memory is reported", func() {
				So(report.Kind(kind.Memory), ShouldNotBeNil)
				So(report.Kind(kind.Rules), ShouldBeNil)
				So(read(t, vaultMemory(f, "-Users-a", "MEMORY.md")), ShouldEqual, "# a\n")
			})
		})
	})
}

func TestMCPHiddenKeyWithBaseIsKept(t *testing.T) {
	Convey("Given an MCP key hidden from the agent but known to the base", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "a"}}}`)
		f.sync(t)
		So(f.servers(t), ShouldContainKey, "alpha")

		write(t, f.vault.ServersPath(), `{"alpha": {"type": "stdio"}}`)
		write(t, f.claudeConfig(), `{"mcpServers": {}}`)

		Convey("When sync runs", func() {
			report := f.sync(t)

			Convey("Then it is neither a memory deletion nor a canon loss", func() {
				So(report.ConflictsOf(kind.MCP), ShouldBeEmpty)
				So(f.servers(t), ShouldContainKey, "alpha")
			})
		})
	})
}

func TestMemoryDeletedSlugWithCanonEditConflicts(t *testing.T) {
	Convey("Given a deleted slug with a canon edit", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")
		write(t, claudeMemory(f, "-Users-b", "MEMORY.md"), "# b\n")
		f.sync(t)

		write(t, vaultMemory(f, "-Users-a", "MEMORY.md"), "# edited\n")
		So(os.RemoveAll(claudeSlug(f, "-Users-a")), ShouldBeNil)

		Convey("When sync runs", func() {
			report := f.sync(t)

			Convey("Then it conflicts as a deletion and the canon keeps the edit", func() {
				So(report.ConflictsOf(kind.Memory), ShouldHaveLength, 1)
				So(report.ConflictsOf(kind.Memory)[0].Reason, ShouldEqual, state.ReasonDeleted)
				So(read(t, vaultMemory(f, "-Users-a", "MEMORY.md")), ShouldEqual, "# edited\n")
			})
		})
	})
}

func TestMemorySymlinkedCanonRootFailsBeforeWrites(t *testing.T) {
	Convey("Given a symlinked memory canon root", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")
		f.sync(t)

		external := t.TempDir()
		So(os.Rename(f.vault.MemoryDir(), filepath.Join(external, "memory")), ShouldBeNil)
		So(os.Symlink(filepath.Join(external, "memory"), f.vault.MemoryDir()), ShouldBeNil)

		So(os.RemoveAll(claudeSlug(f, "-Users-a")), ShouldBeNil)

		Convey("When sync runs", func() {
			report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then it errors and removes nothing through the symlink", func() {
				So(report.Errors(), ShouldNotBeEmpty)
				So(read(t, filepath.Join(external, "memory", "-Users-a", "MEMORY.md")), ShouldEqual, "# a\n")
			})
		})
	})
}

func TestMemorySymlinkedProjectsFailsWithoutDeletion(t *testing.T) {
	Convey("Given a symlinked projects directory", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")
		f.sync(t)

		target := filepath.Join(t.TempDir(), "projects")
		So(os.Rename(claudeProjects(f), target), ShouldBeNil)
		So(os.Symlink(target, claudeProjects(f)), ShouldBeNil)

		Convey("When sync runs", func() {
			report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then it errors without deleting canon notes", func() {
				So(report.Errors(), ShouldNotBeEmpty)
				So(report.ConflictsOf(kind.Memory), ShouldBeEmpty)
				So(read(t, vaultMemory(f, "-Users-a", "MEMORY.md")), ShouldEqual, "# a\n")
			})
		})
	})
}

func TestMemoryWatchPaths(t *testing.T) {
	Convey("Given a projects tree with several slug shapes", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")
		So(os.MkdirAll(claudeSlug(f, "-Users-b"), 0o750), ShouldBeNil)
		So(os.MkdirAll(filepath.Join(claudeSlug(f, "-Users-c"), "memory"), 0o750), ShouldBeNil)
		So(os.MkdirAll(filepath.Join(claudeSlug(f, ".hidden-slug"), "memory"), 0o750), ShouldBeNil)
		So(os.MkdirAll(claudeSlug(f, "-Users-d"), 0o750), ShouldBeNil)
		So(os.Symlink(t.TempDir(), filepath.Join(claudeSlug(f, "-Users-d"), "memory")), ShouldBeNil)

		Convey("When watch paths are listed", func() {
			paths, err := f.engine.WatchPaths(t.Context())
			So(err, ShouldBeNil)

			joined := strings.Join(paths, "\n")

			Convey("Then only real slug memory dirs are watched, never recursively", func() {
				So(paths, ShouldContain, f.vault.MemoryDir())
				So(paths, ShouldContain, filepath.Join(claudeSlug(f, "-Users-a"), "memory"))
				So(paths, ShouldContain, filepath.Join(claudeSlug(f, "-Users-c"), "memory"))

				So(joined, ShouldNotContainSubstring, filepath.Join(f.home, ".claude", "projects")+"\n")
				So(paths, ShouldNotContain, filepath.Join(claudeSlug(f, "-Users-b"), "memory"))
				So(paths, ShouldNotContain, filepath.Join(claudeSlug(f, "-Users-d"), "memory"))
				So(paths, ShouldNotContain, filepath.Join(claudeSlug(f, ".hidden-slug"), "memory"))
			})
		})
	})
}
