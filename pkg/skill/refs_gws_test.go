package skill_test

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/skill"
)

func TestSkillReferences(t *testing.T) {
	Convey("Given skill bodies with documented reference phrases", t, func() {
		Convey("When the bodies are scanned for references", func() {
			Convey("Then the incident phrase is found", func() {
				refs := skill.References([]byte("Call the Skill tool with \"grilling\".\n"))
				So(refs, ShouldHaveLength, 1)
				So(refs[0].Name, ShouldEqual, "grilling")
			})

			Convey("Then every documented form is recognized", func() {
				body := []byte("Skill(alpha)\n" +
					"Call the skill \"beta\".\n" +
					"load skill gamma.\n" +
					"Use the skill \"delta\", then invoke skill epsilon.\n" +
					"Run /my-plugin:zeta now.\n" +
					"Call skill \"eta\".\n")

				names := []string{}
				for _, ref := range skill.References(body) {
					names = append(names, ref.Name)
				}

				So(names, ShouldResemble, []string{"alpha", "beta", "delta", "epsilon", "eta", "gamma", "zeta"})
			})

			Convey("Then a plugin-qualified call keeps the plugin", func() {
				refs := skill.References([]byte("Use Skill(beadle-canon:grill-me) instead.\n"))
				So(refs, ShouldHaveLength, 1)
				So(refs[0].Name, ShouldEqual, "grill-me")
				So(refs[0].Plugin, ShouldEqual, "beadle-canon")
			})

			Convey("Then names are normalized and deduplicated per namespace", func() {
				body := []byte("Skill(Grilling) and skill \"grilling\" and /p:grilling.\n")
				refs := skill.References(body)
				So(refs, ShouldHaveLength, 2)
				So(refs[0].Plugin, ShouldEqual, "")
				So(refs[0].Name, ShouldEqual, "grilling")
				So(refs[1].Plugin, ShouldEqual, "p")
				So(refs[1].Name, ShouldEqual, "grilling")
			})

			Convey("Then an explicit form wins the dedup over a prose-prone one", func() {
				refs := skill.References([]byte("load skill safely. Then skill \"safely\".\n"))
				So(refs, ShouldHaveLength, 1)
				So(refs[0].Name, ShouldEqual, "safely")
				So(refs[0].Explicit, ShouldBeTrue)
			})

			Convey("Then quoted names inside Skill(...) are recognized", func() {
				refs := skill.References([]byte("Use Skill(\"grilling\") now.\n"))
				So(refs, ShouldHaveLength, 1)
				So(refs[0].Name, ShouldEqual, "grilling")
				So(refs[0].Quoted, ShouldBeTrue)
			})

			Convey("Then unquoted names at the end of a line are recognized", func() {
				refs := skill.References([]byte("First load skill grilling\nthen continue.\n"))
				So(refs, ShouldHaveLength, 1)
				So(refs[0].Name, ShouldEqual, "grilling")
				So(refs[0].Quoted, ShouldBeFalse)
			})

			Convey("Then plain prose about skills is not a reference", func() {
				body := []byte("This skill loads skills from the skills directory. " +
					"The Skill tool is useful; call the Skill tool when needed. " +
					"See the skills documentation for the skill file format.\n")
				So(skill.References(body), ShouldBeEmpty)
			})

			Convey("Then unquoted names must end a clause", func() {
				body := []byte("Call the skill tool with care and load skill from disk.\n")
				So(skill.References(body), ShouldBeEmpty)
			})

			Convey("Then the unquoted skill-tool form is not a phrase at all", func() {
				So(skill.References([]byte("Call the skill tool with care.\n")), ShouldBeEmpty)
			})

			Convey("Then prose verbs stay unquoted candidates for the caller", func() {
				refs := skill.References([]byte("Use skill safely. Then load skill files.\n"))
				So(refs, ShouldHaveLength, 2)
				So(refs[0].Name, ShouldEqual, "files")
				So(refs[0].Quoted, ShouldBeFalse)
				So(refs[1].Name, ShouldEqual, "safely")
				So(refs[1].Quoted, ShouldBeFalse)
			})

			Convey("Then build tags, lint directives and URLs are not references", func() {
				body := []byte("//go:build linux\n//nolint:errcheck\nSee http://localhost:6060/debug and /x:6060.\n")
				So(skill.References(body), ShouldBeEmpty)
			})

			Convey("Then non-slug captures are rejected", func() {
				So(skill.References([]byte("Skill(../../etc/passwd)\n")), ShouldBeEmpty)
				So(skill.References([]byte("Skill()\n")), ShouldBeEmpty)
			})
		})
	})
}
