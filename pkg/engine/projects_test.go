package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/agent"
	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/digest"
	"github.com/odiumuniverse/agents-sync/pkg/engine"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
	"github.com/odiumuniverse/agents-sync/pkg/memory"
	"github.com/odiumuniverse/agents-sync/pkg/state"
)

func (f *fixture) useRepo(t *testing.T, repo string) {
	t.Helper()

	e, err := engine.New(f.vault, f.config, agent.All(f.home, repo), engine.WithHome(f.home))
	require.NoError(t, err)

	f.engine = e
}

func newRepo(t *testing.T) string {
	t.Helper()

	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o750))

	return repo
}

func repoFile(repo string) string { return filepath.Join(repo, "AGENTS.md") }

func vaultProject(f *fixture, repo, name string) string {
	return filepath.Join(f.vault.ProjectsDir(), memory.Slug(repo), name)
}

func writeMemoryCanon(t *testing.T, f *fixture, repo, name, content string) {
	t.Helper()

	write(t, filepath.Join(f.vault.MemoryDir(), memory.Slug(repo), name), content)
}

func digestResults(report *engine.Report, action engine.DigestAction) []engine.DigestResult {
	var out []engine.DigestResult

	for _, result := range report.Digest {
		if result.Action == action {
			out = append(out, result)
		}
	}

	return out
}

func baseBlob(t *testing.T, f *fixture, k kind.ID, agentID, key string) []byte {
	t.Helper()

	st, err := state.Load(f.vault.StatePath())
	require.NoError(t, err)

	base, ok := st.Base(k, agentID)
	require.True(t, ok)
	require.Contains(t, base, key)

	hash := base[key]
	require.Len(t, hash, 64)

	data, err := os.ReadFile(filepath.Join(f.vault.ObjectsDir(), string(hash[:2]), string(hash[2:])))
	require.NoError(t, err)

	return data
}

func TestProjectsSyncRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	write(t, repoFile(repo), "# repo rules\n")

	report := f.sync(t)
	require.True(t, report.Kind(kind.Projects).VaultChanged)
	require.Equal(t, "# repo rules\n", read(t, vaultProject(f, repo, "AGENTS.md")))

	report = f.sync(t)
	require.False(t, report.Kind(kind.Projects).VaultChanged)
	require.Equal(t, engine.ActionNoop, report.Action(kind.Projects, agent.OpenCodeID))

	write(t, vaultProject(f, repo, "AGENTS.md"), "# canon edit\n")
	f.sync(t)
	require.Equal(t, "# canon edit\n", read(t, repoFile(repo)))

	write(t, repoFile(repo), "# local edit\n")
	f.sync(t)
	require.Equal(t, "# local edit\n", read(t, vaultProject(f, repo, "AGENTS.md")))

	require.NoError(t, os.Remove(repoFile(repo)))
	f.sync(t)
	require.NoFileExists(t, vaultProject(f, repo, "AGENTS.md"), "deleting the project file deletes the canon item")
}

func TestProjectsConflictResolvedInEditor(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	write(t, repoFile(repo), "v1\n")
	f.sync(t)

	write(t, repoFile(repo), "agent-edit\n")
	write(t, vaultProject(f, repo, "AGENTS.md"), "vault-edit\n")

	report := f.sync(t)
	require.Len(t, report.ConflictsOf(kind.Projects), 1)
	require.Equal(t, "vault-edit\n", read(t, vaultProject(f, repo, "AGENTS.md")))

	c := f.conflict(t, kind.Projects, agent.OpenCodeID)

	file, err := f.engine.ConflictFile(c)
	require.NoError(t, err)
	require.Equal(t, ".md", filepath.Ext(file))
	require.Contains(t, filepath.Base(file), "projects-opencode-")
	require.Contains(t, read(t, file), ">>>>>>> agent:opencode")

	write(t, file, "# resolved\n")

	_, err = f.engine.Resolve(t.Context(), []string{c.ID()}, engine.Resolution{Take: engine.TakeFile})
	require.NoError(t, err)

	require.Equal(t, "# resolved\n", read(t, vaultProject(f, repo, "AGENTS.md")))
	require.Equal(t, "# resolved\n", read(t, repoFile(repo)))
}

func TestProjectsCanonDeletionRemovesFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	write(t, repoFile(repo), "# repo rules\n")
	f.sync(t)

	require.NoError(t, os.Remove(vaultProject(f, repo, "AGENTS.md")))

	f.sync(t)

	require.NoFileExists(t, repoFile(repo), "a deleted canon item removes the project file")
	require.NoFileExists(t, vaultProject(f, repo, "AGENTS.md"))

	writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")
	f.sync(t)

	data := read(t, repoFile(repo))
	require.True(t, strings.HasPrefix(data, digest.BeginPrefix), "a non-empty memory canon recreates the file as fence-only")
	require.NotContains(t, data, "# repo rules")
}

func TestProjectsConflictTakeAgent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	write(t, repoFile(repo), "v1\n")
	f.sync(t)

	write(t, repoFile(repo), "agent-edit\n")
	write(t, vaultProject(f, repo, "AGENTS.md"), "vault-edit\n")
	f.sync(t)

	f.resolve(t, kind.Projects, agent.OpenCodeID, engine.TakeAgent)

	require.Equal(t, "agent-edit\n", read(t, vaultProject(f, repo, "AGENTS.md")))
	require.Equal(t, "agent-edit\n", read(t, repoFile(repo)))
}

func TestProjectsFenceBlindNoop(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	write(t, repoFile(repo), "# repo rules\n")
	writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")

	report := f.sync(t)
	require.NotEmpty(t, digestResults(report, engine.DigestRefreshed))

	data := read(t, repoFile(repo))
	require.True(t, strings.HasPrefix(data, digest.BeginPrefix), "the fence sits at the top")
	require.True(t, strings.HasSuffix(data, "# repo rules\n"))

	canon := read(t, vaultProject(f, repo, "AGENTS.md"))
	require.Equal(t, "# repo rules\n", canon, "the fence never reaches the canon")
	require.NotContains(t, canon, digest.BeginPrefix)
	require.NotContains(t, string(baseBlob(t, f, kind.Projects, agent.OpenCodeID, memory.Slug(repo)+"/AGENTS.md")), digest.BeginPrefix)

	report = f.sync(t)
	require.Empty(t, report.Digest, "an unchanged digest is a noop")
	require.Equal(t, engine.ActionNoop, report.Action(kind.Projects, agent.OpenCodeID))

	writeMemoryCanon(t, f, repo, "feedback.md", "---\ndescription: second\n---\nbody\n")

	report = f.sync(t)
	require.NotEmpty(t, digestResults(report, engine.DigestRefreshed))

	data = read(t, repoFile(repo))
	require.Contains(t, data, "feedback.md")
	require.True(t, strings.HasSuffix(data, "# repo rules\n"), "the body survives a digest refresh")
	require.Equal(t, "# repo rules\n", read(t, vaultProject(f, repo, "AGENTS.md")))
}

func TestProjectsFenceOnlyFileSurvives(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")

	report := f.sync(t)
	require.NotEmpty(t, digestResults(report, engine.DigestCreated))

	require.NoFileExists(t, vaultProject(f, repo, "AGENTS.md"), "a fence-only file has no canon item")

	data := read(t, repoFile(repo))
	require.True(t, strings.HasPrefix(data, digest.BeginPrefix))

	report = f.sync(t)
	require.Equal(t, engine.ActionNoop, report.Action(kind.Projects, agent.OpenCodeID))
	require.FileExists(t, repoFile(repo), "push never deletes a fence-only file")
}

func TestDigestRemovedWhenMemoryEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	write(t, repoFile(repo), "# repo rules\n")
	writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")
	f.sync(t)

	require.NoError(t, os.Remove(filepath.Join(f.vault.MemoryDir(), memory.Slug(repo), "MEMORY.md")))

	report := f.sync(t)
	require.NotEmpty(t, digestResults(report, engine.DigestRemoved))
	require.Equal(t, "# repo rules\n", read(t, repoFile(repo)), "removing the fence keeps the body")

	report = f.sync(t)
	require.Empty(t, report.Digest)
}

func TestDigestGates(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	write(t, repoFile(repo), "# repo rules\n")
	writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")

	f.run(t, engine.SyncOptions{Direction: config.ModePull})
	require.NotContains(t, read(t, repoFile(repo)), digest.BeginPrefix, "pull never writes a fence")

	f.run(t, engine.SyncOptions{Kinds: []kind.ID{kind.Memory}})
	require.NotContains(t, read(t, repoFile(repo)), digest.BeginPrefix, "a kind filter excludes the digest phase")

	f.config.SetKind(kind.Projects, config.ModeOff)
	f.sync(t)
	require.NotContains(t, read(t, repoFile(repo)), digest.BeginPrefix, "kinds.projects=off silences the digest phase")

	f.config.SetKind(kind.Projects, config.ModeSync)
	f.config.SetMode(agent.OpenCodeID, kind.Projects, config.ModeOff)
	f.sync(t)
	require.NotContains(t, read(t, repoFile(repo)), digest.BeginPrefix, "a mode-off agent gets no digest")
}

func TestDigestAdoptBaseline(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	slug := memory.Slug(repo)
	notes := map[string][]byte{slug + "/MEMORY.md": []byte("---\ndescription: hook\n---\nbody\n")}
	block, _ := digest.Render(filepath.Join(f.home, ".claude", "projects", slug, "memory"), notes, digest.DefaultBudget)

	write(t, repoFile(repo), string(block)+"# repo rules\n")
	writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")

	report := f.sync(t)
	require.NotEmpty(t, digestResults(report, engine.DigestAdopted))
	require.Equal(t, string(block)+"# repo rules\n", read(t, repoFile(repo)), "the adopted block is not rewritten")

	st, err := state.Load(f.vault.StatePath())
	require.NoError(t, err)
	require.Contains(t, st.Renders, repoFile(repo))
	require.Zero(t, st.Drift[repoFile(repo)].Count)

	report = f.sync(t)
	require.Empty(t, report.Digest, "an adopted baseline settles into noop")
}

func TestDigestDriftHoldAndRefresh(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	write(t, repoFile(repo), "# repo rules\n")
	writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")
	f.sync(t)

	edited := strings.Replace(read(t, repoFile(repo)), "hook", "hand edit", 1)
	write(t, repoFile(repo), edited)

	report := f.sync(t)
	require.NotEmpty(t, digestResults(report, engine.DigestHeld))
	require.Contains(t, strings.Join(report.Warnings, " "), "manual edits inside the generated block")
	require.Equal(t, edited, read(t, repoFile(repo)), "held bytes stay untouched")

	st, err := state.Load(f.vault.StatePath())
	require.NoError(t, err)
	require.Equal(t, 1, st.Drift[repoFile(repo)].Count)

	report = f.sync(t)
	require.NotEmpty(t, digestResults(report, engine.DigestHeld))

	st, err = state.Load(f.vault.StatePath())
	require.NoError(t, err)
	require.Equal(t, 2, st.Drift[repoFile(repo)].Count)

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityError, "manual edits inside the generated block in "+repoFile(repo)), "issues: %v", issues)

	report, err = f.engine.Sync(t.Context(), engine.SyncOptions{Refresh: true})
	require.NoError(t, err)
	require.NotEmpty(t, digestResults(report, engine.DigestRefreshed))
	require.NotContains(t, read(t, repoFile(repo)), "hand edit")

	st, err = state.Load(f.vault.StatePath())
	require.NoError(t, err)
	require.Zero(t, st.Drift[repoFile(repo)].Count, "an explicit refresh resets the drift counter")

	require.Equal(t, engine.ActionNoop, f.sync(t).Action(kind.Projects, agent.OpenCodeID))
}

func TestDigestBlockDeletedRestores(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	write(t, repoFile(repo), "# repo rules\n")
	writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")
	f.sync(t)

	require.NoError(t, os.Remove(repoFile(repo)))
	write(t, repoFile(repo), "# repo rules\n")

	report := f.sync(t)
	require.NotEmpty(t, digestResults(report, engine.DigestRefreshed))
	require.True(t, strings.HasPrefix(read(t, repoFile(repo)), digest.BeginPrefix), "a deleted block comes back")
}

func TestDigestPrune(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	write(t, repoFile(repo), "# repo rules\n")
	writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")
	f.sync(t)

	st, err := state.Load(f.vault.StatePath())
	require.NoError(t, err)
	require.Contains(t, st.Renders, repoFile(repo))

	require.NoError(t, os.RemoveAll(filepath.Join(repo, ".git")))
	require.NoError(t, os.Remove(repoFile(repo)))

	report := f.sync(t)
	require.NotEmpty(t, digestResults(report, engine.DigestPruned))

	st, err = state.Load(f.vault.StatePath())
	require.NoError(t, err)
	require.NotContains(t, st.Renders, repoFile(repo))
	require.NotContains(t, st.Drift, repoFile(repo))
}

func TestDigestDryRunPreview(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")

	report := f.run(t, engine.SyncOptions{DryRun: true})
	require.NotEmpty(t, digestResults(report, engine.DigestWouldCreate))
	require.NoFileExists(t, repoFile(repo), "a dry run writes nothing")

	st, err := state.Load(f.vault.StatePath())
	require.NoError(t, err)
	require.NotContains(t, st.Renders, repoFile(repo))
}

func TestProjectsWatchPathsAndKindFilter(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	paths, err := f.engine.WatchPaths(t.Context())
	require.NoError(t, err)
	require.Contains(t, paths, f.vault.ProjectsDir())
	require.Contains(t, paths, repoFile(repo))

	write(t, repoFile(repo), "# repo rules\n")

	report := f.run(t, engine.SyncOptions{Kinds: []kind.ID{kind.Projects}})
	require.NotNil(t, report.Kind(kind.Projects))
	require.Nil(t, report.Kind(kind.Memory))
	require.Equal(t, "# repo rules\n", read(t, vaultProject(f, repo, "AGENTS.md")))
}

func TestProjectsRestore(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	write(t, repoFile(repo), "# v1\n")
	f.sync(t)

	write(t, vaultProject(f, repo, "AGENTS.md"), "# v2\n")
	f.sync(t)

	_, err := f.engine.Restore(t.Context(), kind.Projects, -2)
	require.NoError(t, err)

	require.Equal(t, "# v1\n", read(t, vaultProject(f, repo, "AGENTS.md")))
	require.Equal(t, "# v1\n", read(t, repoFile(repo)))
}
