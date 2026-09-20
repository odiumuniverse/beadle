package project_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/memory"
	"github.com/odiumuniverse/beadle/pkg/project"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()

	all := append([]string{"-C", dir}, args...)

	out, err := exec.CommandContext(t.Context(), "git", all...).CombinedOutput() //nolint:gosec // G204: fixed git subcommands in tests
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func newGitRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	git(t, dir, "init", "-q")

	return dir
}

func TestIdentityCanonicalRemote(t *testing.T) {
	Convey("Given a table of remotes and their canonical identity", t, func() {
		cases := map[string]string{
			"git@github.com:Org/Repo.git":            "github.com/org/repo",
			"https://github.com/Org/Repo.git":        "github.com/org/repo",
			"ssh://git@github.com/Org/Repo":          "github.com/org/repo",
			"https://github.com/Org/Repo/":           "github.com/org/repo",
			"ssh://git@github.com:2222/Org/Repo.git": "github.com/2222/org/repo",
			"git@gitlab.example.com:group/sub/repo":  "gitlab.example.com/group/sub/repo",
		}

		for remote, want := range cases {
			Convey("When the remote is "+remote, func() {
				dir := newGitRepo(t)
				git(t, dir, "remote", "add", "origin", remote)

				Convey("Then the canonical identity matches", func() {
					So(project.Resolve(dir).Remote, ShouldEqual, want)
				})
			})
		}
	})
}

func TestIdentityClonesConverge(t *testing.T) {
	Convey("Given two clones of one bare origin", t, func() {
		origin := t.TempDir()
		git(t, origin, "init", "-q", "--bare")

		cloneA := filepath.Join(t.TempDir(), "alpha")
		cloneB := filepath.Join(t.TempDir(), "beta")

		git(t, t.TempDir(), "clone", "-q", origin, cloneA)
		git(t, t.TempDir(), "clone", "-q", origin, cloneB)
		git(t, cloneA, "remote", "set-url", "origin", "git@"+origin)
		git(t, cloneB, "remote", "set-url", "origin", origin)

		deep := filepath.Join(cloneA, "nested", "deeper")
		So(os.MkdirAll(deep, 0o750), ShouldBeNil)

		idA := project.Resolve(cloneA)
		idDeep := project.Resolve(deep)
		idB := project.Resolve(cloneB)

		Convey("When identities are resolved", func() {
			Convey("Then clones and subdirectories share one project", func() {
				So(idA.Slugs, ShouldBeFalse)
				So(idDeep.ID, ShouldEqual, idA.ID)
				So(idB.ID, ShouldEqual, idA.ID)
				So(idDeep.Root, ShouldEqual, idA.Root)
			})
		})
	})
}

func TestIdentityWorktreeSharesTheProject(t *testing.T) {
	Convey("Given a repository with a linked worktree", t, func() {
		repo := newGitRepo(t)
		git(t, repo, "config", "user.email", "test@example.com")
		git(t, repo, "config", "user.name", "Test")
		So(os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o600), ShouldBeNil)
		git(t, repo, "add", "README.md")
		git(t, repo, "commit", "-q", "-m", "init")

		worktree := filepath.Join(t.TempDir(), "wt")
		git(t, repo, "worktree", "add", "-q", worktree, "HEAD")

		Convey("When both paths are resolved", func() {
			Convey("Then the worktree shares the project", func() {
				So(project.Resolve(worktree).ID, ShouldEqual, project.Resolve(repo).ID)
			})
		})
	})
}

func TestIdentityNonGitUsesPathSlug(t *testing.T) {
	Convey("Given a directory that is not a git repository", t, func() {
		dir := t.TempDir()
		identity := project.Resolve(dir)

		Convey("When it is resolved", func() {
			Convey("Then it falls back to a path slug", func() {
				So(identity.Slugs, ShouldBeTrue)
				So(identity.ID, ShouldEqual, memory.Slug(dir))
				So(identity.Root, ShouldBeEmpty)
				So(identity.Remote, ShouldBeEmpty)
			})
		})
	})
}

func TestIdentityHomeGuard(t *testing.T) {
	Convey("Given a git repository rooted at $HOME", t, func() {
		home := newGitRepo(t)
		t.Setenv("HOME", home)

		identity := project.Resolve(home)

		Convey("When it is resolved", func() {
			Convey("Then it is not captured as a project", func() {
				So(identity.Slugs, ShouldBeTrue)
				So(identity.ID, ShouldEqual, memory.Slug(home))
			})
		})
	})
}

func TestIdentityNoRemoteFallsBackToCommonDir(t *testing.T) {
	Convey("Given a git repository without a remote", t, func() {
		repo := newGitRepo(t)
		git(t, repo, "config", "user.email", "test@example.com")
		git(t, repo, "config", "user.name", "Test")
		So(os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o600), ShouldBeNil)
		git(t, repo, "add", "README.md")
		git(t, repo, "commit", "-q", "-m", "init")

		identity := project.Resolve(repo)

		Convey("When it is resolved", func() {
			Convey("Then the id comes from the common dir", func() {
				So(identity.Slugs, ShouldBeFalse)
				So(identity.Remote, ShouldBeEmpty)

				matched, err := regexp.MatchString(`^[a-z0-9-]+-[0-9a-f]{8}$`, identity.ID)
				So(err, ShouldBeNil)
				So(matched, ShouldBeTrue)
			})
		})
	})
}
