package skill_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/skill"
)

func TestReadWriteTree(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	tree := skill.Tree{
		"SKILL.md":     []byte("# hello\n"),
		"scripts/x.sh": []byte("echo hi\n"),
	}

	require.NoError(t, skill.WriteTree(dir, "my-skill", tree))

	got, err := skill.ReadTree(filepath.Join(dir, "my-skill"))
	require.NoError(t, err)
	require.Equal(t, tree, got)

	skills, err := skill.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, skills, 1)
	require.Equal(t, tree, skills["my-skill"])
}

func TestReadDirSkipsSymlinksAndBadNames(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "good-skill"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "good-skill", "SKILL.md"), []byte("x"), 0o600))

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "Bad Name"), 0o750))

	target := filepath.Join(t.TempDir(), "linked")
	require.NoError(t, os.MkdirAll(target, 0o750))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "linked-skill")))

	skills, err := skill.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, skills, 1)
	require.Contains(t, skills, "good-skill")
}

func TestWriteTreeRejectsTraversal(t *testing.T) {
	t.Parallel()

	err := skill.WriteTree(t.TempDir(), "my-skill", skill.Tree{"../escape.md": []byte("x")})
	require.Error(t, err)

	err = skill.WriteTree(t.TempDir(), "Bad Name", skill.Tree{"a.md": []byte("x")})
	require.Error(t, err)
}

func TestManifest(t *testing.T) {
	t.Parallel()

	tree := skill.Tree{"SKILL.md": []byte("# x\n")}
	manifest := skill.ManifestOf(tree)

	data, err := manifest.Marshal()
	require.NoError(t, err)

	parsed, err := skill.ParseManifest(data)
	require.NoError(t, err)
	require.Equal(t, manifest, parsed)
	require.Equal(t, manifest.Hash(), parsed.Hash())
	require.NotEmpty(t, manifest.Hash())
}

func TestTree3(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		base, vault, agent skill.Tree
		want               skill.Tree
		wantConflicts      []string
	}{
		"agent change": {
			base:  skill.Tree{"a": []byte("1")},
			vault: skill.Tree{"a": []byte("1")},
			agent: skill.Tree{"a": []byte("2")},
			want:  skill.Tree{"a": []byte("2")},
		},
		"vault change": {
			base:  skill.Tree{"a": []byte("1")},
			vault: skill.Tree{"a": []byte("2")},
			agent: skill.Tree{"a": []byte("1")},
			want:  skill.Tree{"a": []byte("2")},
		},
		"same change both sides": {
			base:  skill.Tree{"a": []byte("1")},
			vault: skill.Tree{"a": []byte("2")},
			agent: skill.Tree{"a": []byte("2")},
			want:  skill.Tree{"a": []byte("2")},
		},
		"different change": {
			base:          skill.Tree{"a": []byte("1")},
			vault:         skill.Tree{"a": []byte("2")},
			agent:         skill.Tree{"a": []byte("3")},
			want:          skill.Tree{"a": []byte("2")},
			wantConflicts: []string{"a"},
		},
		"agent deletes, vault unchanged": {
			base:  skill.Tree{"a": []byte("1")},
			vault: skill.Tree{"a": []byte("1")},
			agent: skill.Tree{},
			want:  skill.Tree{},
		},
		"agent deletes, vault modifies": {
			base:          skill.Tree{"a": []byte("1")},
			vault:         skill.Tree{"a": []byte("2")},
			agent:         skill.Tree{},
			want:          skill.Tree{"a": []byte("2")},
			wantConflicts: []string{"a"},
		},
		"new file from agent": {
			base:  skill.Tree{},
			vault: skill.Tree{},
			agent: skill.Tree{"b": []byte("1")},
			want:  skill.Tree{"b": []byte("1")},
		},
		"both add same file": {
			base:  skill.Tree{},
			vault: skill.Tree{"b": []byte("1")},
			agent: skill.Tree{"b": []byte("1")},
			want:  skill.Tree{"b": []byte("1")},
		},
		"both add different content": {
			base:          skill.Tree{},
			vault:         skill.Tree{"b": []byte("1")},
			agent:         skill.Tree{"b": []byte("2")},
			want:          skill.Tree{"b": []byte("1")},
			wantConflicts: []string{"b"},
		},
		"vault adds, agent never had it": {
			base:  skill.Tree{},
			vault: skill.Tree{"b": []byte("1")},
			agent: skill.Tree{},
			want:  skill.Tree{"b": []byte("1")},
		},
		"independent files": {
			base:  skill.Tree{"a": []byte("1"), "b": []byte("1")},
			vault: skill.Tree{"a": []byte("2"), "b": []byte("1")},
			agent: skill.Tree{"a": []byte("1"), "b": []byte("2")},
			want:  skill.Tree{"a": []byte("2"), "b": []byte("2")},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			result := skill.Tree3("s", tt.base, tt.vault, tt.agent)
			require.Equal(t, tt.want, result.Tree)

			paths := make([]string, 0, len(result.Conflicts))
			for _, c := range result.Conflicts {
				paths = append(paths, c.Path)
			}

			if tt.wantConflicts == nil {
				require.Empty(t, paths)
			} else {
				require.Equal(t, tt.wantConflicts, paths)
			}
		})
	}
}
