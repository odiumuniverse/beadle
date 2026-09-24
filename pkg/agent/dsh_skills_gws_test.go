package agent_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// dshSkillDoc builds a canon SKILL.md carrying the given frontmatter body.
func dshSkillDoc(front string) string {
	return "---\n" + front + "---\n\n# skill\n"
}

// dshSkillsSurface builds a DSH adapter whose skills surface lives in a fresh
// DSH_HOME and returns the surface with the home it writes into.
func dshSkillsSurface(t *testing.T) (agent.Surface, string) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("DSH_HOME", home)

	return surfaceOf(t, agent.DSH(home, t.TempDir()), kind.Skills), home
}

func TestDSHSkillsWrite(t *testing.T) {
	Convey("Given a DSH skills surface", t, func() {
		surface, home := dshSkillsSurface(t)
		path := filepath.Join(home, "skills", "alpha", "SKILL.md")
		canon := dshSkillDoc("name: alpha\ndescription: first\nwhenToUse: when alpha\n")

		Convey("When a canon skill is written", func() {
			err := surface.Write(t.Context(), kind.Items{"alpha/SKILL.md": []byte(canon)})

			Convey("Then the file carries the canon bytes", func() {
				So(err, ShouldBeNil)
				So(readFile(t, path), ShouldEqual, canon)

				Convey("And the read-back is the canon tree", func() {
					snap, readErr := surface.Read(t.Context())
					So(readErr, ShouldBeNil)
					So(snap.Items["alpha/SKILL.md"], ShouldResemble, []byte(canon))

					Convey("And a repeat write keeps the file byte-stable", func() {
						before := readFile(t, path)

						err := surface.Write(t.Context(), kind.Items{"alpha/SKILL.md": []byte(canon)})
						So(err, ShouldBeNil)
						So(readFile(t, path), ShouldEqual, before)
					})
				})
			})
		})

		Convey("When the canon drops the skill", func() {
			So(surface.Write(t.Context(), kind.Items{"alpha/SKILL.md": []byte(canon)}), ShouldBeNil)

			err := surface.Write(t.Context(), kind.Items{})

			Convey("Then the skill directory is removed", func() {
				So(err, ShouldBeNil)

				_, statErr := os.Stat(filepath.Dir(path))
				So(os.IsNotExist(statErr), ShouldBeTrue)
			})
		})
	})
}

func TestDSHSkillsNameRefusal(t *testing.T) {
	Convey("Given a DSH skills surface", t, func() {
		surface, home := dshSkillsSurface(t)

		for _, name := range []string{"Bad_Name", "UPPER"} {
			Convey("When a skill named "+name+" is written", func() {
				item := kind.Items{name + "/SKILL.md": []byte(dshSkillDoc("name: " + name + "\ndescription: d\n"))}

				err := surface.Write(t.Context(), item)

				Convey("Then the directory name is refused with the kebab-case rule", func() {
					So(err, ShouldBeError)
					So(err.Error(), ShouldContainSubstring, "skill name \""+name+"\" is not kebab-case")
				})
			})
		}

		Convey("When an invalid directory name carries no frontmatter at all", func() {
			// Only the directory-name check can refuse this one: the
			// frontmatter checks never see a name to compare against.
			item := kind.Items{"Bad_Name/SKILL.md": []byte("# no frontmatter\n")}

			err := surface.Write(t.Context(), item)

			Convey("Then the directory name alone is refused", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "skill name \"Bad_Name\" is not kebab-case")
			})
		})

		Convey("When a kebab-case name is written", func() {
			item := kind.Items{"kebab-case-1/SKILL.md": []byte(dshSkillDoc("name: kebab-case-1\ndescription: d\n"))}

			err := surface.Write(t.Context(), item)

			Convey("Then it is accepted", func() {
				So(err, ShouldBeNil)
				So(readFile(t, filepath.Join(home, "skills", "kebab-case-1", "SKILL.md")),
					ShouldContainSubstring, "name: kebab-case-1")
			})
		})
	})
}

func TestDSHSkillsFrontmatterMapping(t *testing.T) {
	Convey("Given a DSH skills surface", t, func() {
		surface, home := dshSkillsSurface(t)
		path := filepath.Join(home, "skills", "alpha", "SKILL.md")

		cases := []struct {
			name  string
			front string
			want  string // refusal substring; empty means the write is accepted
		}{
			{name: "name and description", front: "name: alpha\ndescription: d\n"},
			{name: "whenToUse passes through", front: "name: alpha\ndescription: d\nwhenToUse: use it for alpha\n"},
			{name: "metadata passes through", front: "name: alpha\ndescription: d\nmetadata:\n  owner: team\n"},
			{name: "disable-model-invocation passes through", front: "name: alpha\ndescription: d\ndisable-model-invocation: true\n"},
			{name: "user-invocable passes through", front: "name: alpha\ndescription: d\nuser-invocable: false\n"},
			{name: "the boolean grammar is accepted", front: "name: alpha\ndescription: d\ndisable-model-invocation: 'yes'\nuser-invocable: 'off'\n"},
			{name: "a missing name", front: "description: d\n", want: "requires a name"},
			{name: "a non-kebab frontmatter name", front: "name: UPPER\ndescription: d\n", want: "not kebab-case"},
			{name: "a mismatched name", front: "name: beta\ndescription: d\n", want: "does not match"},
			{name: "a missing description", front: "name: alpha\n", want: "requires a description"},
			{name: "a non-boolean invocation", front: "name: alpha\ndescription: d\ndisable-model-invocation: maybe\n", want: "must be a boolean"},
			{name: "a legacy invocation key", front: "name: alpha\ndescription: d\nuserInvocable: false\n", want: "is rejected by DSH"},
			{name: "the legacy modelInvocable key", front: "name: alpha\ndescription: d\nmodelInvocable: true\n", want: "with the inverted value"},
		}

		for _, tc := range cases {
			Convey("When the canon has "+tc.name, func() {
				canon := dshSkillDoc(tc.front)

				err := surface.Write(t.Context(), kind.Items{"alpha/SKILL.md": []byte(canon)})

				if tc.want == "" {
					Convey("Then the DSH file carries the frontmatter byte-for-byte", func() {
						So(err, ShouldBeNil)
						So(readFile(t, path), ShouldEqual, canon)
					})

					return
				}

				Convey("Then the write is refused", func() {
					So(err, ShouldBeError)
					So(err.Error(), ShouldContainSubstring, tc.want)
				})
			})
		}

		Convey("When the closing fence carries trailing whitespace", func() {
			err := surface.Write(t.Context(), kind.Items{"alpha/SKILL.md": []byte("---\nname: alpha\ndescription: d\n--- \n\n# skill\n")})

			Convey("Then the write is refused", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "fence must be exactly")
			})
		})
	})
}

func TestDSHSkillsSurfaceTraits(t *testing.T) {
	Convey("Given a DSH adapter", t, func() {
		surface, home := dshSkillsSurface(t)

		Convey("Then the skills surface writes and reads the shared hub read-only", func() {
			So(surface.Traits().DefaultMode, ShouldEqual, config.ModeSync)
			So(surface.Traits().Creatable, ShouldBeTrue)

			area, ok := surface.(agent.SkillReadArea)
			So(ok, ShouldBeTrue)
			So(area.ReadDirs(), ShouldResemble, []string{
				filepath.Join(home, "skills"),
				filepath.Join(home, ".agents", "skills"),
			})

			caps, ok := surface.(agent.SkillCapsSurface)
			So(ok, ShouldBeTrue)
			So(caps.SkillCaps().Shadowing, ShouldBeTrue)
			So(caps.SkillCaps().ReadOrder, ShouldResemble, []string{
				filepath.Join(home, "skills"),
				filepath.Join(home, ".agents", "skills"),
			})
		})
	})
}

func TestDSHSkillsAgentsHome(t *testing.T) {
	Convey("Given DSH_AGENTS_HOME", t, func() {
		home := t.TempDir()
		agents := filepath.Join(t.TempDir(), "shared-agents")

		t.Setenv("DSH_HOME", filepath.Join(home, ".dsh"))
		t.Setenv("DSH_AGENTS_HOME", agents)

		a := agent.DSH(home, t.TempDir())

		Convey("Then the rank-500 root is resolved from the environment", func() {
			area, ok := surfaceOf(t, a, kind.Skills).(agent.SkillReadArea)
			So(ok, ShouldBeTrue)
			So(area.ReadDirs(), ShouldResemble, []string{
				filepath.Join(home, ".dsh", "skills"),
				filepath.Join(agents, "skills"),
			})

			caps, ok := surfaceOf(t, a, kind.Skills).(agent.SkillCapsSurface)
			So(ok, ShouldBeTrue)
			So(caps.SkillCaps().ReadOrder, ShouldResemble, []string{
				filepath.Join(home, ".dsh", "skills"),
				filepath.Join(agents, "skills"),
			})
		})
	})

	Convey("Given an empty DSH_AGENTS_HOME", t, func() {
		home := t.TempDir()

		t.Setenv("DSH_HOME", filepath.Join(home, ".dsh"))
		t.Setenv("DSH_AGENTS_HOME", "")

		a := agent.DSH(home, t.TempDir())

		Convey("Then the empty value falls back to ~/.agents", func() {
			So(agent.DSHAgentsHome(home), ShouldEqual, filepath.Join(home, ".agents"))

			area, ok := surfaceOf(t, a, kind.Skills).(agent.SkillReadArea)
			So(ok, ShouldBeTrue)
			So(area.ReadDirs(), ShouldResemble, []string{
				filepath.Join(home, ".dsh", "skills"),
				filepath.Join(home, ".agents", "skills"),
			})
		})
	})
}

func TestDSHSkillsFlatRead(t *testing.T) {
	Convey("Given a flat skill in $DSH_HOME/skills", t, func() {
		surface, home := dshSkillsSurface(t)
		flat := filepath.Join(home, "skills", "alpha.md")
		canon := dshSkillDoc("name: alpha\ndescription: d\n")

		writeFile(t, flat, canon)

		Convey("When the surface is read", func() {
			snap := snapshot(t, agent.DSH(home, t.TempDir()), kind.Skills)

			Convey("Then the flat copy is a skill tree and a readable flat ref", func() {
				So(snap.Items["alpha/SKILL.md"], ShouldResemble, []byte(canon))

				reader, ok := surface.(agent.SkillFlatReader)
				So(ok, ShouldBeTrue)

				readable, shadowed, err := reader.FlatSkillRefs()
				So(err, ShouldBeNil)
				So(readable, ShouldResemble, map[string]string{"alpha": flat})
				So(shadowed, ShouldBeEmpty)
			})

			Convey("And a canon change is delivered as a directory", func() {
				changed := "---\nname: alpha\ndescription: d\n---\n\n# canon\n"

				err := surface.Write(t.Context(), kind.Items{"alpha/SKILL.md": []byte(changed)})

				// The flat file stays shadowed by the directory and untouched.
				So(err, ShouldBeNil)
				So(readFile(t, filepath.Join(home, "skills", "alpha", "SKILL.md")), ShouldContainSubstring, "# canon")
				So(readFile(t, flat), ShouldEqual, canon)
			})
		})
	})
}

func TestDSHSkillsSharedCopyReadOnly(t *testing.T) {
	Convey("Given a skill in the shared hub", t, func() {
		surface, home := dshSkillsSurface(t)
		shared := filepath.Join(home, ".agents", "skills", "beta", "SKILL.md")
		content := dshSkillDoc("name: beta\ndescription: shared\n")

		writeFile(t, shared, content)

		Convey("When the canon writes another skill", func() {
			err := surface.Write(t.Context(), kind.Items{"alpha/SKILL.md": []byte(dshSkillDoc("name: alpha\ndescription: d\n"))})

			Convey("Then the shared copy stays untouched", func() {
				So(err, ShouldBeNil)
				So(readFile(t, shared), ShouldEqual, content)
			})
		})
	})
}
