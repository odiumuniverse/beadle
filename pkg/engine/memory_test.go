package engine_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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

func TestMemorySyncRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")
	write(t, claudeMemory(f, "-Users-a", "feedback_x.md"), "# x\n")
	write(t, claudeMemory(f, "-Users-b", "MEMORY.md"), "# b\n")
	write(t, filepath.Join(claudeSlug(f, "-Users-a"), "s.jsonl"), "{}\n")

	report := f.sync(t)
	require.True(t, report.Kind(kind.Memory).VaultChanged)
	require.Equal(t, "# a\n", read(t, vaultMemory(f, "-Users-a", "MEMORY.md")))
	require.Equal(t, "# x\n", read(t, vaultMemory(f, "-Users-a", "feedback_x.md")))
	require.Equal(t, "# b\n", read(t, vaultMemory(f, "-Users-b", "MEMORY.md")))

	report = f.sync(t)
	require.False(t, report.Kind(kind.Memory).VaultChanged)
	require.Equal(t, engine.ActionNoop, report.Action(kind.Memory, agent.ClaudeCodeID))

	write(t, vaultMemory(f, "-Users-a", "MEMORY.md"), "# a2\n")
	f.sync(t)
	require.Equal(t, "# a2\n", read(t, claudeMemory(f, "-Users-a", "MEMORY.md")))

	require.Equal(t, "{}\n", read(t, filepath.Join(claudeSlug(f, "-Users-a"), "s.jsonl")))

	require.NoError(t, os.Remove(claudeMemory(f, "-Users-a", "feedback_x.md")))
	f.sync(t)

	require.NoFileExists(t, vaultMemory(f, "-Users-a", "feedback_x.md"), "an agent deletion reaches the canon")
	require.DirExists(t, filepath.Join(f.vault.MemoryDir(), "-Users-a"), "the slug keeps its remaining notes")
	require.NoFileExists(t, claudeMemory(f, "-Users-a", "feedback_x.md"))

	past := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(vaultMemory(f, "-Users-b", "MEMORY.md"), past, past))

	write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a3\n")
	f.sync(t)

	info, err := os.Stat(vaultMemory(f, "-Users-b", "MEMORY.md"))
	require.NoError(t, err)
	require.True(t, info.ModTime().Equal(past), "an unchanged slug is not rewritten")
	require.Equal(t, "# a3\n", read(t, vaultMemory(f, "-Users-a", "MEMORY.md")))
}

func TestMemoryCanonForeignEntriesSurvive(t *testing.T) {
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
	require.NoError(t, os.Symlink(external, filepath.Join(f.vault.MemoryDir(), "-Users-link")))

	f.sync(t)

	require.Equal(t, "junk\n", read(t, vaultMemory(f, "-Users-a", "junk.txt")))
	require.Equal(t, "deep\n", read(t, filepath.Join(f.vault.MemoryDir(), "-Users-a", "memory", "nested", "deep.md")))
	require.Equal(t, "hidden\n", read(t, filepath.Join(f.vault.MemoryDir(), ".hidden-slug", "note.md")))
	require.Equal(t, "outer\n", read(t, filepath.Join(external, "note.md")))

	info, err := os.Lstat(filepath.Join(f.vault.MemoryDir(), "-Users-link"))
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink)
}

func TestMemorySlugDirDisappears(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")
	write(t, claudeMemory(f, "-Users-b", "MEMORY.md"), "# b\n")
	f.sync(t)

	require.NoError(t, os.RemoveAll(claudeSlug(f, "-Users-b")))

	report := f.sync(t)
	require.Empty(t, report.ConflictsOf(kind.Memory))
	require.NoDirExists(t, filepath.Join(f.vault.MemoryDir(), "-Users-b"))
	require.FileExists(t, vaultMemory(f, "-Users-a", "MEMORY.md"))
}

func TestMemoryMassDeletionGuard(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")
	write(t, claudeMemory(f, "-Users-b", "MEMORY.md"), "# b\n")
	f.sync(t)

	require.NoError(t, os.RemoveAll(claudeSlug(f, "-Users-a")))
	require.NoError(t, os.RemoveAll(claudeSlug(f, "-Users-b")))

	report := f.sync(t)
	require.Len(t, report.ConflictsOf(kind.Memory), 2)
	require.Equal(t, state.ReasonMassDelete, report.ConflictsOf(kind.Memory)[0].Reason)
	require.FileExists(t, vaultMemory(f, "-Users-a", "MEMORY.md"), "a mass deletion waits for confirmation")
	require.FileExists(t, vaultMemory(f, "-Users-b", "MEMORY.md"))
}

func TestMemoryConflictResolvedInEditor(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "v1\n")
	f.sync(t)

	write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "agent-edit\n")
	write(t, vaultMemory(f, "-Users-a", "MEMORY.md"), "vault-edit\n")

	report := f.sync(t)
	require.Len(t, report.ConflictsOf(kind.Memory), 1)
	require.Equal(t, "vault-edit\n", read(t, vaultMemory(f, "-Users-a", "MEMORY.md")))
	require.Equal(t, "agent-edit\n", read(t, claudeMemory(f, "-Users-a", "MEMORY.md")))

	c := f.conflict(t, kind.Memory, agent.ClaudeCodeID)

	file, err := f.engine.ConflictFile(c)
	require.NoError(t, err)
	require.Equal(t, ".md", filepath.Ext(file))
	require.Contains(t, filepath.Base(file), "memory-claude-code-")
	require.Contains(t, read(t, file), ">>>>>>> agent:claude-code")

	write(t, file, "# resolved\n")

	_, err = f.engine.Resolve(t.Context(), []string{c.ID()}, engine.Resolution{Take: engine.TakeFile})
	require.NoError(t, err)

	require.Equal(t, "# resolved\n", read(t, vaultMemory(f, "-Users-a", "MEMORY.md")))
	require.Equal(t, "# resolved\n", read(t, claudeMemory(f, "-Users-a", "MEMORY.md")))
	require.NoFileExists(t, file)
	require.Empty(t, f.conflicts(t, kind.Memory, agent.ClaudeCodeID))
}

func TestMemoryProjectorHidesMissingSlug(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")
	f.sync(t)

	write(t, vaultMemory(f, "-Users-ghost", "MEMORY.md"), "# g\n")

	report := f.sync(t)
	require.False(t, report.Kind(kind.Memory).VaultChanged)
	require.Equal(t, engine.ActionNoop, report.Action(kind.Memory, agent.ClaudeCodeID))
	require.NoDirExists(t, claudeSlug(f, "-Users-ghost"))
	require.Equal(t, "# g\n", read(t, vaultMemory(f, "-Users-ghost", "MEMORY.md")), "the hidden note stays in the canon")

	for _, result := range report.Kind(kind.Memory).Agents {
		require.NotContains(t, result.Note, "did not keep")
	}
}

func TestMemorySymlinkedNoteUntouched(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")
	f.sync(t)

	target := filepath.Join(t.TempDir(), "external.md")
	write(t, target, "outer\n")

	link := claudeMemory(f, "-Users-a", "linked.md")
	require.NoError(t, os.Symlink(target, link))

	f.sync(t)

	require.NoFileExists(t, vaultMemory(f, "-Users-a", "linked.md"))
	require.Equal(t, "outer\n", read(t, target))

	require.NoError(t, os.Remove(vaultMemory(f, "-Users-a", "MEMORY.md")))
	f.sync(t)

	require.NoFileExists(t, claudeMemory(f, "-Users-a", "MEMORY.md"))

	info, err := os.Lstat(link)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink, "a symlinked note is never deleted")
	require.Equal(t, "outer\n", read(t, target))
}

func TestMemoryRestore(t *testing.T) {
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
	require.NoError(t, err)

	require.Equal(t, "# v2\n", read(t, vaultMemory(f, "-Users-a", "MEMORY.md")))
	require.NoFileExists(t, vaultMemory(f, "-Users-a", "extra.md"))
	require.Equal(t, "# v2\n", read(t, claudeMemory(f, "-Users-a", "MEMORY.md")))
	require.NoFileExists(t, claudeMemory(f, "-Users-a", "extra.md"))
}

func TestMemoryKindOff(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	f.config.SetKind(kind.Memory, config.ModeOff)

	write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")

	report := f.sync(t)
	require.Nil(t, report.Kind(kind.Memory))
	require.NoDirExists(t, filepath.Join(f.vault.MemoryDir(), "-Users-a"))
	require.Equal(t, "# a\n", read(t, claudeMemory(f, "-Users-a", "MEMORY.md")))
}

func TestMemoryKindFilter(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")
	write(t, f.claudeRules(), "# r\n")

	report := f.run(t, engine.SyncOptions{Kinds: []kind.ID{kind.Memory}})
	require.NotNil(t, report.Kind(kind.Memory))
	require.Nil(t, report.Kind(kind.Rules))
	require.Equal(t, "# a\n", read(t, vaultMemory(f, "-Users-a", "MEMORY.md")))
}

func TestMCPHiddenKeyWithBaseIsKept(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, f.claudeConfig(), `{"mcpServers": {"alpha": {"type": "stdio", "command": "a"}}}`)
	f.sync(t)
	require.Contains(t, f.servers(t), "alpha")

	write(t, f.vault.ServersPath(), `{"alpha": {"type": "stdio"}}`)
	write(t, f.claudeConfig(), `{"mcpServers": {}}`)

	report := f.sync(t)
	require.Empty(t, report.ConflictsOf(kind.MCP), "a hidden MCP key is not a memory deletion")
	require.Contains(t, f.servers(t), "alpha", "a hidden key with a base stays in the canon")
}

func TestMemoryDeletedSlugWithCanonEditConflicts(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")
	write(t, claudeMemory(f, "-Users-b", "MEMORY.md"), "# b\n")
	f.sync(t)

	write(t, vaultMemory(f, "-Users-a", "MEMORY.md"), "# edited\n")
	require.NoError(t, os.RemoveAll(claudeSlug(f, "-Users-a")))

	report := f.sync(t)
	require.Len(t, report.ConflictsOf(kind.Memory), 1)
	require.Equal(t, state.ReasonDeleted, report.ConflictsOf(kind.Memory)[0].Reason)
	require.Equal(t, "# edited\n", read(t, vaultMemory(f, "-Users-a", "MEMORY.md")))
}

func TestMemorySymlinkedCanonRootFailsBeforeWrites(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")
	f.sync(t)

	external := t.TempDir()
	require.NoError(t, os.Rename(f.vault.MemoryDir(), filepath.Join(external, "memory")))
	require.NoError(t, os.Symlink(filepath.Join(external, "memory"), f.vault.MemoryDir()))

	require.NoError(t, os.RemoveAll(claudeSlug(f, "-Users-a")))

	report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, report.Errors())
	require.Equal(t, "# a\n", read(t, filepath.Join(external, "memory", "-Users-a", "MEMORY.md")), "nothing is removed through the symlink")
}

func TestMemorySymlinkedProjectsFailsWithoutDeletion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")
	f.sync(t)

	target := filepath.Join(t.TempDir(), "projects")
	require.NoError(t, os.Rename(claudeProjects(f), target))
	require.NoError(t, os.Symlink(target, claudeProjects(f)))

	report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, report.Errors())
	require.Empty(t, report.ConflictsOf(kind.Memory))
	require.Equal(t, "# a\n", read(t, vaultMemory(f, "-Users-a", "MEMORY.md")), "a symlinked projects directory must not delete canon notes")
}

func TestMemoryWatchPaths(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# a\n")
	require.NoError(t, os.MkdirAll(claudeSlug(f, "-Users-b"), 0o750))
	require.NoError(t, os.MkdirAll(filepath.Join(claudeSlug(f, "-Users-c"), "memory"), 0o750))
	require.NoError(t, os.MkdirAll(filepath.Join(claudeSlug(f, ".hidden-slug"), "memory"), 0o750))
	require.NoError(t, os.MkdirAll(claudeSlug(f, "-Users-d"), 0o750))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(claudeSlug(f, "-Users-d"), "memory")))

	paths, err := f.engine.WatchPaths(t.Context())
	require.NoError(t, err)

	require.Contains(t, paths, f.vault.MemoryDir())
	require.Contains(t, paths, filepath.Join(claudeSlug(f, "-Users-a"), "memory"))
	require.Contains(t, paths, filepath.Join(claudeSlug(f, "-Users-c"), "memory"))
	require.NotContains(t, paths, filepath.Join(f.home, ".claude", "projects"), "projects is never watched recursively")
	require.NotContains(t, paths, filepath.Join(claudeSlug(f, "-Users-b"), "memory"))
	require.NotContains(t, paths, filepath.Join(claudeSlug(f, "-Users-d"), "memory"))
	require.NotContains(t, paths, filepath.Join(claudeSlug(f, ".hidden-slug"), "memory"))
}
