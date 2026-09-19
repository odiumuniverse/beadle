package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/memory"
	"github.com/odiumuniverse/beadle/pkg/state"
)

func (f *fixture) enableAgent(t *testing.T, id string) {
	t.Helper()

	f.config.Enable(id)
	require.NoError(t, f.config.Save(f.vault.ConfigPath()))
}

func cursorHome(t *testing.T, f *fixture) {
	t.Helper()

	write(t, filepath.Join(f.home, ".cursor", "mcp.json"), `{"mcpServers": {}}`)
}

func antigravityHome(t *testing.T, f *fixture) {
	t.Helper()

	write(t, filepath.Join(f.home, ".gemini", "config", "mcp_config.json"), `{"mcpServers": {}}`)
}

func TestProjectFourFilesRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	cursorHome(t, f)
	antigravityHome(t, f)
	f.enableAgent(t, agent.CursorID)
	f.enableAgent(t, agent.AntigravityCLIID)

	repo := newRepo(t)
	f.useRepo(t, repo)
	f.enableProject(t, ".mcp.json", ".cursor/mcp.json", ".cursor/rules", ".agents/mcp_config.json")

	write(t, filepath.Join(repo, ".mcp.json"), `{"mcpServers": {"claude-srv": {"type": "http", "url": "https://claude.example.com"}}}`)
	write(t, filepath.Join(repo, ".cursor", "mcp.json"), `{"mcpServers": {"cursor-srv": {"url": "https://cursor.example.com"}}}`)
	write(t, filepath.Join(repo, ".cursor", "rules", "style.mdc"), "# style\n")
	write(t, filepath.Join(repo, ".cursor", "rules", "notes.txt"), "foreign\n")
	write(t, filepath.Join(repo, ".agents", "mcp_config.json"), `{"mcpServers": {"agy-srv": {"serverUrl": "https://agy.example.com"}}}`)

	report := f.sync(t)
	require.True(t, report.Kind(kind.Projects).VaultChanged)
	require.Empty(t, report.Errors(), "no surface errors: %v", report.Errors())
	require.Len(t, report.Kind(kind.Projects).Agents, 3, "one result per agent, not per surface")

	canon := func(rel string) string {
		return read(t, filepath.Join(f.vault.ProjectsDir(), repoID(repo), filepath.FromSlash(rel)))
	}

	require.Contains(t, canon(".mcp.json"), "claude-srv")
	require.Contains(t, canon(".cursor/mcp.json"), "cursor-srv")
	require.Equal(t, "# style\n", canon(".cursor/rules/style.mdc"))
	require.Contains(t, canon(".agents/mcp_config.json"), "agy-srv")

	require.Equal(t, "# style\n", read(t, filepath.Join(repo, ".cursor", "rules", "style.mdc")))
	require.Equal(t, "foreign\n", read(t, filepath.Join(repo, ".cursor", "rules", "notes.txt")), "foreign files survive")

	write(t, filepath.Join(f.vault.ProjectsDir(), repoID(repo), ".cursor", "rules", "style.mdc"), "# style v2\n")

	f.sync(t)
	require.Equal(t, "# style v2\n", read(t, filepath.Join(repo, ".cursor", "rules", "style.mdc")))

	require.NoError(t, os.Remove(filepath.Join(f.vault.ProjectsDir(), repoID(repo), ".cursor", "rules", "style.mdc")))

	f.sync(t)
	require.NoFileExists(t, filepath.Join(repo, ".cursor", "rules", "style.mdc"), "an explicit canon deletion removes the file")
	require.Equal(t, "foreign\n", read(t, filepath.Join(repo, ".cursor", "rules", "notes.txt")))

	report = f.sync(t)
	require.Equal(t, engine.ActionNoop, report.Action(kind.Projects, agent.CursorID))
	require.Equal(t, engine.ActionNoop, report.Action(kind.Projects, agent.ClaudeCodeID))
}

func TestProjectEnableCreatesSkeleton(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	f.enableProject(t, ".mcp.json")

	require.JSONEq(t, `{"mcpServers":{}}`, read(t, filepath.Join(repo, ".mcp.json")), "enable creates the missing skeleton file")

	f.sync(t)

	require.JSONEq(t, `{"mcpServers":{}}`, read(t, filepath.Join(f.vault.ProjectsDir(), repoID(repo), ".mcp.json")))

	report := f.sync(t)
	require.Equal(t, engine.ActionNoop, report.Action(kind.Projects, agent.ClaudeCodeID))

	status, err := f.engine.ProjectDisable(t.Context(), ".mcp.json")
	require.NoError(t, err)

	for _, file := range status.Files {
		if file.Rel == ".mcp.json" {
			require.False(t, file.Enabled)
		}
	}

	report = f.sync(t)
	require.Empty(t, report.Kind(kind.Projects).Agents, "disabled surfaces are not synced")
	require.FileExists(t, filepath.Join(repo, ".mcp.json"), "disable never deletes")
}

func TestProjectLeakGateRendersEnvForm(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)
	f.enableProject(t, ".mcp.json")

	f.engine.Secrets().Set("AUTHORIZATION", "Bearer abc123")
	require.NoError(t, f.engine.Secrets().Save())

	f.seedProjectCanon(t, repo, ".mcp.json",
		`{"mcpServers":{"ctx7":{"type":"http","url":"https://mcp.example.com","headers":{"Authorization":"{secret:AUTHORIZATION}"}}}}`)

	report := f.sync(t)
	require.FileExists(t, filepath.Join(repo, ".mcp.json"))

	data := read(t, filepath.Join(repo, ".mcp.json"))
	require.Contains(t, data, "${AUTHORIZATION}")
	require.Contains(t, data, "ctx7", "the server stays in place")
	require.NotContains(t, data, "Bearer abc123", "the value never reaches a tracked file")
	require.Contains(t, strings.Join(report.Kind(kind.Projects).Warnings, " "), "rendered as ${NAME}")
}

func TestProjectIgnoredFileGetsValues(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)
	f.enableProject(t, ".mcp.json")
	ignoreInRepo(t, repo, ".mcp.json")

	f.engine.Secrets().Set("AUTHORIZATION", "Bearer abc123")
	require.NoError(t, f.engine.Secrets().Save())

	f.seedProjectCanon(t, repo, ".mcp.json",
		`{"mcpServers":{"ctx7":{"type":"http","url":"https://mcp.example.com","headers":{"Authorization":"{secret:AUTHORIZATION}"}}}}`)

	report := f.sync(t)
	require.Empty(t, strings.Join(report.Kind(kind.Projects).Warnings, " "))

	data := read(t, filepath.Join(repo, ".mcp.json"))
	require.Contains(t, data, "Bearer abc123", "a gitignored file materializes the value")
	require.NotContains(t, data, "${AUTHORIZATION}")
}

func TestProjectWithoutClaudeCode(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	repo := newRepo(t)

	write(t, filepath.Join(f.home, ".config", "opencode", "AGENTS.md"), "# global\n")

	f.useRepo(t, repo)
	f.enableProject(t, "AGENTS.md")
	ignoreInRepo(t, repo, "AGENTS.md")

	write(t, repoFile(repo), "# repo rules\n")
	writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")

	report := f.sync(t)
	require.Empty(t, report.Errors(), "opencode-only setups sync without Claude Code")
	require.Equal(t, "# repo rules\n", read(t, vaultProject(f, repo, "AGENTS.md")))
	require.True(t, strings.HasPrefix(read(t, repoFile(repo)), "<!-- beadle:memory:"), "the digest works without Claude Code")
	require.Contains(t, read(t, repoFile(repo)), "hook")

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)

	for _, issue := range issues {
		require.NotEqual(t, engine.SeverityError, issue.Severity, issue.Message)
	}

	report = f.sync(t)
	require.Equal(t, engine.ActionNoop, report.Action(kind.Projects, agent.OpenCodeID))
	require.Empty(t, report.Kind(kind.Memory).Agents, "no Claude Code means no memory surface, not an error")
}

func TestProjectLeakGateFollowsSymlinkedCwd(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	repo := newRepo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "sub"), 0o750))
	write(t, filepath.Join(repo, ".gitignore"), "/sub/.mcp.json\n")

	link := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(repo, link))

	cwd := filepath.Join(link, "sub")

	f.useCwd(t, cwd)
	f.enableProject(t, ".mcp.json")

	f.engine.Secrets().Set("AUTHORIZATION", "Bearer abc123")
	require.NoError(t, f.engine.Secrets().Save())

	write(t, filepath.Join(f.vault.ProjectsDir(), repoID(repo), ".mcp.json"),
		`{"mcpServers":{"ctx7":{"headers":{"Authorization":"{secret:AUTHORIZATION}"}}}}`)
	require.NoError(t, os.Remove(filepath.Join(cwd, ".mcp.json")))

	f.sync(t)

	data := read(t, filepath.Join(cwd, ".mcp.json"))
	require.Contains(t, data, "Bearer abc123", "the ignored path under a symlinked cwd materializes values")
	require.NotContains(t, data, "${AUTHORIZATION}")
}

func TestProjectAllowSecretsOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)
	f.enableProjectWith(t, engine.ProjectOptions{AllowSecrets: true}, ".mcp.json")

	f.engine.Secrets().Set("AUTHORIZATION", "Bearer abc123")
	require.NoError(t, f.engine.Secrets().Save())

	f.seedProjectCanon(t, repo, ".mcp.json",
		`{"mcpServers":{"ctx7":{"type":"http","url":"https://mcp.example.com","headers":{"Authorization":"{secret:AUTHORIZATION}"}}}}`)

	f.sync(t)

	require.Contains(t, read(t, filepath.Join(repo, ".mcp.json")), "Bearer abc123", "allowSecrets materializes values in a tracked file")
}

func TestProjectSecretsExtractionAndRefs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)
	f.enableProject(t, ".mcp.json")

	write(t, filepath.Join(repo, ".mcp.json"),
		`{"mcpServers":{"ctx7":{"type":"http","url":"https://mcp.example.com","headers":{"Authorization":"Bearer abc123"}}}}`)

	report := f.sync(t)
	require.Empty(t, report.Errors())

	canon := read(t, filepath.Join(f.vault.ProjectsDir(), repoID(repo), ".mcp.json"))
	require.Contains(t, canon, "{secret:AUTHORIZATION}")
	require.NotContains(t, canon, "abc123")

	value, ok := f.engine.Secrets().Get("AUTHORIZATION")
	require.True(t, ok)
	require.Equal(t, "Bearer abc123", value)

	removed, err := f.engine.PruneSecrets(t.Context())
	require.NoError(t, err)
	require.Empty(t, removed, "the project reference keeps the value in use")

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.False(t, hasIssue(issues, engine.SeverityError, "has no value"), "the reference resolves: %v", issues)
}

func TestProjectNoImplicitDelete(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)
	f.enableProject(t, "AGENTS.md")

	gitIn(t, repo, "config", "user.email", "test@example.com")
	gitIn(t, repo, "config", "user.name", "Test")

	write(t, repoFile(repo), "# repo rules\n")
	gitIn(t, repo, "add", "AGENTS.md")
	gitIn(t, repo, "commit", "-q", "-m", "rules")

	f.sync(t)

	gitIn(t, repo, "checkout", "-q", "-b", "without")
	gitIn(t, repo, "rm", "-q", "AGENTS.md")
	gitIn(t, repo, "commit", "-q", "-m", "drop rules")

	report := f.sync(t)
	require.FileExists(t, vaultProject(f, repo, "AGENTS.md"), "a checkout without the file keeps the canon")
	require.NoFileExists(t, repoFile(repo))
	require.NotEmpty(t, report.Kind(kind.Projects).Kept)
	require.Empty(t, report.ConflictsOf(kind.Projects), "no deletion conflict")

	report = f.sync(t)
	require.NoFileExists(t, repoFile(repo), "push never re-imposes the file")
	require.Equal(t, engine.ActionNoop, report.Action(kind.Projects, agent.OpenCodeID))

	gitIn(t, repo, "checkout", "-q", "-")

	report = f.sync(t)
	require.Equal(t, "# repo rules\n", read(t, repoFile(repo)))
	require.Empty(t, report.ConflictsOf(kind.Projects))

	_, err := f.engine.ProjectForget(t.Context(), "AGENTS.md")
	require.NoError(t, err)
	require.NoFileExists(t, vaultProject(f, repo, "AGENTS.md"), "forget clears the canon")
	require.NoFileExists(t, repoFile(repo), "forget removes the file through the push")

	st, err := state.Load(f.vault.StatePath())
	require.NoError(t, err)

	for _, a := range f.engine.Agents() {
		base, ok := st.Base(kind.Projects, a.ID)
		if !ok {
			continue
		}

		require.NotContains(t, base, repoID(repo)+"/AGENTS.md", "forget clears the base of %s", a.ID)
	}

	policy, err := os.ReadFile(filepath.Join(f.vault.ProjectsDir(), repoID(repo), "policy.json"))
	require.NoError(t, err)
	require.NotContains(t, string(policy), "AGENTS.md", "forget clears the policy entry")

	report = f.sync(t)
	require.Empty(t, report.Kind(kind.Projects).Agents, "a forgotten file is no longer synced")
}

func TestProjectForgetRemovesLocalEdits(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)
	f.enableProject(t, "AGENTS.md")

	write(t, repoFile(repo), "# rules\n")
	f.sync(t)

	write(t, repoFile(repo), "# edited after the last sync\n")

	_, err := f.engine.ProjectForget(t.Context(), "AGENTS.md")
	require.NoError(t, err)
	require.NoFileExists(t, repoFile(repo), "forget removes the file even with pending local edits")
	require.NoFileExists(t, vaultProject(f, repo, "AGENTS.md"))

	report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
	require.NoError(t, err)
	require.Empty(t, report.ConflictsOf(kind.Projects), "forget leaves no dangling conflicts")
	require.Empty(t, report.Kind(kind.Projects).Kept)
}

func TestProjectForgetRemovesFenceOnlyFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)
	f.enableProject(t, "AGENTS.md")
	ignoreInRepo(t, repo, "AGENTS.md")

	writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")
	f.sync(t)

	require.FileExists(t, repoFile(repo), "the digest creates a fence-only file")
	require.NoFileExists(t, vaultProject(f, repo, "AGENTS.md"))

	_, err := f.engine.ProjectForget(t.Context(), "AGENTS.md")
	require.NoError(t, err)
	require.NoFileExists(t, repoFile(repo), "forget removes a fence-only file too")

	report := f.sync(t)
	require.Empty(t, report.Kind(kind.Projects).Agents, "the policy entry is gone")
}

func TestProjectHardeningRefusesLinks(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)
	f.enableProject(t, ".mcp.json")

	f.engine.Secrets().Set("API_TOKEN", "tok-123456")
	require.NoError(t, f.engine.Secrets().Save())

	f.seedProjectCanon(t, repo, ".mcp.json", `{"mcpServers":{"ctx7":{"headers":{"Authorization":"{secret:API_TOKEN}"}}}}`)

	target := filepath.Join(f.home, ".claude.json")
	before := read(t, target)

	require.NoError(t, os.Symlink(target, filepath.Join(repo, ".mcp.json")))

	report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
	require.NoError(t, err)
	require.Contains(t, strings.Join(report.Errors(), " "), "refusing to touch a symlink")
	require.Equal(t, before, read(t, target), "no write-through into the symlink target")

	info, err := os.Lstat(filepath.Join(repo, ".mcp.json"))
	require.NoError(t, err)
	require.NotEqual(t, os.FileMode(0), info.Mode()&os.ModeSymlink, "the symlink stays")

	require.NoError(t, os.Remove(filepath.Join(repo, ".mcp.json")))

	other := filepath.Join(repo, "shared-config.json")
	write(t, other, `{"mcpServers": {}}`)
	require.NoError(t, os.Link(other, filepath.Join(repo, ".mcp.json")))

	report, err = f.engine.Sync(t.Context(), engine.SyncOptions{})
	require.NoError(t, err)
	require.Contains(t, strings.Join(report.Errors(), " "), "refusing to touch a file with several hard links")
	require.JSONEq(t, `{"mcpServers": {}}`, read(t, other))

	require.NoError(t, os.Remove(filepath.Join(repo, ".mcp.json")))
	require.NoError(t, os.Remove(other))

	f.sync(t)
	require.Contains(t, read(t, filepath.Join(repo, ".mcp.json")), "ctx7")
}

func TestProjectFailClosedOnGitErrors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)
	f.enableProject(t, ".mcp.json")

	f.engine.Secrets().Set("API_TOKEN", "tok-123456")
	require.NoError(t, f.engine.Secrets().Save())

	f.seedProjectCanon(t, repo, ".mcp.json", `{"mcpServers":{"ctx7":{"headers":{"Authorization":"{secret:API_TOKEN}"}}}}`)

	f.sync(t)
	require.Contains(t, read(t, filepath.Join(repo, ".mcp.json")), "${API_TOKEN}")

	require.NoError(t, os.RemoveAll(filepath.Join(repo, ".git")))

	report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
	require.NoError(t, err)
	require.Contains(t, strings.Join(report.Kind(kind.Projects).Warnings, " "), "check-ignore", "git errors are visible")

	require.Contains(t, read(t, filepath.Join(repo, ".mcp.json")), "${API_TOKEN}", "fail-closed keeps the env form")
}

func TestProjectMultiSurfaceCursor(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	cursorHome(t, f)
	f.enableAgent(t, agent.CursorID)

	repo := newRepo(t)
	f.useRepo(t, repo)
	f.enableProject(t, ".cursor/mcp.json", ".cursor/rules")

	write(t, filepath.Join(repo, ".cursor", "mcp.json"), `{"mcpServers": {"cursor-srv": {"url": "https://cursor.example.com"}}}`)
	write(t, filepath.Join(repo, ".cursor", "rules", "style.mdc"), "# style\n")
	write(t, filepath.Join(repo, ".cursor", "rules", "notes.txt"), "foreign\n")

	report := f.sync(t)
	require.Len(t, report.Kind(kind.Projects).Agents, 1, "both surfaces merge into one agent result")

	result, ok := report.Kind(kind.Projects).Agent(agent.CursorID)
	require.True(t, ok)
	require.Equal(t, engine.ActionNoop, result.Action)
	require.Len(t, report.Kind(kind.Projects).Pulled, 2, "both surfaces flow into the canon")

	for _, change := range report.Kind(kind.Projects).Pulled {
		require.Equal(t, agent.CursorID, change.Agent)
	}

	require.Equal(t, "# style\n", read(t, filepath.Join(f.vault.ProjectsDir(), repoID(repo), ".cursor", "rules", "style.mdc")))
	require.Contains(t, read(t, filepath.Join(f.vault.ProjectsDir(), repoID(repo), ".cursor", "mcp.json")), "cursor-srv")

	write(t, filepath.Join(f.vault.ProjectsDir(), repoID(repo), ".cursor", "rules", "style.mdc"), "# style v2\n")
	write(t, filepath.Join(f.vault.ProjectsDir(), repoID(repo), ".cursor", "mcp.json"), `{"mcpServers": {"cursor-srv": {"url": "https://cursor2.example.com"}}}`)

	report = f.sync(t)
	require.Equal(t, "# style v2\n", read(t, filepath.Join(repo, ".cursor", "rules", "style.mdc")))

	result, ok = report.Kind(kind.Projects).Agent(agent.CursorID)
	require.True(t, ok)
	require.Len(t, result.Changes, 2, "both surfaces merge their changes into one result")

	require.NoError(t, os.Remove(filepath.Join(f.vault.ProjectsDir(), repoID(repo), ".cursor", "rules", "style.mdc")))

	f.sync(t)
	require.NoFileExists(t, filepath.Join(repo, ".cursor", "rules", "style.mdc"), "an explicit canon deletion removes the file")
	require.FileExists(t, filepath.Join(repo, ".cursor", "rules", "notes.txt"))
}

func TestProjectDigestUsesNotesSlug(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)
	f.enableProjectWith(t, engine.ProjectOptions{AllowSecrets: true}, "AGENTS.md")

	write(t, repoFile(repo), "# repo rules\n")
	writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")

	report := f.sync(t)
	require.NotEmpty(t, digestResults(report, engine.DigestRefreshed))

	require.FileExists(t, vaultProject(f, repo, "AGENTS.md"), "the canon lives under the git identity")
	require.FileExists(t, filepath.Join(f.vault.MemoryDir(), memory.Slug(repo), "MEMORY.md"), "memory notes keep the path slug")

	fence := read(t, repoFile(repo))
	require.True(t, strings.HasPrefix(fence, "<!-- beadle:memory:"), "the fence is written")
	require.Contains(t, fence, "hook", "the digest renders the notes found under the path slug")
}
