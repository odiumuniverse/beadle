package engine_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/kind"
)

func TestProjectCanonArbitraryFiles(t *testing.T) {
	Convey("Given a project with arbitrary canon files", t, func() {
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
		So(os.Symlink(canon("AGENTS.md"), canon("linked.md")), ShouldBeNil)

		write(t, repoFile(repo), "# rules\n")

		report := f.sync(t)
		So(report.Kind(kind.Projects).VaultChanged, ShouldBeFalse)

		info, err := os.Lstat(canon("linked.md"))
		So(err, ShouldBeNil)

		Convey("When an unrelated canon file is removed and the repo changes", func() {
			So(os.Remove(canon("deep/notes.json")), ShouldBeNil)
			write(t, repoFile(repo), "# rules v2\n")

			report := f.sync(t)

			_, notesErr := os.Stat(canon("deep/notes.json"))

			Convey("Then foreign canon files survive and the repo updates", func() {
				So(report.Kind(kind.Projects).VaultChanged, ShouldBeTrue)
				So(errors.Is(notesErr, fs.ErrNotExist), ShouldBeTrue)

				_, hiddenErr := os.Stat(canon("deep/nested/.hidden"))
				So(hiddenErr, ShouldBeNil)

				_, policyErr := os.Stat(canon("policy.json"))
				So(policyErr, ShouldBeNil)

				So(read(t, repoFile(repo)), ShouldEqual, "# rules v2\n")
			})
		})

		Convey("Then nested, dotfiles and symlinks are preserved", func() {
			_, hiddenErr := os.Stat(canon("deep/nested/.hidden"))
			So(hiddenErr, ShouldBeNil)

			_, notesErr := os.Stat(canon("deep/notes.json"))
			So(notesErr, ShouldBeNil)

			_, policyErr := os.Stat(canon("policy.json"))
			So(policyErr, ShouldBeNil)

			So(info.Mode()&os.ModeSymlink, ShouldNotEqual, os.FileMode(0))
		})
	})
}

func TestProjectCanonLoaderRejectsTraversal(t *testing.T) {
	Convey("Given a file outside the project canon directory", t, func() {
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

		Convey("When the next sync runs", func() {
			report := f.sync(t)

			Convey("Then the loader never touches sibling paths", func() {
				So(report.Kind(kind.Projects).VaultChanged, ShouldBeFalse)
				So(read(t, outside), ShouldEqual, "secret\n")
			})
		})
	})
}

func TestProjectForgetKeepsOtherEntries(t *testing.T) {
	Convey("Given a project with two enabled files", t, func() {
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

		_, agentsErr := os.Stat(canon("AGENTS.md"))
		_, mcpErr := os.Stat(canon(".mcp.json"))

		Convey("When one file is forgotten", func() {
			_, err := f.engine.ProjectForget(t.Context(), ".mcp.json")
			So(err, ShouldBeNil)

			_, goneErr := os.Stat(canon(".mcp.json"))
			_, keptErr := os.Stat(canon("AGENTS.md"))
			_, repoErr := os.Stat(repoFile(repo))

			Convey("Then only that entry is removed", func() {
				So(agentsErr, ShouldBeNil)
				So(mcpErr, ShouldBeNil)

				So(errors.Is(goneErr, fs.ErrNotExist), ShouldBeTrue)
				So(keptErr, ShouldBeNil)
				So(repoErr, ShouldBeNil)
			})
		})
	})
}
