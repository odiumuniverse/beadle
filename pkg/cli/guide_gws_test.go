package cli

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/guide"
)

func TestGuideCommand(t *testing.T) {
	Convey("Given the guide command", t, func() {
		Convey("When it runs without flags", func() {
			out, err := runCLI(t, "guide")

			Convey("Then it prints the AI guide", func() {
				So(err, ShouldBeNil)
				So(out, ShouldEqual, guide.AI())
				So(out, ShouldContainSubstring, "<rules>")
				So(out, ShouldContainSubstring, "<important>")
				So(out, ShouldContainSubstring, "hooks approve")
			})
		})

		Convey("When it runs with --humans", func() {
			out, err := runCLI(t, "guide", "--humans")

			Convey("Then it prints the human guide", func() {
				So(err, ShouldBeNil)
				So(out, ShouldEqual, guide.Humans())
				So(out, ShouldContainSubstring, "Quick start")
				So(out, ShouldNotContainSubstring, "<rules>")
			})
		})
	})
}

func TestGuidePointerInHelp(t *testing.T) {
	Convey("Given the root command", t, func() {
		Convey("When help runs", func() {
			out, err := runCLI(t, "--help")

			Convey("Then it points AI agents at the guide", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "Are you an AI agent? Run `beadle guide`.")
			})
		})
	})
}

func TestGuidePointerAfterInit(t *testing.T) {
	Convey("Given a fresh vault", t, func() {
		Convey("When init runs", func() {
			out, err := runCLI(t, "init")

			Convey("Then the next steps point AI agents at the guide", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "Are you an AI agent? Run `beadle guide`.")
			})
		})
	})
}
