package engine_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// a38DSHHome prepares a DSH_HOME fixture before the engine is built and
// enables the adapter.
func a38DSHHome(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "dsh")

	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir DSH_HOME: %v", err)
	}

	t.Setenv("DSH_HOME", dir)

	return dir
}

func TestDSHRulesRoundTrip(t *testing.T) {
	Convey("Given an enabled DSH adapter", t, func() {
		dsh := a38DSHHome(t)

		f := newFixture(t)
		f.enableAgent(t, agent.DSHID)

		write(t, f.vault.RulesPath(), "# vault rules\n")

		Convey("When the canon syncs", func() {
			report := f.sync(t)

			Convey("Then the rules land in $DSH_HOME/AGENTS.md", func() {
				So(report.Errors(), ShouldBeEmpty)
				So(read(t, filepath.Join(dsh, "AGENTS.md")), ShouldEqual, "# vault rules\n")

				Convey("When the file is edited", func() {
					write(t, filepath.Join(dsh, "AGENTS.md"), "# harness rules\n")

					f.sync(t)

					Convey("Then the edit is pulled into the canon", func() {
						So(read(t, f.vault.RulesPath()), ShouldEqual, "# harness rules\n")

						Convey("When the file is deleted", func() {
							So(os.Remove(filepath.Join(dsh, "AGENTS.md")), ShouldBeNil)

							f.sync(t)

							Convey("Then the singleton canon survives and the file comes back", func() {
								So(read(t, f.vault.RulesPath()), ShouldEqual, "# harness rules\n")
								So(read(t, filepath.Join(dsh, "AGENTS.md")), ShouldEqual, "# harness rules\n")

								Convey("When the canon is emptied", func() {
									So(os.Remove(f.vault.RulesPath()), ShouldBeNil)

									f.sync(t)

									Convey("Then the singleton host file survives too", func() {
										So(read(t, filepath.Join(dsh, "AGENTS.md")), ShouldEqual, "# harness rules\n")
									})
								})
							})
						})
					})
				})
			})
		})
	})
}

func TestDSHChainPull(t *testing.T) {
	Convey("Given a checkout with a nested instruction chain", t, func() {
		a38DSHHome(t)

		f := newFixture(t)
		f.enableAgent(t, agent.DSHID)

		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.md"), "# root agents\n")
		write(t, filepath.Join(repo, "CLAUDE.md"), "# root agents\n") // identical sibling
		write(t, filepath.Join(repo, "sub", "CLAUDE.md"), "# sub claude\n")

		f.useCwd(t, filepath.Join(repo, "sub"))
		f.enableProject(t, "AGENTS.md", "CLAUDE.md", "sub/CLAUDE.md")

		Convey("When the project syncs", func() {
			report := f.sync(t)

			Convey("Then unique contents land in the canon once", func() {
				So(report.Errors(), ShouldBeEmpty)
				So(read(t, projectCanon(f, repo, "AGENTS.md")), ShouldEqual, "# root agents\n")
				So(read(t, projectCanon(f, repo, "sub/CLAUDE.md")), ShouldEqual, "# sub claude\n")

				_, siblingErr := os.Stat(projectCanon(f, repo, "CLAUDE.md"))
				So(errors.Is(siblingErr, fs.ErrNotExist), ShouldBeTrue)

				Convey("Then the project status resolves root-relative rels against the root", func() {
					status, err := f.engine.ProjectStatus(t.Context())
					So(err, ShouldBeNil)

					present := map[string]bool{}

					for _, file := range status.Files {
						present[file.Rel] = file.Present
					}

					So(present["CLAUDE.md"], ShouldBeTrue)
					So(present["sub/CLAUDE.md"], ShouldBeTrue)

					// AGENTS.md is contested: OpenCode reads <cwd>/AGENTS.md,
					// so the rel resolves at cwd, where the file is missing.
					So(present["AGENTS.md"], ShouldBeFalse)
				})

				Convey("Then doctor resolves the chain rel against the root for gitignore checks", func() {
					write(t, filepath.Join(repo, ".gitignore"), "/CLAUDE.md\n")
					f.enableProject(t, "CLAUDE.md")

					issues, err := f.engine.Doctor(t.Context())
					So(err, ShouldBeNil)
					So(hasIssue(issues, engine.SeverityInfo, "project file CLAUDE.md: enabled (publishable: yes)"), ShouldBeTrue)
				})

				Convey("When a chain file is deleted", func() {
					So(os.Remove(filepath.Join(repo, "sub", "CLAUDE.md")), ShouldBeNil)

					f.sync(t)

					Convey("Then the projects policy keeps the canon element", func() {
						So(read(t, projectCanon(f, repo, "sub/CLAUDE.md")), ShouldEqual, "# sub claude\n")
						So(read(t, projectCanon(f, repo, "AGENTS.md")), ShouldEqual, "# root agents\n")
					})
				})
			})
		})
	})
}

func TestDSHChainDisabledPredecessorDoesNotShadow(t *testing.T) {
	Convey("Given identical chain siblings where only the second is enabled", t, func() {
		a38DSHHome(t)

		f := newFixture(t)
		f.enableAgent(t, agent.DSHID)

		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.md"), "# shared agents\n")
		write(t, filepath.Join(repo, "CLAUDE.md"), "# shared agents\n")

		So(os.MkdirAll(filepath.Join(repo, "sub"), 0o750), ShouldBeNil)

		f.useCwd(t, filepath.Join(repo, "sub"))
		f.enableProject(t, "CLAUDE.md")

		Convey("When the project syncs", func() {
			report := f.sync(t)

			Convey("Then the enabled file lands in the canon despite the disabled twin", func() {
				So(report.Errors(), ShouldBeEmpty)
				So(read(t, projectCanon(f, repo, "CLAUDE.md")), ShouldEqual, "# shared agents\n")

				_, agErr := os.Stat(projectCanon(f, repo, "AGENTS.md"))
				So(errors.Is(agErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

// a38CanonItems lists the canon elements of a project, policy excluded.
func a38CanonItems(t *testing.T, f *fixture, repo string) []string {
	t.Helper()

	dir := filepath.Join(f.vault.ProjectsDir(), repoID(repo))

	var rels []string

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Name() == "policy.json" {
			return nil //nolint:nilerr // a missing canon dir simply has no items
		}

		rel, relErr := filepath.Rel(dir, path)
		if relErr == nil {
			rels = append(rels, filepath.ToSlash(rel))
		}

		return nil
	})
	So(err, ShouldBeNil)

	slices.Sort(rels)

	return rels
}

func TestDSHChainAliasPredecessorCollapses(t *testing.T) {
	Convey("Given identical siblings where another agent owns the first rel", t, func() {
		a38DSHHome(t)

		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.DSHID)

		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.md"), "# shared agents\n")
		write(t, filepath.Join(repo, "CLAUDE.md"), "# shared agents\n")

		f.useRepo(t, repo)
		f.enableProject(t, "AGENTS.md", "CLAUDE.md")

		Convey("When the project syncs", func() {
			report := f.sync(t)

			Convey("Then the canon holds one element for the shared content", func() {
				So(report.Errors(), ShouldBeEmpty)
				So(a38CanonItems(t, f, repo), ShouldResemble, []string{"AGENTS.md"})
				So(read(t, projectCanon(f, repo, "AGENTS.md")), ShouldEqual, "# shared agents\n")
			})
		})
	})

	Convey("Given a nested cwd where the cwd file shares the content", t, func() {
		a38DSHHome(t)

		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.DSHID)

		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.md"), "# shared agents\n")
		write(t, filepath.Join(repo, "CLAUDE.md"), "# shared agents\n")
		write(t, filepath.Join(repo, "sub", "AGENTS.md"), "# shared agents\n")

		f.useCwd(t, filepath.Join(repo, "sub"))
		f.enableProject(t, "AGENTS.md", "CLAUDE.md", "sub/AGENTS.md")

		Convey("When the project syncs", func() {
			report := f.sync(t)

			Convey("Then the canon still holds one element", func() {
				So(report.Errors(), ShouldBeEmpty)
				So(a38CanonItems(t, f, repo), ShouldResemble, []string{"AGENTS.md"})
			})
		})
	})
}

func TestDSHChainCollision(t *testing.T) {
	Convey("Given DSH and a cwd-relative surface claiming the same rel", t, func() {
		a38DSHHome(t)

		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.DSHID)

		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.md"), "# root agents\n")
		write(t, filepath.Join(repo, "sub", "AGENTS.md"), "# cwd agents\n")

		f.useCwd(t, filepath.Join(repo, "sub"))
		f.enableProject(t, "AGENTS.md")

		Convey("When the project syncs", func() {
			report := f.sync(t)

			Convey("Then the cwd file keeps the key and the chain file is a visible conflict", func() {
				So(report.Errors(), ShouldBeEmpty)
				So(read(t, projectCanon(f, repo, "AGENTS.md")), ShouldEqual, "# cwd agents\n")

				conflicts := report.ConflictsOf(kind.Projects)
				So(conflicts, ShouldHaveLength, 1)
				So(conflicts[0].Key, ShouldEqual, repoID(repo)+"/AGENTS.md")
				So(conflicts[0].Agent, ShouldEqual, agent.DSHID)
				So(conflicts[0].Reason, ShouldEqual, state.ReasonAdded)

				Convey("Then a second sync keeps the conflict instead of flip-flopping", func() {
					again := f.sync(t)
					So(again.ConflictsOf(kind.Projects), ShouldHaveLength, 1)
					So(again.ConflictsOf(kind.Projects)[0].Reason, ShouldEqual, state.ReasonAdded)
					So(read(t, projectCanon(f, repo, "AGENTS.md")), ShouldEqual, "# cwd agents\n")
				})
			})
		})
	})

	Convey("Given identical contents at the root and cwd", t, func() {
		a38DSHHome(t)

		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.DSHID)

		repo := newRepo(t)
		write(t, filepath.Join(repo, "AGENTS.md"), "# same agents\n")
		write(t, filepath.Join(repo, "sub", "AGENTS.md"), "# same agents\n")

		f.useCwd(t, filepath.Join(repo, "sub"))
		f.enableProject(t, "AGENTS.md")

		Convey("Then one element renders and no conflict appears", func() {
			report := f.sync(t)

			So(report.Errors(), ShouldBeEmpty)
			So(report.ConflictsOf(kind.Projects), ShouldBeEmpty)
			So(read(t, projectCanon(f, repo, "AGENTS.md")), ShouldEqual, "# same agents\n")
		})
	})
}
