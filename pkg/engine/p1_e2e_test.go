package engine_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	proj "github.com/odiumuniverse/beadle/pkg/project"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

func e2eVault(t *testing.T) (*vault.Vault, *config.Config) {
	t.Helper()

	v := vault.New(filepath.Join(t.TempDir(), "vault"))
	require.NoError(t, v.Init())

	cfg, err := config.Load(v.ConfigPath())
	require.NoError(t, err)

	cfg.Enable(agent.ClaudeCodeID)
	cfg.Enable(agent.OpenCodeID)
	require.NoError(t, cfg.Save(v.ConfigPath()))

	return v, cfg
}

func e2eEngine(t *testing.T, v *vault.Vault, cfg *config.Config, home, cwd string) *engine.Engine {
	t.Helper()

	e, err := engine.New(v, cfg, agent.All(home, cwd), engine.WithHome(home), engine.WithCwd(cwd))
	require.NoError(t, err)

	return e
}

func sha256Of(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: tests read their own temp files
	require.NoError(t, err)

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

func TestProjectE2EClonesAndWorktree(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	work := t.TempDir()
	seed := filepath.Join(work, "seed")
	origin := filepath.Join(work, "origin.git")

	gitIn(t, work, "init", "-q", seed)
	gitIn(t, seed, "config", "user.email", "test@example.com")
	gitIn(t, seed, "config", "user.name", "Test")

	write(t, filepath.Join(seed, ".mcp.json"), `{"mcpServers": {"ctx7": {"headers": {"Authorization": "Bearer abc123"}}}}`)
	write(t, filepath.Join(seed, "AGENTS.md"), "# rules\n")
	gitIn(t, seed, "add", ".mcp.json", "AGENTS.md")
	gitIn(t, seed, "commit", "-q", "-m", "init")

	gitIn(t, work, "init", "-q", "--bare", origin)
	gitIn(t, seed, "push", "-q", origin, "HEAD:refs/heads/main")
	gitIn(t, origin, "symbolic-ref", "HEAD", "refs/heads/main")

	cloneA := filepath.Join(work, "alpha")
	cloneB := filepath.Join(work, "beta")

	gitIn(t, work, "clone", "-q", origin, cloneA)
	gitIn(t, work, "clone", "-q", origin, cloneB)

	gitIn(t, cloneA, "config", "user.email", "test@example.com")
	gitIn(t, cloneA, "config", "user.name", "Test")
	gitIn(t, cloneB, "config", "user.email", "test@example.com")
	gitIn(t, cloneB, "config", "user.name", "Test")

	gitIn(t, cloneA, "remote", "set-url", "origin", "git@github.example.com:org/product.git")
	gitIn(t, cloneB, "remote", "set-url", "origin", "https://github.example.com/org/product.git")

	require.Equal(t, proj.Resolve(cloneA).ID, proj.Resolve(cloneB).ID, "ssh and https clones share one identity")

	home := t.TempDir()
	write(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {}}`)
	write(t, filepath.Join(home, ".config", "opencode", "AGENTS.md"), "# global\n")

	v, cfg := e2eVault(t)

	eA := e2eEngine(t, v, cfg, home, cloneA)

	_, err := eA.ProjectEnable(t.Context(), ".mcp.json", engine.ProjectOptions{})
	require.NoError(t, err)

	_, err = eA.ProjectEnable(t.Context(), "AGENTS.md", engine.ProjectOptions{})
	require.NoError(t, err)

	report, err := eA.Sync(t.Context(), engine.SyncOptions{})
	require.NoError(t, err)
	require.Empty(t, report.Errors())

	id := proj.Resolve(cloneA).ID
	canon := func(rel string) string { return filepath.Join(v.ProjectsDir(), id, filepath.FromSlash(rel)) }

	require.Contains(t, read(t, canon(".mcp.json")), "{secret:AUTHORIZATION}")
	require.NotContains(t, read(t, canon(".mcp.json")), "abc123")
	require.Contains(t, read(t, filepath.Join(cloneA, ".mcp.json")), "${AUTHORIZATION}", "a tracked file gets the env form")
	require.NotContains(t, read(t, filepath.Join(cloneA, ".mcp.json")), "abc123")

	hashA := sha256Of(t, filepath.Join(cloneA, "AGENTS.md"))

	gitIn(t, cloneA, "add", "-A")
	gitIn(t, cloneA, "commit", "-q", "-m", "beadle")
	gitIn(t, cloneA, "push", "-q", origin, "HEAD:main")
	gitIn(t, cloneB, "fetch", "-q", origin)
	gitIn(t, cloneB, "merge", "-q", "--ff-only", "FETCH_HEAD")

	eB := e2eEngine(t, v, cfg, home, cloneB)

	report, err = eB.Sync(t.Context(), engine.SyncOptions{})
	require.NoError(t, err)
	require.Equal(t, engine.ActionNoop, report.Action(kind.Projects, agent.ClaudeCodeID), "a fresh clone is in sync")
	require.Equal(t, engine.ActionNoop, report.Action(kind.Projects, agent.OpenCodeID))

	write(t, canon("AGENTS.md"), "# rules v2\n")

	report, err = eB.Sync(t.Context(), engine.SyncOptions{})
	require.NoError(t, err)
	require.Equal(t, engine.ActionPushed, report.Action(kind.Projects, agent.OpenCodeID))
	require.Equal(t, "# rules v2\n", read(t, filepath.Join(cloneB, "AGENTS.md")))

	worktree := filepath.Join(work, "wt")
	gitIn(t, cloneB, "worktree", "add", "-q", worktree, "HEAD")

	eW := e2eEngine(t, v, cfg, home, worktree)

	report, err = eW.Sync(t.Context(), engine.SyncOptions{})
	require.NoError(t, err)
	require.Equal(t, engine.ActionNoop, report.Action(kind.Projects, agent.OpenCodeID), "a worktree shares the identity")

	require.Equal(t, hashA, sha256Of(t, filepath.Join(cloneA, "AGENTS.md")), "clone A is untouched")

	require.NoError(t, os.Remove(filepath.Join(worktree, "AGENTS.md")))

	report, err = eW.Sync(t.Context(), engine.SyncOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, report.Kind(kind.Projects).Kept)
	require.FileExists(t, canon("AGENTS.md"), "a worktree deletion keeps the canon")
	require.NoFileExists(t, filepath.Join(worktree, "AGENTS.md"))

	_, err = eW.ProjectForget(t.Context(), "AGENTS.md")
	require.NoError(t, err)

	require.NoFileExists(t, canon("AGENTS.md"))
	require.NoFileExists(t, filepath.Join(worktree, "AGENTS.md"))
	require.FileExists(t, filepath.Join(cloneB, "AGENTS.md"), "forget only touches its own working tree")
	require.Equal(t, hashA, sha256Of(t, filepath.Join(cloneA, "AGENTS.md")))
}
