package subagent_test

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/subagent"
)

func TestSubagentRenderParseRoundTrip(t *testing.T) {
	Convey("Given a canonical document with every field", t, func() {
		yes := true
		doc := subagent.Document{
			Name:            "reviewer",
			Description:     "Reviews code",
			Mode:            "subagent",
			Model:           "inherit",
			Tools:           []string{"Read", "Grep"},
			DisallowedTools: []string{"Write"},
			PermissionMode:  "plan",
			MaxTurns:        5,
			Skills:          []string{"checklist"},
			MCPServers:      []any{"slack"},
			Background:      &yes,
			Isolation:       "worktree",
			Color:           "blue",
			Hidden:          &yes,
			Body:            "You review.\n",
		}

		rendered := subagent.Render(doc)

		Convey("When the document is rendered twice", func() {
			Convey("Then the bytes are deterministic", func() {
				So(string(subagent.Render(doc)), ShouldEqual, string(rendered))
			})
		})

		Convey("When it is parsed back", func() {
			back, err := subagent.Parse(rendered)

			Convey("Then every field survives and the render is stable", func() {
				So(err, ShouldBeNil)
				So(back.Name, ShouldEqual, "reviewer")
				So(back.Description, ShouldEqual, "Reviews code")
				So(back.Mode, ShouldEqual, "subagent")
				So(back.Model, ShouldEqual, "inherit")
				So(back.Tools, ShouldResemble, []string{"Read", "Grep"})
				So(back.DisallowedTools, ShouldResemble, []string{"Write"})
				So(back.PermissionMode, ShouldEqual, "plan")
				So(back.MaxTurns, ShouldEqual, 5)
				So(back.Skills, ShouldResemble, []string{"checklist"})
				So(back.MCPServers, ShouldResemble, []any{"slack"})
				So(*back.Background, ShouldBeTrue)
				So(back.Isolation, ShouldEqual, "worktree")
				So(back.Color, ShouldEqual, "blue")
				So(*back.Hidden, ShouldBeTrue)
				So(back.Body, ShouldEqual, "You review.\n")
				So(string(subagent.Render(back)), ShouldEqual, string(rendered))
			})
		})
	})
}

func TestSubagentToolsScalarAndList(t *testing.T) {
	Convey("Given frontmatter with tools as a comma string", t, func() {
		doc, err := subagent.Parse([]byte("---\nname: a\ndescription: d\ntools: Read, Grep Bash\n---\nbody\n"))

		Convey("Then the tools parse into a list", func() {
			So(err, ShouldBeNil)
			So(doc.Tools, ShouldResemble, []string{"Read", "Grep", "Bash"})
		})
	})

	Convey("Given frontmatter with tools as a YAML list", t, func() {
		doc, err := subagent.Parse([]byte("---\nname: a\ndescription: d\ntools: [Read, Grep]\n---\nbody\n"))

		Convey("Then the tools parse into a list", func() {
			So(err, ShouldBeNil)
			So(doc.Tools, ShouldResemble, []string{"Read", "Grep"})
		})
	})
}

func TestSubagentPatchPreservesForeignParts(t *testing.T) {
	Convey("Given a host file with foreign keys, a comment and a body", t, func() {
		existing := []byte("---\nname: reviewer\ndescription: old\n# keep me\nrequest:\n  body:\n    temperature: 0.1\n---\nold body\n")
		fields := []subagent.Field{{Key: "name", Value: "reviewer"}, {Key: "description", Value: "new"}}

		out, err := subagent.Patch(existing, []string{"name", "description"}, fields, "new body\n")

		Convey("When the frontmatter is patched", func() {
			Convey("Then foreign keys, their comments and the new body are kept", func() {
				So(err, ShouldBeNil)
				So(string(out), ShouldContainSubstring, "# keep me")
				So(string(out), ShouldContainSubstring, "request:")
				So(string(out), ShouldContainSubstring, "temperature: 0.1")
				So(string(out), ShouldContainSubstring, "description: new")
				So(string(out), ShouldContainSubstring, "new body")
				So(string(out), ShouldNotContainSubstring, "old body")
			})
		})
	})
}

func TestSubagentPatchCreatesFrontmatter(t *testing.T) {
	Convey("Given a file without frontmatter", t, func() {
		out, err := subagent.Patch([]byte("just a body\n"), subagent.Keys(),
			[]subagent.Field{{Key: "name", Value: "a"}, {Key: "description", Value: "d"}}, "just a body\n")

		Convey("When the frontmatter is patched", func() {
			Convey("Then a block is created before the body", func() {
				So(err, ShouldBeNil)
				So(string(out), ShouldStartWith, "---\n")
				So(string(out), ShouldContainSubstring, "name: a")
				So(string(out), ShouldContainSubstring, "just a body")
			})
		})
	})
}

func TestSubagentLift(t *testing.T) {
	Convey("Given a vault document, its projection and an agent update", t, func() {
		vault := []byte("---\nname: a\ndescription: vault\npermissionMode: plan\nskills: [x]\n---\nbody\n")
		projected := []byte("---\nname: a\ndescription: vault\n---\nbody\n")
		updated := []byte("---\nname: a\ndescription: changed\n---\nnew body\n")

		back, err := subagent.Parse(subagent.Lift(vault, projected, updated))

		Convey("Then changed fields win and unexpressible fields are kept", func() {
			So(err, ShouldBeNil)
			So(back.Description, ShouldEqual, "changed")
			So(back.PermissionMode, ShouldEqual, "plan")
			So(back.Skills, ShouldResemble, []string{"x"})
			So(back.Body, ShouldEqual, "new body\n")
		})
	})

	Convey("Given a field the host dropped", t, func() {
		vault := []byte("---\nname: a\ndescription: vault\ncolor: blue\n---\nbody\n")
		projected := []byte("---\nname: a\ndescription: vault\ncolor: blue\n---\nbody\n")
		updated := []byte("---\nname: a\ndescription: vault\n---\nbody\n")

		back, err := subagent.Parse(subagent.Lift(vault, projected, updated))

		Convey("Then the field is removed from the vault document", func() {
			So(err, ShouldBeNil)
			So(back.Color, ShouldEqual, "")
		})
	})
}

func TestSubagentSplitCRLF(t *testing.T) {
	Convey("Given a document with CRLF fences", t, func() {
		front, body, ok := subagent.Split([]byte("---\r\nname: a\r\n---\r\nbody\r\n"))

		Convey("Then the frontmatter and the body are separated", func() {
			So(ok, ShouldBeTrue)
			So(string(front), ShouldContainSubstring, "name: a")
			So(body, ShouldEqual, "body\r\n")
		})
	})
}

func TestSubagentPatchCRLFAndBlockScalar(t *testing.T) {
	Convey("Given a CRLF file with a block scalar and a foreign key", t, func() {
		existing := []byte("---\r\ndescription: >\r\n  line one\r\n  line two\r\nrequest:\r\n  body:\r\n    temperature: 0.1\r\n---\r\nbody line\r\n")

		doc, err := subagent.Parse(existing)
		So(err, ShouldBeNil)
		So(doc.Description, ShouldContainSubstring, "line one line two")

		patched, err := subagent.Patch(existing, []string{"description"},
			[]subagent.Field{{Key: "description", Value: "changed"}}, doc.Body)
		So(err, ShouldBeNil)
		So(string(patched), ShouldContainSubstring, "request:")
		So(string(patched), ShouldContainSubstring, "changed")
		So(string(patched), ShouldContainSubstring, "body line")

		back, err := subagent.Parse(patched)
		So(err, ShouldBeNil)
		So(back.Description, ShouldEqual, "changed")

		Convey("When the same patch is applied again", func() {
			again, err := subagent.Patch(patched, []string{"description"},
				[]subagent.Field{{Key: "description", Value: "changed"}}, back.Body)

			Convey("Then the bytes are stable", func() {
				So(err, ShouldBeNil)
				So(string(again), ShouldEqual, string(patched))
			})
		})
	})
}

func TestSubagentSplitFenceTrailingSpaces(t *testing.T) {
	Convey("Given a closing fence with trailing spaces", t, func() {
		front, body, ok := subagent.Split([]byte("---\nname: a\n---   \nbody\n"))

		Convey("Then the fence is recognized and the spaces stay out of the body", func() {
			So(ok, ShouldBeTrue)
			So(string(front), ShouldContainSubstring, "name: a")
			So(body, ShouldEqual, "body\n")
		})
	})
}
