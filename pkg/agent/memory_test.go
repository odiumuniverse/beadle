package agent_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func memorySurfaceOf(t *testing.T, home string) agent.Surface {
	t.Helper()

	return surfaceOf(t, agent.ClaudeCode(home, t.TempDir()), kind.Memory)
}

func projectsDir(home string) string {
	return filepath.Join(home, ".claude", "projects")
}

func memoryDir(home, slug string) string {
	return filepath.Join(projectsDir(home), slug, "memory")
}

func TestMemorySurfaceRead(t *testing.T) {
	t.Parallel()

	home := t.TempDir()

	writeFile(t, filepath.Join(memoryDir(home, "-Users-a"), "MEMORY.md"), "# a\n")
	writeFile(t, filepath.Join(memoryDir(home, "-Users-a"), "feedback_x.md"), "# x\n")
	writeFile(t, filepath.Join(projectsDir(home), "-Users-a", "session.jsonl"), "{}\n")
	writeFile(t, filepath.Join(projectsDir(home), "-Users-a", "abc", "tool-results", "f.jsonl"), "x\n")
	writeFile(t, filepath.Join(memoryDir(home, "-Users-b"), "MEMORY.md"), "# b\n")
	writeFile(t, filepath.Join(memoryDir(home, "-Users-b"), "notes.jsonl"), "{}\n")

	snap, err := memorySurfaceOf(t, home).Read(t.Context())
	require.NoError(t, err)
	require.True(t, snap.Present)
	require.Equal(t, kind.Items{
		"-Users-a/MEMORY.md":     []byte("# a\n"),
		"-Users-a/feedback_x.md": []byte("# x\n"),
		"-Users-b/MEMORY.md":     []byte("# b\n"),
	}, snap.Items)
}

func TestMemorySurfaceReadMissingProjects(t *testing.T) {
	t.Parallel()

	snap, err := memorySurfaceOf(t, t.TempDir()).Read(t.Context())
	require.NoError(t, err)
	require.False(t, snap.Present)
	require.Empty(t, snap.Items)
}

func TestMemorySurfaceReadSkipsSymlinks(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	target := t.TempDir()

	writeFile(t, filepath.Join(target, "note.md"), "# target\n")
	writeFile(t, filepath.Join(target, "MEMORY.md"), "# real\n")

	writeFile(t, filepath.Join(memoryDir(home, "-Users-a"), "MEMORY.md"), "# a\n")
	require.NoError(t, os.Symlink(target, filepath.Join(memoryDir(home, "-Users-a"), "linked.md")))
	require.NoError(t, os.Symlink(target, filepath.Join(projectsDir(home), "linked-slug")))
	require.NoError(t, os.MkdirAll(filepath.Join(projectsDir(home), "-Users-b"), 0o750))
	require.NoError(t, os.Symlink(target, memoryDir(home, "-Users-b")))

	snap, err := memorySurfaceOf(t, home).Read(t.Context())
	require.NoError(t, err)
	require.Equal(t, kind.Items{"-Users-a/MEMORY.md": []byte("# a\n")}, snap.Items)
}

func TestMemorySurfaceReadSymlinkedProjects(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	target := t.TempDir()

	writeFile(t, filepath.Join(target, "-Users-a", "memory", "MEMORY.md"), "# a\n")
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o750))
	require.NoError(t, os.Symlink(target, projectsDir(home)))

	surface := memorySurfaceOf(t, home)

	_, err := surface.Read(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "symlink")
	require.Nil(t, surface.WatchPaths())

	require.NoError(t, os.Remove(projectsDir(home)))
	require.NoError(t, os.WriteFile(projectsDir(home), []byte("x"), 0o600))

	_, err = surface.Read(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a directory")
	require.Nil(t, surface.WatchPaths())
}

func TestMemorySurfaceReadError(t *testing.T) {
	t.Parallel()

	home := t.TempDir()

	note := filepath.Join(memoryDir(home, "-Users-a"), "broken.md")
	writeFile(t, note, "x\n")
	require.NoError(t, os.Chmod(note, 0o000))
	t.Cleanup(func() { _ = os.Chmod(note, 0o600) })

	_, err := memorySurfaceOf(t, home).Read(t.Context())
	require.Error(t, err)
}

func TestMemorySurfaceWrite(t *testing.T) {
	t.Parallel()

	home := t.TempDir()

	writeFile(t, filepath.Join(memoryDir(home, "-Users-a"), "a.md"), "v1\n")
	writeFile(t, filepath.Join(memoryDir(home, "-Users-a"), "b.md"), "stale\n")
	writeFile(t, filepath.Join(projectsDir(home), "-Users-a", "session.jsonl"), "{}\n")

	surface := memorySurfaceOf(t, home)

	err := surface.Write(t.Context(), kind.Items{
		"-Users-a/a.md": []byte("v2\n"),
		"-Users-a/c.md": []byte("new\n"),
		"-Users-z/n.md": []byte("nope\n"),
	})
	require.NoError(t, err)

	require.Equal(t, "v2\n", readFile(t, filepath.Join(memoryDir(home, "-Users-a"), "a.md")))
	require.Equal(t, "new\n", readFile(t, filepath.Join(memoryDir(home, "-Users-a"), "c.md")))
	require.NoFileExists(t, filepath.Join(memoryDir(home, "-Users-a"), "b.md"))
	require.Equal(t, "{}\n", readFile(t, filepath.Join(projectsDir(home), "-Users-a", "session.jsonl")))
	require.NoDirExists(t, filepath.Join(projectsDir(home), "-Users-z"))
	require.DirExists(t, memoryDir(home, "-Users-a"))

	require.NoError(t, surface.Write(t.Context(), kind.Items{}))

	require.NoDirExists(t, filepath.Join(memoryDir(home, "-Users-a"), "a.md"))
	require.DirExists(t, memoryDir(home, "-Users-a"), "an emptied memory directory stays in place")
	require.Equal(t, "{}\n", readFile(t, filepath.Join(projectsDir(home), "-Users-a", "session.jsonl")))
}

func TestMemorySurfaceWriteCreatesMemoryDir(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(projectsDir(home), "-Users-a"), 0o750))

	require.NoError(t, memorySurfaceOf(t, home).Write(t.Context(), kind.Items{"-Users-a/n.md": []byte("x\n")}))

	info, err := os.Stat(memoryDir(home, "-Users-a"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o750), info.Mode().Perm())
	require.Equal(t, "x\n", readFile(t, filepath.Join(memoryDir(home, "-Users-a"), "n.md")))
}

func TestMemorySurfaceWriteSkipsSymlinks(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	target := t.TempDir()

	writeFile(t, filepath.Join(target, "keep.md"), "keep\n")

	require.NoError(t, os.MkdirAll(filepath.Join(projectsDir(home), "-Users-a"), 0o750))
	require.NoError(t, os.Symlink(target, memoryDir(home, "-Users-a")))

	surface := memorySurfaceOf(t, home)
	require.NoError(t, surface.Write(t.Context(), kind.Items{"-Users-a/new.md": []byte("x\n")}))
	require.NoFileExists(t, filepath.Join(target, "new.md"))
	require.Equal(t, "keep\n", readFile(t, filepath.Join(target, "keep.md")))

	writeFile(t, filepath.Join(memoryDir(home, "-Users-b"), "real.md"), "real\n")
	link := filepath.Join(memoryDir(home, "-Users-b"), "linked.md")
	require.NoError(t, os.Symlink(filepath.Join(target, "keep.md"), link))

	require.NoError(t, surface.Write(t.Context(), kind.Items{"-Users-b/linked.md": []byte("changed\n")}))

	info, err := os.Lstat(link)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink)
	require.Equal(t, "keep\n", readFile(t, filepath.Join(target, "keep.md")))
}

func TestMemorySurfaceWriteKeepsEqualNotes(t *testing.T) {
	t.Parallel()

	home := t.TempDir()

	path := filepath.Join(memoryDir(home, "-Users-a"), "same.md")
	writeFile(t, path, "same\n")

	past := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(path, past, past))

	surface := memorySurfaceOf(t, home)
	require.NoError(t, surface.Write(t.Context(), kind.Items{"-Users-a/same.md": []byte("same\n")}))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.True(t, info.ModTime().Equal(past), "an unchanged note must not be rewritten")
}

func TestMemorySurfaceProject(t *testing.T) {
	t.Parallel()

	home := t.TempDir()

	writeFile(t, filepath.Join(memoryDir(home, "-Users-a"), "MEMORY.md"), "# a\n")

	require.NoError(t, os.MkdirAll(filepath.Join(projectsDir(home), "-Users-b"), 0o750))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(projectsDir(home), "-Users-c")))

	surface := memorySurfaceOf(t, home)
	projector, ok := surface.(agent.Projector)
	require.True(t, ok)

	key, value, visible := projector.Project("-Users-a/MEMORY.md", []byte("# a\n"))
	require.True(t, visible)
	require.Equal(t, "-Users-a/MEMORY.md", key)
	require.Equal(t, "# a\n", string(value))

	_, _, visible = projector.Project("-Users-missing/MEMORY.md", []byte("# m\n"))
	require.False(t, visible, "a slug without a directory stays hidden")

	_, _, visible = projector.Project("-Users-c/MEMORY.md", []byte("# c\n"))
	require.False(t, visible, "a symlinked slug stays hidden")

	_, _, visible = projector.Project("malformed", []byte("x"))
	require.False(t, visible)
}
