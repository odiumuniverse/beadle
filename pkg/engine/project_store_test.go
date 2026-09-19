package engine_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/kind"
)

func TestProjectCanonArbitraryFiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)
	f.enableProject(t, "AGENTS.md")

	id := repoID(repo)
	canon := func(rel string) string { return filepath.Join(f.vault.ProjectsDir(), id, filepath.FromSlash(rel)) }

	write(t, canon("AGENTS.md"), "# rules\n")
	write(t, canon("deep/nested/.hidden"), "custom\n")
	write(t, canon("deep/notes.json"), "{}\n")
	require.NoError(t, os.Symlink(canon("AGENTS.md"), canon("linked.md")))

	write(t, repoFile(repo), "# rules\n")

	report := f.sync(t)
	require.False(t, report.Kind(kind.Projects).VaultChanged, "foreign canon files are not pushed anywhere")

	require.FileExists(t, canon("deep/nested/.hidden"), "nested and dotfiles survive")
	require.FileExists(t, canon("deep/notes.json"))
	require.FileExists(t, canon("policy.json"), "policy.json is never removed by the saver")

	info, err := os.Lstat(canon("linked.md"))
	require.NoError(t, err)
	require.NotEqual(t, os.FileMode(0), info.Mode()&os.ModeSymlink, "symlinks in the canon are left alone")

	require.NoError(t, os.Remove(canon("deep/notes.json")))
	write(t, repoFile(repo), "# rules v2\n")

	report = f.sync(t)
	require.True(t, report.Kind(kind.Projects).VaultChanged)
	require.NoFileExists(t, canon("deep/notes.json"))
	require.FileExists(t, canon("deep/nested/.hidden"), "an unrelated canon file survives the saver pass")
	require.FileExists(t, canon("policy.json"))
	require.Equal(t, "# rules v2\n", read(t, repoFile(repo)))
}

func TestProjectCanonLoaderRejectsTraversal(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)
	f.enableProject(t, "AGENTS.md")

	id := repoID(repo)
	write(t, filepath.Join(f.vault.ProjectsDir(), id, "AGENTS.md"), "# rules\n")
	write(t, repoFile(repo), "# rules\n")

	f.sync(t)

	outside := filepath.Join(f.vault.ProjectsDir(), "escape.md")
	write(t, outside, "secret\n")

	report := f.sync(t)
	require.False(t, report.Kind(kind.Projects).VaultChanged, "nothing outside the project directory is loaded")
	require.Equal(t, "secret\n", read(t, outside), "the loader never touches sibling paths")
}

func TestProjectForgetKeepsOtherEntries(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)
	f.enableProject(t, "AGENTS.md", ".mcp.json")

	id := repoID(repo)
	canon := func(rel string) string { return filepath.Join(f.vault.ProjectsDir(), id, filepath.FromSlash(rel)) }

	write(t, repoFile(repo), "# rules\n")
	f.sync(t)

	require.FileExists(t, canon("AGENTS.md"))
	require.FileExists(t, canon(".mcp.json"))

	_, err := f.engine.ProjectForget(t.Context(), ".mcp.json")
	require.NoError(t, err)

	require.NoFileExists(t, canon(".mcp.json"))
	require.FileExists(t, canon("AGENTS.md"), "other canon entries survive a targeted forget")
	require.FileExists(t, repoFile(repo))
}
