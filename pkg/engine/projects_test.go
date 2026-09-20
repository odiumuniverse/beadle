package engine_test

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/digest"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/memory"
	proj "github.com/odiumuniverse/beadle/pkg/project"
	"github.com/odiumuniverse/beadle/pkg/state"
)

func (f *fixture) useRepo(t *testing.T, repo string) {
	t.Helper()

	f.useCwd(t, repo)
}

func (f *fixture) useCwd(t *testing.T, dir string) {
	t.Helper()

	e, err := engine.New(f.vault, f.config, agent.All(f.home, dir), engine.WithHome(f.home), engine.WithCwd(dir))
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	f.engine = e
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()

	all := append([]string{"-C", dir}, args...)

	out, err := exec.CommandContext(t.Context(), "git", all...).CombinedOutput() //nolint:gosec // G204: fixed git subcommands in tests
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}

	return string(out)
}

func newRepo(t *testing.T) string {
	t.Helper()

	repo := t.TempDir()
	gitIn(t, repo, "init", "-q")

	return repo
}

func repoID(repo string) string { return proj.Resolve(repo).ID }

func repoFile(repo string) string { return filepath.Join(repo, "AGENTS.md") }

func vaultProject(f *fixture, repo, name string) string {
	return filepath.Join(f.vault.ProjectsDir(), repoID(repo), name)
}

func (f *fixture) enableProject(t *testing.T, rels ...string) {
	t.Helper()

	f.enableProjectWith(t, engine.ProjectOptions{}, rels...)
}

func (f *fixture) seedProjectCanon(t *testing.T, repo, rel, content string) {
	t.Helper()

	write(t, filepath.Join(f.vault.ProjectsDir(), repoID(repo), filepath.FromSlash(rel)), content)

	if err := os.Remove(filepath.Join(repo, filepath.FromSlash(rel))); err != nil {
		t.Fatalf("remove skeleton: %v", err)
	}
}

func (f *fixture) enableProjectWith(t *testing.T, opts engine.ProjectOptions, rels ...string) {
	t.Helper()

	for _, rel := range rels {
		if _, err := f.engine.ProjectEnable(t.Context(), rel, opts); err != nil {
			t.Fatalf("enable %s: %v", rel, err)
		}
	}
}

func ignoreInRepo(t *testing.T, repo string, rels ...string) {
	t.Helper()

	path := filepath.Join(repo, ".gitignore")

	existing := ""

	if data, err := os.ReadFile(path); err == nil { //nolint:gosec // G304: tests read their own temp files
		existing = string(data)
	}

	write(t, path, existing+strings.Join(rels, "\n")+"\n")
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
	if err != nil {
		t.Fatalf("load state: %v", err)
	}

	base, ok := st.Base(k, agentID)
	if !ok {
		t.Fatal("no base")
	}

	hash, ok := base[key]
	if !ok {
		t.Fatalf("no base key %s", key)
	}

	if len(hash) != 64 {
		t.Fatalf("hash length %d", len(hash))
	}

	data, err := os.ReadFile(filepath.Join(f.vault.ObjectsDir(), string(hash[:2]), string(hash[2:])))
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}

	return data
}

func TestProjectsSyncRoundTrip(t *testing.T) {
	Convey("Given an enabled project AGENTS.md", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")

		write(t, repoFile(repo), "# repo rules\n")

		report := f.sync(t)

		Convey("When syncs, edits and a deletion happen", func() {
			So(report.Kind(kind.Projects).VaultChanged, ShouldBeTrue)
			So(read(t, vaultProject(f, repo, "AGENTS.md")), ShouldEqual, "# repo rules\n")

			report = f.sync(t)
			So(report.Kind(kind.Projects).VaultChanged, ShouldBeFalse)
			So(report.Action(kind.Projects, agent.OpenCodeID), ShouldEqual, engine.ActionNoop)

			write(t, vaultProject(f, repo, "AGENTS.md"), "# canon edit\n")
			f.sync(t)
			So(read(t, repoFile(repo)), ShouldEqual, "# canon edit\n")

			write(t, repoFile(repo), "# local edit\n")
			f.sync(t)
			So(read(t, vaultProject(f, repo, "AGENTS.md")), ShouldEqual, "# local edit\n")

			So(os.Remove(repoFile(repo)), ShouldBeNil)

			report = f.sync(t)

			_, repoErr := os.Stat(repoFile(repo))

			Convey("Then deleting the repo file keeps the canon and imposes nothing", func() {
				So(read(t, vaultProject(f, repo, "AGENTS.md")), ShouldEqual, "# local edit\n")
				So(errors.Is(repoErr, fs.ErrNotExist), ShouldBeTrue)
				So(report.Kind(kind.Projects).Kept, ShouldNotBeEmpty)
				So(report.Action(kind.Projects, agent.OpenCodeID), ShouldEqual, engine.ActionNoop)
			})
		})
	})
}

func TestProjectsUntrackedFileIsReadOnly(t *testing.T) {
	Convey("Given a repo without a policy entry", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)

		write(t, repoFile(repo), "# repo rules\n")

		report := f.sync(t)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When sync runs", func() {
			_, canonErr := os.Stat(vaultProject(f, repo, "AGENTS.md"))

			Convey("Then the project stays read-only and doctor reports the identity", func() {
				So(report.Kind(kind.Projects).VaultChanged, ShouldBeFalse)
				So(errors.Is(canonErr, fs.ErrNotExist), ShouldBeTrue)
				So(read(t, repoFile(repo)), ShouldEqual, "# repo rules\n")
				So(hasIssue(issues, engine.SeverityInfo, "not a git checkout") || hasIssue(issues, engine.SeverityInfo, "project "), ShouldBeTrue)
			})
		})
	})
}

func TestProjectsConflictResolvedInEditor(t *testing.T) {
	Convey("Given a project conflict resolved in the editor", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")

		write(t, repoFile(repo), "v1\n")
		f.sync(t)

		write(t, repoFile(repo), "agent-edit\n")
		write(t, vaultProject(f, repo, "AGENTS.md"), "vault-edit\n")

		report := f.sync(t)
		So(report.ConflictsOf(kind.Projects), ShouldHaveLength, 1)

		c := f.conflict(t, kind.Projects, agent.OpenCodeID)

		file, err := f.engine.ConflictFile(c)
		So(err, ShouldBeNil)
		So(filepath.Ext(file), ShouldEqual, ".md")
		So(filepath.Base(file), ShouldContainSubstring, "projects-opencode-")
		So(read(t, file), ShouldContainSubstring, ">>>>>>> agent:opencode")

		write(t, file, "# resolved\n")

		Convey("When the edited file is taken", func() {
			_, err = f.engine.Resolve(t.Context(), []string{c.ID()}, engine.Resolution{Take: engine.TakeFile})
			So(err, ShouldBeNil)

			Convey("Then it lands on both sides", func() {
				So(read(t, vaultProject(f, repo, "AGENTS.md")), ShouldEqual, "# resolved\n")
				So(read(t, repoFile(repo)), ShouldEqual, "# resolved\n")
			})
		})
	})
}

func TestProjectsCanonDeletionRemovesFile(t *testing.T) {
	Convey("Given a canon deletion of an ignored project file", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")
		ignoreInRepo(t, repo, "AGENTS.md")

		write(t, repoFile(repo), "# repo rules\n")
		f.sync(t)

		So(os.Remove(vaultProject(f, repo, "AGENTS.md")), ShouldBeNil)

		f.sync(t)

		_, repoErr := os.Stat(repoFile(repo))
		_, canonErr := os.Stat(vaultProject(f, repo, "AGENTS.md"))

		Convey("When memory returns it recreates a fence-only file", func() {
			So(errors.Is(repoErr, fs.ErrNotExist), ShouldBeTrue)
			So(errors.Is(canonErr, fs.ErrNotExist), ShouldBeTrue)

			writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")
			f.sync(t)

			data := read(t, repoFile(repo))

			So(strings.HasPrefix(data, digest.BeginPrefix), ShouldBeTrue)
			So(data, ShouldNotContainSubstring, "# repo rules")
		})
	})
}

func TestProjectsConflictTakeAgent(t *testing.T) {
	Convey("Given a project conflict taken from the agent", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")

		write(t, repoFile(repo), "v1\n")
		f.sync(t)

		write(t, repoFile(repo), "agent-edit\n")
		write(t, vaultProject(f, repo, "AGENTS.md"), "vault-edit\n")
		f.sync(t)

		f.resolve(t, kind.Projects, agent.OpenCodeID, engine.TakeAgent)

		Convey("When resolved", func() {
			Convey("Then the agent value wins everywhere", func() {
				So(read(t, vaultProject(f, repo, "AGENTS.md")), ShouldEqual, "agent-edit\n")
				So(read(t, repoFile(repo)), ShouldEqual, "agent-edit\n")
			})
		})
	})
}

func TestProjectsFenceBlindNoop(t *testing.T) {
	Convey("Given an ignored project file with a digest fence", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")
		ignoreInRepo(t, repo, "AGENTS.md")

		write(t, repoFile(repo), "# repo rules\n")
		writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")

		report := f.sync(t)
		So(digestResults(report, engine.DigestRefreshed), ShouldNotBeEmpty)

		data := read(t, repoFile(repo))
		canon := read(t, vaultProject(f, repo, "AGENTS.md"))

		Convey("When the digest node changes", func() {
			So(strings.HasPrefix(data, digest.BeginPrefix), ShouldBeTrue)
			So(strings.HasSuffix(data, "# repo rules\n"), ShouldBeTrue)

			So(canon, ShouldEqual, "# repo rules\n")
			So(canon, ShouldNotContainSubstring, digest.BeginPrefix)
			So(string(baseBlob(t, f, kind.Projects, agent.OpenCodeID, repoID(repo)+"/AGENTS.md")), ShouldNotContainSubstring, digest.BeginPrefix)

			report = f.sync(t)
			So(report.Digest, ShouldBeEmpty)
			So(report.Action(kind.Projects, agent.OpenCodeID), ShouldEqual, engine.ActionNoop)

			writeMemoryCanon(t, f, repo, "feedback.md", "---\ndescription: second\n---\nbody\n")

			report = f.sync(t)
			So(digestResults(report, engine.DigestRefreshed), ShouldNotBeEmpty)

			data = read(t, repoFile(repo))

			Convey("Then the fence never reaches the canon and refreshes cleanly", func() {
				So(data, ShouldContainSubstring, "feedback.md")
				So(strings.HasSuffix(data, "# repo rules\n"), ShouldBeTrue)
				So(read(t, vaultProject(f, repo, "AGENTS.md")), ShouldEqual, "# repo rules\n")
			})
		})
	})
}

func TestProjectsFenceOnlyFileSurvives(t *testing.T) {
	Convey("Given a memory canon with no project body", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")
		ignoreInRepo(t, repo, "AGENTS.md")

		writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")

		report := f.sync(t)
		So(digestResults(report, engine.DigestCreated), ShouldNotBeEmpty)

		Convey("When it is synced again", func() {
			_, canonErr := os.Stat(vaultProject(f, repo, "AGENTS.md"))
			So(errors.Is(canonErr, fs.ErrNotExist), ShouldBeTrue)

			data := read(t, repoFile(repo))
			So(strings.HasPrefix(data, digest.BeginPrefix), ShouldBeTrue)

			report = f.sync(t)

			_, fileErr := os.Stat(repoFile(repo))

			Convey("Then the fence-only file is never deleted", func() {
				So(report.Action(kind.Projects, agent.OpenCodeID), ShouldEqual, engine.ActionNoop)
				So(fileErr, ShouldBeNil)
			})
		})
	})
}

func TestDigestRemovedWhenMemoryEmpty(t *testing.T) {
	Convey("Given a fence removed once memory is empty", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")
		ignoreInRepo(t, repo, "AGENTS.md")

		write(t, repoFile(repo), "# repo rules\n")
		writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")
		f.sync(t)

		So(os.Remove(filepath.Join(f.vault.MemoryDir(), memory.Slug(repo), "MEMORY.md")), ShouldBeNil)

		report := f.sync(t)

		Convey("When memory is emptied", func() {
			So(digestResults(report, engine.DigestRemoved), ShouldNotBeEmpty)
			So(read(t, repoFile(repo)), ShouldEqual, "# repo rules\n")

			report = f.sync(t)

			Convey("Then the fence is removed and the body kept", func() {
				So(report.Digest, ShouldBeEmpty)
			})
		})
	})
}

func TestDigestGates(t *testing.T) {
	Convey("Given digest write gates", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")
		ignoreInRepo(t, repo, "AGENTS.md")

		write(t, repoFile(repo), "# repo rules\n")
		writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")

		Convey("When pull, a kind filter, projects-off and mode-off are applied", func() {
			f.run(t, engine.SyncOptions{Direction: config.ModePull})
			So(read(t, repoFile(repo)), ShouldNotContainSubstring, digest.BeginPrefix)

			f.run(t, engine.SyncOptions{Kinds: []kind.ID{kind.Memory}})
			So(read(t, repoFile(repo)), ShouldNotContainSubstring, digest.BeginPrefix)

			f.config.SetKind(kind.Projects, config.ModeOff)
			f.sync(t)
			So(read(t, repoFile(repo)), ShouldNotContainSubstring, digest.BeginPrefix)

			f.config.SetKind(kind.Projects, config.ModeSync)
			f.config.SetMode(agent.OpenCodeID, kind.Projects, config.ModeOff)
			f.sync(t)

			Convey("Then no fence is ever written", func() {
				So(read(t, repoFile(repo)), ShouldNotContainSubstring, digest.BeginPrefix)
			})
		})
	})
}

func TestDigestAdoptBaseline(t *testing.T) {
	Convey("Given a repo file whose digest block was adopted", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProjectWith(t, engine.ProjectOptions{AllowSecrets: true}, "AGENTS.md")

		slug := memory.Slug(repo)
		notes := map[string][]byte{slug + "/MEMORY.md": []byte("---\ndescription: hook\n---\nbody\n")}
		block, _ := digest.Render(filepath.Join(f.home, ".claude", "projects", slug, "memory"), notes, digest.DefaultBudget)

		write(t, repoFile(repo), string(block)+"# repo rules\n")
		writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")

		report := f.sync(t)

		st, err := state.Load(f.vault.StatePath())
		So(err, ShouldBeNil)

		Convey("When it is adopted", func() {
			So(digestResults(report, engine.DigestAdopted), ShouldNotBeEmpty)
			So(read(t, repoFile(repo)), ShouldEqual, string(block)+"# repo rules\n")
			So(st.Renders, ShouldContainKey, repoFile(repo))
			So(st.Drift[repoFile(repo)].Count, ShouldEqual, 0)

			report = f.sync(t)

			Convey("Then the block is not rewritten and it settles into noop", func() {
				So(report.Digest, ShouldBeEmpty)
			})
		})
	})
}

func TestDigestDriftHoldAndRefresh(t *testing.T) {
	Convey("Given a manual edit inside the generated block", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProjectWith(t, engine.ProjectOptions{AllowSecrets: true}, "AGENTS.md")

		write(t, repoFile(repo), "# repo rules\n")
		writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")
		f.sync(t)

		edited := strings.Replace(read(t, repoFile(repo)), "hook", "hand edit", 1)
		write(t, repoFile(repo), edited)

		report := f.sync(t)

		So(digestResults(report, engine.DigestHeld), ShouldNotBeEmpty)
		So(strings.Join(report.Warnings, " "), ShouldContainSubstring, "manual edits inside the generated block")
		So(read(t, repoFile(repo)), ShouldEqual, edited)

		Convey("When drift accumulates and an explicit refresh runs", func() {
			st, err := state.Load(f.vault.StatePath())
			So(err, ShouldBeNil)
			So(st.Drift[repoFile(repo)].Count, ShouldEqual, 1)

			report = f.sync(t)
			So(digestResults(report, engine.DigestHeld), ShouldNotBeEmpty)

			st, err = state.Load(f.vault.StatePath())
			So(err, ShouldBeNil)
			So(st.Drift[repoFile(repo)].Count, ShouldEqual, 2)

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)
			So(hasIssue(issues, engine.SeverityError, "manual edits inside the generated block in "+repoFile(repo)), ShouldBeTrue)

			report, err = f.engine.Sync(t.Context(), engine.SyncOptions{Refresh: true})
			So(err, ShouldBeNil)

			st, err = state.Load(f.vault.StatePath())
			So(err, ShouldBeNil)

			Convey("Then the refresh resets drift and rewrites the block", func() {
				So(digestResults(report, engine.DigestRefreshed), ShouldNotBeEmpty)
				So(read(t, repoFile(repo)), ShouldNotContainSubstring, "hand edit")
				So(st.Drift[repoFile(repo)].Count, ShouldEqual, 0)

				So(f.sync(t).Action(kind.Projects, agent.OpenCodeID), ShouldEqual, engine.ActionNoop)
			})
		})
	})
}

func TestDigestBlockDeletedRestores(t *testing.T) {
	Convey("Given a generated block deleted by hand", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")
		ignoreInRepo(t, repo, "AGENTS.md")

		write(t, repoFile(repo), "# repo rules\n")
		writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")
		f.sync(t)

		So(os.Remove(repoFile(repo)), ShouldBeNil)
		write(t, repoFile(repo), "# repo rules\n")

		report := f.sync(t)

		Convey("When it is restored", func() {
			Convey("Then the block comes back", func() {
				So(digestResults(report, engine.DigestRefreshed), ShouldNotBeEmpty)
				So(strings.HasPrefix(read(t, repoFile(repo)), digest.BeginPrefix), ShouldBeTrue)
			})
		})
	})
}

func TestDigestPrune(t *testing.T) {
	Convey("Given a render record for a repo that disappears", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProjectWith(t, engine.ProjectOptions{AllowSecrets: true}, "AGENTS.md")

		write(t, repoFile(repo), "# repo rules\n")
		writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")
		f.sync(t)

		st, err := state.Load(f.vault.StatePath())
		So(err, ShouldBeNil)
		So(st.Renders, ShouldContainKey, repoFile(repo))

		So(os.RemoveAll(filepath.Join(repo, ".git")), ShouldBeNil)
		So(os.Remove(repoFile(repo)), ShouldBeNil)

		report := f.sync(t)

		st, err = state.Load(f.vault.StatePath())
		So(err, ShouldBeNil)

		Convey("When the identity is gone", func() {
			Convey("Then the record and drift are pruned", func() {
				So(digestResults(report, engine.DigestPruned), ShouldNotBeEmpty)
				So(st.Renders, ShouldNotContainKey, repoFile(repo))
				So(st.Drift, ShouldNotContainKey, repoFile(repo))
			})
		})
	})
}

func TestDigestDryRunPreview(t *testing.T) {
	Convey("Given a dry run with memory to digest", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProjectWith(t, engine.ProjectOptions{AllowSecrets: true}, "AGENTS.md")

		writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")

		report := f.run(t, engine.SyncOptions{DryRun: true})

		st, err := state.Load(f.vault.StatePath())
		So(err, ShouldBeNil)

		Convey("When it previews", func() {
			_, fileErr := os.Stat(repoFile(repo))

			Convey("Then nothing is written or recorded", func() {
				So(digestResults(report, engine.DigestWouldCreate), ShouldNotBeEmpty)
				So(errors.Is(fileErr, fs.ErrNotExist), ShouldBeTrue)
				So(st.Renders, ShouldNotContainKey, repoFile(repo))
			})
		})
	})
}

func TestDigestFenceGate(t *testing.T) {
	Convey("Given a git-tracked project file", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")

		write(t, repoFile(repo), "# repo rules\n")
		writeMemoryCanon(t, f, repo, "MEMORY.md", "---\ndescription: hook\n---\nbody\n")

		report := f.sync(t)

		Convey("When it becomes ignored and then tracked again", func() {
			So(digestResults(report, engine.DigestSkipped), ShouldNotBeEmpty)
			So(read(t, repoFile(repo)), ShouldNotContainSubstring, digest.BeginPrefix)
			So(strings.Join(report.Warnings, " "), ShouldContainSubstring, "is not gitignored")

			ignoreInRepo(t, repo, "AGENTS.md")

			report = f.sync(t)

			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			So(digestResults(report, engine.DigestRefreshed), ShouldNotBeEmpty)
			So(strings.HasPrefix(read(t, repoFile(repo)), digest.BeginPrefix), ShouldBeTrue)
			So(hasIssue(issues, engine.SeverityError, "lives in a git-tracked file"), ShouldBeFalse)

			So(os.Remove(filepath.Join(repo, ".gitignore")), ShouldBeNil)

			issues, err = f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then a fence in a tracked file is a doctor error", func() {
				So(hasIssue(issues, engine.SeverityError, "lives in a git-tracked file"), ShouldBeTrue)
			})
		})
	})
}

func TestProjectsWatchPathsAndKindFilter(t *testing.T) {
	Convey("Given an enabled project", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")

		Convey("When watch paths and a kind filter are used", func() {
			paths, err := f.engine.WatchPaths(t.Context())
			So(err, ShouldBeNil)

			write(t, repoFile(repo), "# repo rules\n")

			report := f.run(t, engine.SyncOptions{Kinds: []kind.ID{kind.Projects}})

			Convey("Then only projects is synced", func() {
				So(paths, ShouldContain, f.vault.ProjectsDir())
				So(paths, ShouldContain, repoFile(repo))

				So(report.Kind(kind.Projects), ShouldNotBeNil)
				So(report.Kind(kind.Memory), ShouldBeNil)
				So(read(t, vaultProject(f, repo, "AGENTS.md")), ShouldEqual, "# repo rules\n")
			})
		})
	})
}

func TestProjectsRestore(t *testing.T) {
	Convey("Given canon versions of a project file", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		repo := newRepo(t)
		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md")

		write(t, repoFile(repo), "# v1\n")
		f.sync(t)

		write(t, vaultProject(f, repo, "AGENTS.md"), "# v2\n")
		f.sync(t)

		_, err := f.engine.Restore(t.Context(), kind.Projects, -2)
		So(err, ShouldBeNil)

		Convey("When the previous version is restored", func() {
			Convey("Then the canon and the repo roll back", func() {
				So(read(t, vaultProject(f, repo, "AGENTS.md")), ShouldEqual, "# v1\n")
				So(read(t, repoFile(repo)), ShouldEqual, "# v1\n")
			})
		})
	})
}
