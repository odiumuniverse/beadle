package engine_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

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
	if err := v.Init(); err != nil {
		t.Fatalf("init vault: %v", err)
	}

	cfg, err := config.Load(v.ConfigPath())
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	cfg.Enable(agent.ClaudeCodeID)
	cfg.Enable(agent.OpenCodeID)

	if err := cfg.Save(v.ConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	return v, cfg
}

func e2eEngine(t *testing.T, v *vault.Vault, cfg *config.Config, home, cwd string) *engine.Engine {
	t.Helper()

	e, err := engine.New(v, cfg, agent.All(home, cwd), engine.WithHome(home), engine.WithCwd(cwd))
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	return e
}

func sha256Of(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: tests read their own temp files
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

//nolint:funlen // one end-to-end scenario: two clones, a worktree and the identity gate
func TestProjectE2EClonesAndWorktree(t *testing.T) {
	Convey("Given two clones of one origin and a worktree", t, func() {
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

		So(proj.Resolve(cloneA).ID, ShouldEqual, proj.Resolve(cloneB).ID)

		home := t.TempDir()
		write(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {}}`)
		write(t, filepath.Join(home, ".config", "opencode", "AGENTS.md"), "# global\n")

		v, cfg := e2eVault(t)

		eA := e2eEngine(t, v, cfg, home, cloneA)

		_, err := eA.ProjectEnable(t.Context(), ".mcp.json", engine.ProjectOptions{})
		So(err, ShouldBeNil)

		_, err = eA.ProjectEnable(t.Context(), "AGENTS.md", engine.ProjectOptions{})
		So(err, ShouldBeNil)

		report, err := eA.Sync(t.Context(), engine.SyncOptions{})
		So(err, ShouldBeNil)

		id := proj.Resolve(cloneA).ID
		canon := func(rel string) string { return filepath.Join(v.ProjectsDir(), id, filepath.FromSlash(rel)) }

		hashA := sha256Of(t, filepath.Join(cloneA, "AGENTS.md"))

		gitIn(t, cloneA, "add", "-A")
		gitIn(t, cloneA, "commit", "-q", "-m", "beadle")
		gitIn(t, cloneA, "push", "-q", origin, "HEAD:main")
		gitIn(t, cloneB, "fetch", "-q", origin)
		gitIn(t, cloneB, "merge", "-q", "--ff-only", "FETCH_HEAD")

		eB := e2eEngine(t, v, cfg, home, cloneB)

		Convey("When clone B syncs and the canon changes", func() {
			So(report.Errors(), ShouldBeEmpty)
			So(read(t, canon(".mcp.json")), ShouldContainSubstring, "{secret:AUTHORIZATION}")
			So(read(t, canon(".mcp.json")), ShouldNotContainSubstring, "abc123")
			So(read(t, filepath.Join(cloneA, ".mcp.json")), ShouldContainSubstring, "${AUTHORIZATION}")
			So(read(t, filepath.Join(cloneA, ".mcp.json")), ShouldNotContainSubstring, "abc123")

			report, err := eB.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)
			So(report.Action(kind.Projects, agent.ClaudeCodeID), ShouldEqual, engine.ActionNoop)
			So(report.Action(kind.Projects, agent.OpenCodeID), ShouldEqual, engine.ActionAlias)

			write(t, canon("AGENTS.md"), "# rules v2\n")

			report, err = eB.Sync(t.Context(), engine.SyncOptions{})
			So(err, ShouldBeNil)

			Convey("Then clone B pulls and pushes and the worktree shares the identity", func() {
				So(report.Action(kind.Projects, agent.ClaudeCodeID), ShouldEqual, engine.ActionPushed)
				So(read(t, filepath.Join(cloneB, "AGENTS.md")), ShouldEqual, "# rules v2\n")

				worktree := filepath.Join(work, "wt")
				gitIn(t, cloneB, "worktree", "add", "-q", worktree, "HEAD")

				eW := e2eEngine(t, v, cfg, home, worktree)

				report, err = eW.Sync(t.Context(), engine.SyncOptions{})
				So(err, ShouldBeNil)

				Convey("And a worktree deletion keeps the canon while forget removes it", func() {
					So(report.Action(kind.Projects, agent.ClaudeCodeID), ShouldEqual, engine.ActionNoop)
					So(sha256Of(t, filepath.Join(cloneA, "AGENTS.md")), ShouldEqual, hashA)

					So(os.Remove(filepath.Join(worktree, "AGENTS.md")), ShouldBeNil)

					report, err = eW.Sync(t.Context(), engine.SyncOptions{})
					So(err, ShouldBeNil)

					So(report.Kind(kind.Projects).Kept, ShouldNotBeEmpty)

					_, canonErr := os.Stat(canon("AGENTS.md"))
					_, wtErr := os.Stat(filepath.Join(worktree, "AGENTS.md"))

					So(canonErr, ShouldBeNil)
					So(errors.Is(wtErr, fs.ErrNotExist), ShouldBeTrue)

					_, err = eW.ProjectForget(t.Context(), "AGENTS.md")
					So(err, ShouldBeNil)

					_, canonErr = os.Stat(canon("AGENTS.md"))
					_, cloneBErr := os.Stat(filepath.Join(cloneB, "AGENTS.md"))

					So(errors.Is(canonErr, fs.ErrNotExist), ShouldBeTrue)
					So(cloneBErr, ShouldBeNil)
					So(sha256Of(t, filepath.Join(cloneA, "AGENTS.md")), ShouldEqual, hashA)
				})
			})
		})
	})
}
