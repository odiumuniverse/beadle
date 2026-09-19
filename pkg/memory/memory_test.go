package memory_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/memory"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func TestSlug(t *testing.T) {
	t.Parallel()

	require.Equal(t, "-Users-x-my-proj", memory.Slug("/Users/x/my/proj"))
	require.Equal(t, "-", memory.Slug("/"))
	require.Equal(t, "-Users-x-my agents", memory.Slug("/Users/x/my agents"))
	require.Equal(t, "-tmp-a_b-c.d+", memory.Slug("/tmp/a_b/c.d+"))

	abs, err := filepath.Abs("rel/file")
	require.NoError(t, err)
	require.Equal(t, strings.ReplaceAll(abs, "/", "-"), memory.Slug("rel/file"))
}

func TestValidSlug(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want bool
	}{
		{name: "-Users-universe-my-beadle", want: true},
		{name: "-Users-universe", want: true},
		{name: "plain", want: true},
		{name: "with space", want: true},
		{name: "-Users-universe-ish_2.0+x", want: true},
		{name: ".", want: false},
		{name: "..", want: false},
		{name: "a/b", want: false},
		{name: "", want: false},
		{name: ".hidden", want: false},
		{name: "/abs", want: false},
		{name: "a/../b", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, memory.ValidSlug(tt.name))
		})
	}
}

func TestValidNote(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want bool
	}{
		{name: "MEMORY.md", want: true},
		{name: "feedback_workflow.md", want: true},
		{name: "project state.md", want: true},
		{name: "a.md", want: true},
		{name: strings.Repeat("long", 60) + ".md", want: true},
		{name: ".md", want: false},
		{name: ".hidden.md", want: false},
		{name: "x.MD", want: false},
		{name: "x.Md", want: false},
		{name: "note", want: false},
		{name: "x.md.bak", want: false},
		{name: "nested/x.md", want: false},
		{name: "", want: false},
		{name: "..", want: false},
		{name: "x.", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, memory.ValidNote(tt.name))
		})
	}
}

func TestReadDirMissingRoot(t *testing.T) {
	t.Parallel()

	slugs, err := memory.ReadDir(filepath.Join(t.TempDir(), "missing"))
	require.NoError(t, err)
	require.Empty(t, slugs)
}

func TestReadDirLayout(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	writeFile(t, filepath.Join(root, "-Users-universe", "MEMORY.md"), "# memory\n")
	writeFile(t, filepath.Join(root, "-Users-universe", "feedback_x.md"), "# feedback\n")

	writeFile(t, filepath.Join(root, "-Users-universe", "session.jsonl"), "{}\n")
	writeFile(t, filepath.Join(root, "-Users-universe", "nested", "deep.md"), "x\n")
	writeFile(t, filepath.Join(root, "-Users-universe", ".hidden.md"), "x\n")
	writeFile(t, filepath.Join(root, "-Users-universe", "upper.MD"), "x\n")
	writeFile(t, filepath.Join(root, "top-level.md"), "x\n")

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".hidden-slug"), 0o750))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "hosts-memory", "memory"), 0o750))

	target := filepath.Join(t.TempDir(), "outside")
	writeFile(t, filepath.Join(target, "note.md"), "x\n")

	require.NoError(t, os.Symlink(filepath.Join(target, "note.md"), filepath.Join(root, "-Users-universe", "link.md")))
	require.NoError(t, os.Symlink(target, filepath.Join(root, "linked-slug")))

	slugs, err := memory.ReadDir(root)
	require.NoError(t, err)
	require.Equal(t, map[string]memory.Tree{
		"-Users-universe": {
			"MEMORY.md":     []byte("# memory\n"),
			"feedback_x.md": []byte("# feedback\n"),
		},
		"hosts-memory": {},
	}, slugs)
}

func TestReadDirNoteError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	note := filepath.Join(root, "-slug", "broken.md")
	writeFile(t, note, "x\n")
	require.NoError(t, os.Chmod(note, 0o000))
	t.Cleanup(func() { _ = os.Chmod(note, 0o600) })

	_, err := memory.ReadDir(root)
	require.Error(t, err)
	require.Contains(t, err.Error(), "broken.md")
}

func TestFlattenGroupRoundTrip(t *testing.T) {
	t.Parallel()

	slugs := map[string]memory.Tree{
		"a": {"one.md": []byte("1\n"), "two.md": []byte("2\n")},
		"b": {"MEMORY.md": []byte("m\n")},
	}

	require.Equal(t, slugs, memory.Group(memory.Flatten(slugs)))
}

func TestGroupDropsInvalidKeys(t *testing.T) {
	t.Parallel()

	items := map[string][]byte{
		"good/ok.md":        []byte("x"),
		"../evil/note.md":   []byte("x"),
		".hidden/note.md":   []byte("x"),
		"slug/.hidden.md":   []byte("x"),
		"slug/note.txt":     []byte("x"),
		"slug/nested/x.md":  []byte("x"),
		"slug/":             []byte("x"),
		"noslash":           []byte("x"),
		"":                  []byte("x"),
		"slug/bad.md.bak":   []byte("x"),
		"slug/..":           []byte("x"),
		"deep/slug/note.md": []byte("x"),
	}

	require.Equal(t, map[string]memory.Tree{"good": {"ok.md": []byte("x")}}, memory.Group(items))
}

func TestSyncTreeWrites(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "memory")

	require.NoError(t, memory.SyncTree(root, "-slug", memory.Tree{"MEMORY.md": []byte("v1\n")}))

	info, err := os.Stat(filepath.Join(root, "-slug"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o750), info.Mode().Perm())

	note := filepath.Join(root, "-slug", "MEMORY.md")

	info, err = os.Stat(note)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm())

	require.NoError(t, memory.SyncTree(root, "-slug", memory.Tree{"MEMORY.md": []byte("v2\n")}))

	data, err := os.ReadFile(note) //nolint:gosec // G304: tests read their own temp files
	require.NoError(t, err)
	require.Equal(t, "v2\n", string(data))
}

func TestReadDirRejectsBadRoots(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "memory")
	require.NoError(t, os.Symlink(t.TempDir(), root))

	_, err := memory.ReadDir(root)
	require.Error(t, err)
	require.Contains(t, err.Error(), "symlink")

	root = filepath.Join(t.TempDir(), "memory")
	require.NoError(t, os.WriteFile(root, []byte("x"), 0o600))

	_, err = memory.ReadDir(root)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a directory")
}

func TestSyncTreeRejectsSymlinks(t *testing.T) {
	t.Parallel()

	target := t.TempDir()
	root := filepath.Join(t.TempDir(), "memory")

	require.NoError(t, os.Symlink(target, root))
	require.Error(t, memory.SyncTree(root, "-slug", memory.Tree{"MEMORY.md": []byte("x")}))

	root = filepath.Join(t.TempDir(), "memory")
	require.NoError(t, os.MkdirAll(root, 0o750))
	require.NoError(t, os.Symlink(target, filepath.Join(root, "-slug")))
	require.Error(t, memory.SyncTree(root, "-slug", memory.Tree{"MEMORY.md": []byte("x")}))

	root = filepath.Join(t.TempDir(), "memory")
	require.NoError(t, memory.SyncTree(root, "-slug", memory.Tree{"MEMORY.md": []byte("v1")}))
	require.NoError(t, os.Remove(filepath.Join(root, "-slug", "MEMORY.md")))
	require.NoError(t, os.Symlink(filepath.Join(target, "external.md"), filepath.Join(root, "-slug", "MEMORY.md")))
	require.Error(t, memory.SyncTree(root, "-slug", memory.Tree{"MEMORY.md": []byte("v2")}))

	info, err := os.Lstat(filepath.Join(root, "-slug", "MEMORY.md"))
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink, "a symlinked canon note is not replaced")
}

func TestSyncTreeRejectsInvalidNames(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "memory")

	require.Error(t, memory.SyncTree(root, ".hidden", memory.Tree{"MEMORY.md": []byte("x")}))
	require.Error(t, memory.SyncTree(root, "-slug", memory.Tree{"note.txt": []byte("x")}))
	require.NoError(t, memory.SyncTree(root, "-slug", memory.Tree{}))
	require.NoDirExists(t, filepath.Join(root, "-slug"))
}
