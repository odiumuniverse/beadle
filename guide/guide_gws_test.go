package guide_test

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/guide"
)

var (
	viewBoxRe = regexp.MustCompile(`viewBox="0 0 ([0-9]+) ([0-9]+)"`)
	coordRe   = regexp.MustCompile(`(?:^|[ "])(x|y|x1|y1|x2|y2)="(-?[0-9.]+)"`)
	rectRe    = regexp.MustCompile(`<rect x="([0-9.]+)" y="([0-9.]+)" width="([0-9.]+)" height="([0-9.]+)"`)
)

func TestGuideEmbed(t *testing.T) {
	Convey("Given the embedded guides", t, func() {
		Convey("When they are read", func() {
			Convey("Then they equal the repository files and carry the required sections", func() {
				ai, err := os.ReadFile("ai-agents.md")
				So(err, ShouldBeNil)
				So(guide.AI(), ShouldNotBeEmpty)
				So(guide.AI(), ShouldEqual, string(ai))
				So(guide.AI(), ShouldContainSubstring, "<rules>")
				So(guide.AI(), ShouldContainSubstring, "<important>")
				So(guide.AI(), ShouldContainSubstring, "<never>")
				So(guide.AI(), ShouldContainSubstring, "hooks approve")
				So(guide.AI(), ShouldContainSubstring, "beadle guide")

				humans, err := os.ReadFile("humans.md")
				So(err, ShouldBeNil)
				So(guide.Humans(), ShouldNotBeEmpty)
				So(guide.Humans(), ShouldEqual, string(humans))
				So(guide.Humans(), ShouldContainSubstring, "Quick start")
				So(guide.Humans(), ShouldContainSubstring, "```")
			})
		})
	})
}

func TestGuideDiagrams(t *testing.T) {
	Convey("Given the guide diagrams", t, func() {
		names := []string{
			"guide-architecture.svg",
			"guide-sync-map.svg",
			"guide-plugin-flow.svg",
			"guide-conflict-flow.svg",
		}

		Convey("When each diagram is validated", func() {
			for _, name := range names {
				path := filepath.Join("..", "assets", name)

				data, err := os.ReadFile(path) //nolint:gosec // G304: the test reads its own repository file
				So(err, ShouldBeNil)

				var doc struct{}
				So(xml.Unmarshal(data, &doc), ShouldBeNil)

				box := viewBoxRe.FindStringSubmatch(string(data))
				So(box, ShouldHaveLength, 3)

				width, err := strconv.Atoi(box[1])
				So(err, ShouldBeNil)
				height, err := strconv.Atoi(box[2])
				So(err, ShouldBeNil)
				So(width, ShouldBeGreaterThan, 300)
				So(height, ShouldBeGreaterThan, 200)

				for _, match := range coordRe.FindAllStringSubmatch(string(data), -1) {
					value, err := strconv.ParseFloat(match[2], 64)
					So(err, ShouldBeNil)

					limit := width
					if match[1] == "y" || match[1] == "y1" || match[1] == "y2" {
						limit = height
					}

					So(value, ShouldBeBetweenOrEqual, 0, limit)
				}

				for _, match := range rectRe.FindAllStringSubmatch(string(data), -1) {
					x, _ := strconv.ParseFloat(match[1], 64)
					y, _ := strconv.ParseFloat(match[2], 64)
					w, _ := strconv.ParseFloat(match[3], 64)
					h, _ := strconv.ParseFloat(match[4], 64)

					So(x+w, ShouldBeLessThanOrEqualTo, width)
					So(y+h, ShouldBeLessThanOrEqualTo, height)
				}

				So(string(data), ShouldContainSubstring, "#3E6E5C")
				So(string(data), ShouldContainSubstring, `fill="none"`)
			}
		})

		Convey("Then the human guide embeds every diagram", func() {
			for _, name := range names {
				So(guide.Humans(), ShouldContainSubstring, "../assets/"+name)
			}
		})

		Convey("Then the README links resolve", func() {
			readme, err := os.ReadFile(filepath.Join("..", "README.md")) //nolint:gosec // G304: the test reads its own repository file
			So(err, ShouldBeNil)
			So(string(readme), ShouldContainSubstring, "guide/ai-agents.md")
			So(string(readme), ShouldContainSubstring, "guide/humans.md")
			So(string(readme), ShouldContainSubstring, "assets/guide-architecture.svg")
		})
	})
}
