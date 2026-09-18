package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/agent"
	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/engine"
	"github.com/odiumuniverse/agents-sync/pkg/inbox"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
	"github.com/odiumuniverse/agents-sync/pkg/memory"
)

const (
	u12Token  = "ghp_abcdefghijklmnopqrstuvwxyz012345"
	u12Line   = "prefer short review loops"
	u12ApiKey = "abcdefgh12345678"
)

func inboxResults(report *engine.Report, k kind.ID, agentID string, action engine.InboxAction) []engine.InboxResult {
	kr := report.Kind(k)
	if kr == nil {
		return nil
	}

	var out []engine.InboxResult

	for _, result := range kr.Inbox {
		if result.Agent == agentID && result.Action == action {
			out = append(out, result)
		}
	}

	return out
}

func openCodeInbox(f *fixture) string {
	return agent.InboxPath(f.home, agent.OpenCodeID)
}

func writeInboxLines(t *testing.T, path string, lines ...string) {
	t.Helper()

	write(t, path, strings.Join(lines, "\n")+"\n")
}

func inboxHash(t *testing.T, text string) string {
	t.Helper()

	lines, err := inbox.Read(writeInboxTemp(t, text))
	require.NoError(t, err)
	require.Len(t, lines, 1)

	return lines[0].Hash
}

func writeInboxTemp(t *testing.T, text string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "inbox.md")
	write(t, path, text+"\n")

	return path
}

func vaultMemoryNote(f *fixture, slug, name string) string {
	return filepath.Join(f.vault.MemoryDir(), slug, name)
}

func canonFiles(t *testing.T, f *fixture) string {
	t.Helper()

	var out strings.Builder

	err := filepath.WalkDir(f.vault.MemoryDir(), func(path string, entry os.DirEntry, err error) error {
		require.NoError(t, err)

		if entry.IsDir() {
			return nil
		}

		data, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own vault
		require.NoError(t, err)

		out.WriteString(path)
		out.Write(data)

		return nil
	})
	require.NoError(t, err)

	return out.String()
}

func objectsText(t *testing.T, f *fixture) string {
	t.Helper()

	var out strings.Builder

	err := filepath.WalkDir(f.vault.ObjectsDir(), func(path string, entry os.DirEntry, err error) error {
		require.NoError(t, err)

		if entry.IsDir() {
			return nil
		}

		data, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own vault
		require.NoError(t, err)

		out.Write(data)

		return nil
	})
	require.NoError(t, err)

	return out.String()
}

func TestInboxFanIn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	writeInboxLines(t, openCodeInbox(f), "<!-- header -->", "", u12Line)

	report := f.sync(t)

	hash := inboxHash(t, u12Line)
	note := vaultMemoryNote(f, memory.Slug(repo), "inbox-"+hash+".md")

	require.NotEmpty(t, inboxResults(report, kind.Memory, agent.OpenCodeID, engine.InboxCreated))
	require.FileExists(t, note)

	data := read(t, note)
	require.Contains(t, data, "name: \""+u12Line+"\"")
	require.Contains(t, data, "origin: inbox")
	require.Contains(t, data, "agent: opencode")
	require.Contains(t, data, "hash: "+hash)
	require.Contains(t, data, "<!-- transcribed: opencode -->")

	before := canonFiles(t, f)

	report = f.sync(t)
	require.Empty(t, inboxResults(report, kind.Memory, agent.OpenCodeID, engine.InboxCreated), "a consumed line creates nothing")
	require.NotEmpty(t, inboxResults(report, kind.Memory, agent.OpenCodeID, engine.InboxSkipped), "the standing line is reported as skipped")
	require.Equal(t, before, canonFiles(t, f), "a repeated sync is a noop")

	require.NoError(t, os.Remove(f.vault.StatePath()))
	f.sync(t)
	require.Equal(t, before, canonFiles(t, f), "dedupe survives the loss of state.json")
}

func TestInboxDedupeAcrossSlugs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	hash := inboxHash(t, u12Line)
	write(t, vaultMemoryNote(f, "-Users-other", "inbox-"+hash+".md"), "elsewhere\n")
	writeInboxLines(t, openCodeInbox(f), u12Line)

	report := f.sync(t)

	require.NotEmpty(t, inboxResults(report, kind.Memory, agent.OpenCodeID, engine.InboxSkipped))
	require.NoFileExists(t, vaultMemoryNote(f, memory.Slug(repo), "inbox-"+hash+".md"), "a line consumed by another slug is skipped")
}

func TestInboxOptOutAndReimport(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	writeInboxLines(t, openCodeInbox(f), u12Line)
	f.sync(t)

	hash := inboxHash(t, u12Line)
	note := vaultMemoryNote(f, memory.Slug(repo), "inbox-"+hash+".md")
	require.FileExists(t, note)

	require.NoError(t, os.Remove(note))
	write(t, openCodeInbox(f), "other line\n")

	f.sync(t)
	require.NoFileExists(t, note, "a removed note is not reimported while its line is gone")

	writeInboxLines(t, openCodeInbox(f), u12Line)
	f.sync(t)
	require.FileExists(t, note, "the line comes back when it is written again")
}

func TestInboxDirectionsAndModes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	writeInboxLines(t, openCodeInbox(f), u12Line)

	report := f.run(t, engine.SyncOptions{DryRun: true})
	require.NotEmpty(t, inboxResults(report, kind.Memory, agent.OpenCodeID, engine.InboxWouldCreate))
	require.NoDirExists(t, filepath.Join(f.vault.MemoryDir(), memory.Slug(repo)), "a dry run writes no notes")

	report = f.run(t, engine.SyncOptions{Direction: config.ModePush})
	require.Empty(t, report.Kind(kind.Memory).Inbox, "push never reads inbox files")

	report = f.run(t, engine.SyncOptions{Direction: config.ModePull})
	require.NotEmpty(t, inboxResults(report, kind.Memory, agent.OpenCodeID, engine.InboxCreated), "pull is an agent-to-vault direction: it consumes inbox lines")
	require.FileExists(t, vaultMemoryNote(f, memory.Slug(repo), "inbox-"+inboxHash(t, u12Line)+".md"))

	second := "another line"
	writeInboxLines(t, openCodeInbox(f), second)

	f.config.SetMode(agent.OpenCodeID, kind.Memory, config.ModeOff)
	f.sync(t)
	require.NoFileExists(t, vaultMemoryNote(f, memory.Slug(repo), "inbox-"+inboxHash(t, second)+".md"), "a mode-off agent is not consumed")
}

func TestSecretGateDirectPull(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	write(t, claudeMemory(f, memory.Slug(repo), "MEMORY.md"), "api_key: "+u12Token+"\n")

	f.sync(t)

	canon := read(t, vaultMemoryNote(f, memory.Slug(repo), "MEMORY.md"))
	require.NotContains(t, canon, u12Token)
	require.Contains(t, canon, "{secret:")

	require.NotContains(t, canonFiles(t, f), u12Token)
	require.NotContains(t, objectsText(t, f), u12Token)
	require.NotContains(t, read(t, f.vault.StatePath()), u12Token)

	require.Equal(t, 1, f.engine.Secrets().Len())
	require.Equal(t, u12Token, secretValue(t, f, "SECRET"))

	require.Equal(t, "api_key: "+u12Token+"\n", read(t, claudeMemory(f, memory.Slug(repo), "MEMORY.md")), "the agent keeps the literal value")
}

func secretValue(t *testing.T, f *fixture, prefix string) string {
	t.Helper()

	for _, name := range f.engine.Secrets().Names() {
		if strings.HasPrefix(name, prefix) {
			value, ok := f.engine.Secrets().Get(name)
			require.True(t, ok)

			return value
		}
	}

	t.Fatalf("no secret named %s*", prefix)

	return ""
}

func TestSecretGateLaterKeyPair(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	write(t, claudeMemory(f, memory.Slug(repo), "MEMORY.md"), `{"token":"","password":"hunter2secret"}`+"\n")

	f.sync(t)

	canon := read(t, vaultMemoryNote(f, memory.Slug(repo), "MEMORY.md"))
	require.NotContains(t, canon, "hunter2secret")
	require.Contains(t, canon, "{secret:PASSWORD}")
	require.NotContains(t, objectsText(t, f), "hunter2secret")
	require.NotContains(t, read(t, f.vault.StatePath()), "hunter2secret")
}

func TestSecretGateMigrationAndIdempotency(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	write(t, vaultMemoryNote(f, memory.Slug(repo), "MEMORY.md"), "notes\ntoken: "+u12Token+"\n")

	f.sync(t)

	canon := read(t, vaultMemoryNote(f, memory.Slug(repo), "MEMORY.md"))
	require.NotContains(t, canon, u12Token)
	require.Contains(t, canon, "{secret:SECRET}")

	require.NotContains(t, objectsText(t, f), u12Token, "snapshots hold refs only")
	require.NotContains(t, read(t, f.vault.StatePath()), u12Token)

	before := canonFiles(t, f)

	second := f.sync(t)
	require.False(t, second.Kind(kind.Memory).VaultChanged, "a migrated canon is a noop")
	require.Equal(t, before, canonFiles(t, f))
}

func TestSecretGateConflictBlobs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	write(t, claudeMemory(f, memory.Slug(repo), "MEMORY.md"), "api_key: "+u12ApiKey+"\n")
	f.sync(t)

	write(t, claudeMemory(f, memory.Slug(repo), "MEMORY.md"), "api_key: "+u12ApiKey+"\nagent edit\n")
	write(t, vaultMemoryNote(f, memory.Slug(repo), "MEMORY.md"), "api_key: {secret:API_KEY}\nvault edit\n")

	report := f.sync(t)
	require.Len(t, report.ConflictsOf(kind.Memory), 1)

	require.NotContains(t, objectsText(t, f), u12ApiKey, "conflict blobs hold refs only")
}

func TestOutboundMissingSecretSkipsNote(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	write(t, vaultMemoryNote(f, memory.Slug(repo), "MEMORY.md"), "api_key: {secret:GONE_12345}\n")
	write(t, filepath.Join(claudeSlug(f, memory.Slug(repo)), "memory", "other.md"), "x\n")

	report := f.sync(t)

	result, ok := report.Kind(kind.Memory).Agent(agent.ClaudeCodeID)
	require.True(t, ok)
	require.Equal(t, engine.ActionSkipped, result.Action)
	require.Contains(t, result.Note, "missing secrets: GONE_12345")
	require.NoFileExists(t, claudeMemory(f, memory.Slug(repo), "MEMORY.md"), "a note with a missing secret is not written")
}

func TestOutboundEnvRefNotExpanded(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	write(t, vaultMemoryNote(f, memory.Slug(repo), "ENV.md"), "value: {env:MY_TOKEN}\n")
	write(t, claudeMemory(f, memory.Slug(repo), "seed.md"), "seed\n")

	f.sync(t)

	require.Contains(t, read(t, claudeMemory(f, memory.Slug(repo), "ENV.md")), "{env:MY_TOKEN}", "env refs are not expanded")
}

func TestDigestRedactsRefs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	write(t, vaultMemoryNote(f, memory.Slug(repo), "MEMORY.md"), "---\ndescription: api_key: {secret:API_KEY}\n---\nbody\n")

	f.sync(t)

	data := read(t, repoFile(repo))
	require.Contains(t, data, "[redacted]")
	require.NotContains(t, data, "{secret:API_KEY}")
}

func TestMemoryGitignoreCleanup(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	legacy := read(t, filepath.Join(f.vault.Root(), ".gitignore")) + vaultLegacyMemoryLines()
	write(t, filepath.Join(f.vault.Root(), ".gitignore"), legacy)

	write(t, claudeMemory(f, memory.Slug(repo), "MEMORY.md"), "api_key: "+u12Token+"\n")

	f.sync(t)

	ignore := read(t, filepath.Join(f.vault.Root(), ".gitignore"))
	require.NotContains(t, ignore, "memory/")
	require.NotContains(t, ignore, "U-12 secret gate lands\nmemory")

	ignored, err := f.vault.MemoryIgnored()
	require.NoError(t, err)
	require.False(t, ignored)

	f.sync(t)
	require.Equal(t, ignore, read(t, filepath.Join(f.vault.Root(), ".gitignore")), "cleanup is idempotent")
}

func vaultLegacyMemoryLines() string {
	return "# memory notes stay out of git until the U-12 secret gate lands\nmemory/\n"
}

func TestDoctorFlagsTrackedPlaintextNotes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	write(t, vaultMemoryNote(f, memory.Slug(repo), "MEMORY.md"), "api_key: "+u12Token+"\n")

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityError, "plaintext secrets are not ignored by git"), "issues: %v", issues)
}

func TestDoctorReportsNoteRefs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)
	repo := newRepo(t)
	f.useRepo(t, repo)

	write(t, vaultMemoryNote(f, memory.Slug(repo), "MEMORY.md"), "api_key: {secret:API_KEY}\n")
	f.engine.Secrets().Set("API_KEY", u12ApiKey)
	require.NoError(t, f.engine.Secrets().Save())

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityInfo, "1 secret reference(s) in 1 memory note(s)"), "issues: %v", issues)

	f.engine.Secrets().Delete("API_KEY")

	issues, err = f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityError, "secret API_KEY has no value"), "issues: %v", issues)
}
