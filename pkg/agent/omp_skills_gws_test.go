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

// ompSkillDoc builds a canon SKILL.md carrying the given frontmatter body.
func ompSkillDoc(front string) string {
	return "---\n" + front + "---\n\n# skill\n"
}

// ompSkillsSurface builds an omp adapter whose skills surface lives in a
// fresh home and returns the adapter with the home it writes into.
func ompSkillsSurface(t *testing.T) (*agent.Agent, string) {
	t.Helper()

	home := t.TempDir()

	withoutOmpEnv(t)

	return agent.Omp(home, t.TempDir()), home
}

func TestOmpSkillsCaps(t *testing.T) {
	Convey("Given the omp skills surface", t, func() {
		home := t.TempDir()

		withoutOmpEnv(t)

		a := agent.Omp(home, t.TempDir())
		surface := surfaceOf(t, a, kind.Skills)
		own := filepath.Join(home, ".omp", "agent", "skills")
		shared := filepath.Join(home, ".agents", "skills")

		Convey("Then the native root and the shared agents home are read", func() {
			area, ok := surface.(agent.SkillReadArea)
			So(ok, ShouldBeTrue)
			So(area.ReadDirs(), ShouldResemble, []string{own, shared})

			declared, ok := surface.(agent.SkillCapsSurface)
			So(ok, ShouldBeTrue)

			caps := declared.SkillCaps()
			So(caps.Shadowing, ShouldBeTrue)
			So(caps.ReadOrder, ShouldResemble, []string{own, shared})
			So(caps.NamespacedBundle, ShouldBeFalse)

			So(surface.Traits().DefaultMode, ShouldEqual, config.ModePull)
			So(surface.Traits().Creatable, ShouldBeTrue)
		})

		Convey("When both roots hold the same skill", func() {
			writeFile(t, filepath.Join(own, "alpha", "SKILL.md"), ompSkillDoc("name: alpha\ndescription: own\n"))
			writeFile(t, filepath.Join(shared, "alpha", "SKILL.md"), ompSkillDoc("name: alpha\ndescription: shared\n"))

			Convey("Then the shared copy is readable but never enters the canon", func() {
				refs := readableRefs(t, a)
				So(readableDirs(refs), ShouldResemble, []string{own, shared})

				snap := snapshot(t, a, kind.Skills)
				So(string(snap.Items["alpha/SKILL.md"]), ShouldContainSubstring, "description: own")
			})
		})

		Convey("When a skill is written", func() {
			canon := ompSkillDoc("name: alpha\ndescription: first\n")

			err := surface.Write(t.Context(), kind.Items{"alpha/SKILL.md": []byte(canon)})

			Convey("Then the file carries the canon bytes and a repeat write is a no-op", func() {
				So(err, ShouldBeNil)

				path := filepath.Join(own, "alpha", "SKILL.md")
				So(readFile(t, path), ShouldEqual, canon)

				before := readFile(t, path)

				So(surface.Write(t.Context(), kind.Items{"alpha/SKILL.md": []byte(canon)}), ShouldBeNil)
				So(readFile(t, path), ShouldEqual, before)
			})
		})
	})
}

func TestOmpSkillsCodec(t *testing.T) {
	Convey("Given an omp skills surface", t, func() {
		a, home := ompSkillsSurface(t)
		surface := surfaceOf(t, a, kind.Skills)
		dir := filepath.Join(home, ".omp", "agent", "skills")

		Convey("When a skill without a description is written", func() {
			item := kind.Items{"alpha/SKILL.md": []byte(ompSkillDoc("name: alpha\n"))}

			err := surface.Write(t.Context(), item)

			Convey("Then the write is refused before anything lands on disk", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "requires a description")

				_, statErr := os.Stat(filepath.Join(dir, "alpha"))
				So(os.IsNotExist(statErr), ShouldBeTrue)
			})
		})

		Convey("When a skill has no frontmatter at all", func() {
			err := surface.Write(t.Context(), kind.Items{"alpha/SKILL.md": []byte("# bare\n")})

			Convey("Then the missing description is reported", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "needs YAML frontmatter with a description")
			})
		})

		Convey("When a skill name is not kebab-case", func() {
			item := kind.Items{"Bad_Name/SKILL.md": []byte(ompSkillDoc("name: Bad_Name\ndescription: d\n"))}

			err := surface.Write(t.Context(), item)

			Convey("Then it is refused with the naming rule", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "is not kebab-case")
			})
		})

		Convey("When the frontmatter name does not match the directory", func() {
			item := kind.Items{"alpha/SKILL.md": []byte(ompSkillDoc("name: beta\ndescription: d\n"))}

			err := surface.Write(t.Context(), item)

			Convey("Then it is refused: omp would expose it under the frontmatter name", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "does not match the skill directory")
			})
		})

		// Live-verified against omp 18.4.1: both spellings are accepted (the
		// native provider normalizes the kebab form into the camelCase field),
		// so the codec must not refuse either one.
		for _, invocation := range []string{"disable-model-invocation: true", "disableModelInvocation: true"} {
			Convey("When a skill carries "+invocation, func() {
				canon := ompSkillDoc("name: alpha\ndescription: d\n" + invocation + "\n")

				err := surface.Write(t.Context(), kind.Items{"alpha/SKILL.md": []byte(canon)})

				Convey("Then it is accepted byte-for-byte", func() {
					So(err, ShouldBeNil)
					So(readFile(t, filepath.Join(dir, "alpha", "SKILL.md")), ShouldEqual, canon)
				})
			})
		}
	})
}
