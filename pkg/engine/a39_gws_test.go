package engine_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// a39SkillDoc builds a canon SKILL.md carrying the given frontmatter body.
func a39SkillDoc(front string) string {
	return "---\n" + front + "---\n\n# skill\n"
}

func TestDSHSkillsRoundTrip(t *testing.T) {
	Convey("Given an enabled DSH adapter", t, func() {
		dsh := a38DSHHome(t)

		f := newFixture(t)
		f.enableAgent(t, agent.DSHID)

		canon := a39SkillDoc("name: alpha\ndescription: first\nwhenToUse: when alpha\n")
		write(t, f.vaultSkill("alpha"), canon)

		Convey("When the canon syncs", func() {
			report := f.sync(t)
			path := filepath.Join(dsh, "skills", "alpha", "SKILL.md")

			Convey("Then the skill lands byte-for-byte in $DSH_HOME/skills", func() {
				So(report.Errors(), ShouldBeEmpty)
				So(read(t, path), ShouldEqual, canon)

				Convey("And a repeat sync is a no-op", func() {
					second := f.sync(t)

					So(read(t, path), ShouldEqual, canon)

					result, ok := second.Kind(kind.Skills).Agent(agent.DSHID)
					So(ok, ShouldBeTrue)
					So(result.Action, ShouldEqual, engine.ActionNoop)
				})

				Convey("When the DSH file is edited", func() {
					edited := a39SkillDoc("name: alpha\ndescription: harness edit\n")
					write(t, path, edited)

					f.sync(t)

					Convey("Then the edit is pulled into the canon", func() {
						So(read(t, f.vaultSkill("alpha")), ShouldEqual, edited)

						Convey("When the canon drops the skill", func() {
							So(os.RemoveAll(filepath.Dir(f.vaultSkill("alpha"))), ShouldBeNil)

							f.sync(t)

							Convey("Then the DSH copy is removed", func() {
								_, err := os.Stat(filepath.Dir(path))
								So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)
							})
						})
					})
				})
			})
		})
	})
}

func TestDSHSkillsPull(t *testing.T) {
	Convey("Given a DSH skill with no canon copy", t, func() {
		dsh := a38DSHHome(t)

		f := newFixture(t)
		f.enableAgent(t, agent.DSHID)

		path := filepath.Join(dsh, "skills", "alpha", "SKILL.md")
		doc := a39SkillDoc("name: alpha\ndescription: authored in DSH\n")

		write(t, path, doc)

		Convey("When the sync runs", func() {
			report := f.sync(t)

			Convey("Then the skill lands in the canon and the file is not rewritten", func() {
				So(report.Errors(), ShouldBeEmpty)
				So(read(t, f.vaultSkill("alpha")), ShouldEqual, doc)
				So(read(t, path), ShouldEqual, doc)
			})
		})
	})
}

func TestDSHSkillsWriteRefusal(t *testing.T) {
	Convey("Given a canon skill DSH cannot read", t, func() {
		a38DSHHome(t)

		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.DSHID)

		cases := []struct {
			name  string
			front string
			want  string
		}{
			{"a non-kebab frontmatter name", "name: UPPER\ndescription: d\n", "kebab-case"},
			{"a missing description", "name: alpha\n", "requires a description"},
			{"a non-boolean invocation", "name: alpha\ndescription: d\ndisable-model-invocation: maybe\n", "must be a boolean"},
		}

		for _, tc := range cases {
			Convey("When the canon declares "+tc.name, func() {
				doc := a39SkillDoc(tc.front)
				write(t, f.vaultSkill("alpha"), doc)

				report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
				So(err, ShouldBeNil)

				Convey("Then the DSH write is refused with a clear error", func() {
					So(strings.Join(report.Errors(), "\n"), ShouldContainSubstring, tc.want)

					result, ok := report.Kind(kind.Skills).Agent(agent.DSHID)
					So(ok, ShouldBeTrue)
					So(result.Action, ShouldEqual, engine.ActionError)

					So(read(t, f.vaultSkill("alpha")), ShouldEqual, doc)
				})
			})
		}
	})
}

func TestDSHSkillsSharedShadow(t *testing.T) {
	Convey("Given the same skill in the shared hub and the canon", t, func() {
		dsh := a38DSHHome(t)

		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.DSHID)

		shared := filepath.Join(f.home, ".agents", "skills", "alpha", "SKILL.md")
		sharedDoc := a39SkillDoc("name: alpha\ndescription: shared copy\n")
		write(t, shared, sharedDoc)

		canon := a39SkillDoc("name: alpha\ndescription: canon copy\n")
		write(t, f.vaultSkill("alpha"), canon)

		Convey("When the sync runs", func() {
			report := f.sync(t)
			path := filepath.Join(dsh, "skills", "alpha", "SKILL.md")

			Convey("Then DSH gets its own copy and the shared copy stays", func() {
				So(report.Errors(), ShouldBeEmpty)
				So(read(t, path), ShouldEqual, canon)
				So(read(t, shared), ShouldEqual, sharedDoc)
			})

			Convey("And the shadowed shared copy is not reported as a duplicate for DSH", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)

				for _, issue := range issues {
					if issue.Agent != agent.DSHID || issue.Severity != engine.SeverityWarn {
						continue
					}

					So(issue.Message, ShouldNotContainSubstring, "differs between")
				}
			})
		})
	})
}

func TestDSHSkillsFlatFanIn(t *testing.T) {
	Convey("Given a flat skill in $DSH_HOME/skills", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")
		withoutDSHHome(t)

		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.DSHID)

		flat := filepath.Join(f.home, ".dsh", "skills", "alpha.md")
		doc := a39SkillDoc("name: alpha\ndescription: flat\n")
		write(t, flat, doc)

		Convey("When the sync runs", func() {
			report := f.sync(t)

			Convey("Then the canon holds the flat skill and the file stays", func() {
				So(report.Errors(), ShouldBeEmpty)
				So(read(t, f.vaultSkill("alpha")), ShouldEqual, doc)
				So(read(t, flat), ShouldEqual, doc)

				_, err := os.Stat(filepath.Join(f.home, ".dsh", "skills", "alpha"))
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)
			})

			Convey("And the doctor reports the flat copy", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityInfo, "1 flat skill(s) in ~/.dsh/skills are read by the host: alpha"), ShouldBeTrue)
			})

			Convey("And a canon change is delivered as a directory", func() {
				changed := a39SkillDoc("name: alpha\ndescription: changed\n")
				write(t, f.vaultSkill("alpha"), changed)

				f.sync(t)

				So(read(t, filepath.Join(f.home, ".dsh", "skills", "alpha", "SKILL.md")), ShouldEqual, changed)
				So(read(t, flat), ShouldEqual, doc)
			})
		})
	})
}

func TestDSHSkillsCustomAgentsHome(t *testing.T) {
	Convey("Given DSH_AGENTS_HOME pointing at a custom shared root", t, func() {
		dsh := a38DSHHome(t)

		agents := filepath.Join(t.TempDir(), "shared-agents")
		t.Setenv("DSH_AGENTS_HOME", agents)

		f := newFixture(t)
		f.emptyConfigs(t)
		f.enableAgent(t, agent.DSHID)

		// The custom hub carries a copy identical to the canon: a foreign
		// root would release the DSH write, a shared root must not.
		canon := a39SkillDoc("name: alpha\ndescription: canon copy\n")
		hub := filepath.Join(agents, "skills", "alpha", "SKILL.md")
		write(t, hub, canon)

		write(t, f.vaultSkill("alpha"), canon)

		Convey("When the sync runs", func() {
			report := f.sync(t)
			path := filepath.Join(dsh, "skills", "alpha", "SKILL.md")

			Convey("Then DSH writes its own rank-400 copy and the hub stays", func() {
				So(report.Errors(), ShouldBeEmpty)
				So(read(t, path), ShouldEqual, canon)
				So(read(t, hub), ShouldEqual, canon)

				kr := report.Kind(kind.Skills)
				So(kr, ShouldNotBeNil)
				So(strings.Join(kr.Warnings, "\n"), ShouldNotContainSubstring, "also delivered by")
			})

			Convey("And the doctor reports the resolved shared root", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityInfo, "shared "+filepath.Join(agents, "skills")+" (read-only)"), ShouldBeTrue)
			})
		})
	})
}
