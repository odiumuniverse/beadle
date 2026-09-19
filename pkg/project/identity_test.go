package project_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/memory"
	"github.com/odiumuniverse/beadle/pkg/project"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()

	all := append([]string{"-C", dir}, args...)

	out, err := exec.CommandContext(t.Context(), "git", all...).CombinedOutput() //nolint:gosec // G204: fixed git subcommands in tests
	require.NoError(t, err, string(out))
}

func newGitRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	git(t, dir, "init", "-q")

	return dir
}

func TestIdentityCanonicalRemote(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"git@github.com:Org/Repo.git":            "github.com/org/repo",
		"https://github.com/Org/Repo.git":        "github.com/org/repo",
		"ssh://git@github.com/Org/Repo":          "github.com/org/repo",
		"https://github.com/Org/Repo/":           "github.com/org/repo",
		"ssh://git@github.com:2222/Org/Repo.git": "github.com/2222/org/repo",
		"git@gitlab.example.com:group/sub/repo":  "gitlab.example.com/group/sub/repo",
	}

	for remote, want := range cases {
		t.Run(remote, func(t *testing.T) {
			t.Parallel()

			dir := newGitRepo(t)
			git(t, dir, "remote", "add", "origin", remote)

			require.Equal(t, want, project.Resolve(dir).Remote)
		})
	}
}

func TestIdentityClonesConverge(t *testing.T) {
	t.Parallel()

	origin := t.TempDir()
	git(t, origin, "init", "-q", "--bare")

	cloneA := filepath.Join(t.TempDir(), "alpha")
	cloneB := filepath.Join(t.TempDir(), "beta")

	git(t, t.TempDir(), "clone", "-q", origin, cloneA)
	git(t, t.TempDir(), "clone", "-q", origin, cloneB)
	git(t, cloneA, "remote", "set-url", "origin", "git@"+origin)
	git(t, cloneB, "remote", "set-url", "origin", origin)

	deep := filepath.Join(cloneA, "nested", "deeper")
	require.NoError(t, os.MkdirAll(deep, 0o750))

	idA := project.Resolve(cloneA)
	idDeep := project.Resolve(deep)
	idB := project.Resolve(cloneB)

	require.False(t, idA.Slugs)
	require.Equal(t, idA.ID, idDeep.ID, "a subdirectory resolves to the same project")
	require.Equal(t, idA.ID, idB.ID, "two clones of one remote share the project")
	require.Equal(t, idA.Root, idDeep.Root)
}

func TestIdentityWorktreeSharesTheProject(t *testing.T) {
	t.Parallel()

	repo := newGitRepo(t)
	git(t, repo, "config", "user.email", "test@example.com")
	git(t, repo, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o600))
	git(t, repo, "add", "README.md")
	git(t, repo, "commit", "-q", "-m", "init")

	worktree := filepath.Join(t.TempDir(), "wt")
	git(t, repo, "worktree", "add", "-q", worktree, "HEAD")

	require.Equal(t, project.Resolve(repo).ID, project.Resolve(worktree).ID)
}

func TestIdentityNonGitUsesPathSlug(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	identity := project.Resolve(dir)
	require.True(t, identity.Slugs)
	require.Equal(t, memory.Slug(dir), identity.ID)
	require.Empty(t, identity.Root)
	require.Empty(t, identity.Remote)
}

func TestIdentityHomeGuard(t *testing.T) {
	home := newGitRepo(t)
	t.Setenv("HOME", home)

	identity := project.Resolve(home)
	require.True(t, identity.Slugs, "a repo rooted at $HOME is not captured")
	require.Equal(t, memory.Slug(home), identity.ID)
}

func TestIdentityNoRemoteFallsBackToCommonDir(t *testing.T) {
	t.Parallel()

	repo := newGitRepo(t)
	git(t, repo, "config", "user.email", "test@example.com")
	git(t, repo, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o600))
	git(t, repo, "add", "README.md")
	git(t, repo, "commit", "-q", "-m", "init")

	identity := project.Resolve(repo)
	require.False(t, identity.Slugs)
	require.Empty(t, identity.Remote)
	require.Regexp(t, `^[a-z0-9-]+-[0-9a-f]{8}$`, identity.ID)
}
