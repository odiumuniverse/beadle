package command_test

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/command"
)

func TestCommandRenderParseRoundTrip(t *testing.T) {
	Convey("Given a canonical command document", t, func() {
		doc := command.Document{
			Name:                   "review",
			Description:            "Reviews a diff",
			ArgumentHint:           "<pr>",
			Arguments:              []string{"PR"},
			Model:                  "haiku",
			DisableModelInvocation: new(true),
			Body:                   "Review $1 now.\n",
		}

		Convey("When it is rendered and parsed back", func() {
			rendered := command.Render(doc)

			back, err := command.Parse(rendered)

			Convey("Then every field survives and the name is never a frontmatter key", func() {
				So(err, ShouldBeNil)
				So(back.Name, ShouldEqual, "")
				So(back.Description, ShouldEqual, "Reviews a diff")
				So(back.ArgumentHint, ShouldEqual, "<pr>")
				So(back.Arguments, ShouldResemble, []string{"PR"})
				So(back.Model, ShouldEqual, "haiku")
				So(back.DisableModelInvocation, ShouldNotBeNil)
				So(*back.DisableModelInvocation, ShouldBeTrue)
				So(back.Body, ShouldEqual, "Review $1 now.\n")
				So(string(rendered), ShouldNotContainSubstring, "name:")
			})
		})

		Convey("When a document has no frontmatter fields", func() {
			rendered := command.Render(command.Document{Name: "bare", Body: "Just the template.\n"})

			Convey("Then no empty frontmatter block is written", func() {
				So(string(rendered), ShouldEqual, "Just the template.\n")
			})
		})

		Convey("When a document without a body or frontmatter is parsed", func() {
			doc, err := command.Parse([]byte("Just a template.\n"))

			Convey("Then only the body is decoded", func() {
				So(err, ShouldBeNil)
				So(doc.Body, ShouldEqual, "Just a template.\n")
				So(doc.Description, ShouldEqual, "")
			})
		})
	})
}

func TestCommandLift(t *testing.T) {
	Convey("Given a vault command and an agent projection", t, func() {
		vault := command.Render(command.Document{
			Name: "review", Description: "Vault description", Model: "inherit",
			ArgumentHint: "<pr>", Body: "vault body\n",
		})
		// The projection cannot express model and argument-hint: they are
		// absent, so the vault value must survive.
		projected := command.Render(command.Document{Name: "review", Body: "projected\n"})
		updated := command.Render(command.Document{
			Name: "review", Description: "Agent description", Body: "agent body\n",
		})

		Convey("When the agent update is lifted", func() {
			out := command.Lift(vault, projected, updated)

			doc, err := command.Parse(out)

			Convey("Then changed fields win and unexpressible ones keep the vault value", func() {
				So(err, ShouldBeNil)
				So(doc.Description, ShouldEqual, "Agent description")
				So(doc.Body, ShouldEqual, "agent body\n")
				So(doc.Model, ShouldEqual, "inherit")
				So(doc.ArgumentHint, ShouldEqual, "<pr>")
			})
		})

		Convey("When the host expresses the description and the agent drops it", func() {
			proj := command.Render(command.Document{Name: "review", Description: "Vault description", Body: "projected\n"})
			dropped := command.Render(command.Document{Name: "review", Body: "projected\n"})

			Convey("Then the vault description is removed", func() {
				doc, err := command.Parse(command.Lift(vault, proj, dropped))
				So(err, ShouldBeNil)
				So(doc.Description, ShouldEqual, "")
			})
		})
	})
}

func TestCommandValidName(t *testing.T) {
	Convey("Given command slugs", t, func() {
		Convey("Then the canonical slug rules apply", func() {
			So(command.ValidName("review"), ShouldBeTrue)
			So(command.ValidName("team_review-2"), ShouldBeTrue)
			So(command.ValidName(""), ShouldBeFalse)
			So(command.ValidName("Review"), ShouldBeFalse)
			So(command.ValidName("-lead"), ShouldBeFalse)
			So(command.ValidName("team/review"), ShouldBeFalse)
		})
	})
}
