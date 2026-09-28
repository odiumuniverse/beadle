package bundle_test

import (
	"encoding/json"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func TestBundleOmpHost(t *testing.T) {
	Convey("Given the omp bundle host", t, func() {
		Convey("Then its identity, binary and kinds are the omp ones", func() {
			So(bundle.Omp.AgentID(), ShouldEqual, agent.OmpID)
			So(bundle.Omp.Binary(), ShouldEqual, "omp")
			So(bundle.Omp.Kinds(), ShouldResemble, []kind.ID{kind.Skills, kind.MCP})
			So(bundle.Omp.ContentKinds(), ShouldResemble, []kind.ID{kind.Skills, kind.MCP})
			So(bundle.Hosts(), ShouldContain, bundle.Omp)
		})

		Convey("When parsing the aliases", func() {
			for _, name := range []string{"omp", "oh-my-pi", "OMP"} {
				host, err := bundle.ParseHost(name)

				Convey("Then "+name+" resolves to omp", func() {
					So(err, ShouldBeNil)
					So(host, ShouldEqual, bundle.Omp)
				})
			}
		})
	})
}

func TestBundlePlanOmpGolden(t *testing.T) {
	Convey("Given the omp fixture request", t, func() {
		result, files, err := bundle.Plan(fixtureRequest(bundle.Omp))

		Convey("When the bundle is planned", func() {
			Convey("Then the native catalog, the manifest omp reads the version from and the MCP document are rendered", func() {
				So(err, ShouldBeNil)
				So(versionPattern.MatchString(result.Version), ShouldBeTrue)

				// omp has no command-hook file: the approved hooks are named
				// once instead of warned about one by one, and no hooks file
				// is written.
				So(result.Warnings, ShouldResemble, []string{"omp has no command-hook file; 2 approved hook(s) are not delivered"})

				So(fileKeys(files), ShouldResemble, []string{
					".omp-plugin/marketplace.json",
					"plugins/beadle-canon/.claude-plugin/plugin.json",
					"plugins/beadle-canon/.mcp.json",
					"plugins/beadle-canon/skills/alpha/SKILL.md",
					"plugins/beadle-canon/skills/alpha/docs/note.txt",
				})

				So(string(files[".omp-plugin/marketplace.json"]), ShouldEqualJSON, `{
					"name": "beadle",
					"owner": {"name": "beadle"},
					"description": "beadle vault canon: skills, MCP servers and approved hooks",
					"plugins": [{"name": "beadle-canon", "source": "./plugins/beadle-canon", "version": "`+result.Version+`"}]
				}`)

				So(string(files["plugins/beadle-canon/.claude-plugin/plugin.json"]), ShouldEqualJSON, `{
					"name": "beadle-canon",
					"version": "`+result.Version+`",
					"description": "beadle vault canon: skills, MCP servers and approved hooks",
					"author": {"name": "beadle"}
				}`)

				So(string(files["plugins/beadle-canon/.mcp.json"]), ShouldEqualJSON, `{
					"mcpServers": {
						"plug": {"type": "stdio", "command": "node", "args": ["srv.js"]},
						"web": {"type": "http", "url": "https://example.com/mcp"}
					}
				}`)
			})

			Convey("And both version carriers hold the rendered version", func() {
				var catalog struct {
					Plugins []struct {
						Version string `json:"version"`
					} `json:"plugins"`
				}

				So(json.Unmarshal(files[".omp-plugin/marketplace.json"], &catalog), ShouldBeNil)
				So(catalog.Plugins, ShouldHaveLength, 1)
				So(catalog.Plugins[0].Version, ShouldEqual, result.Version)
			})
		})
	})
}

func TestBundlePlanOmpWithoutApprovedHooks(t *testing.T) {
	Convey("Given an omp request with no approved hook", t, func() {
		req := fixtureRequest(bundle.Omp)
		req.Approved = map[string]bool{}

		result, files, err := bundle.Plan(req)

		Convey("When the bundle is planned", func() {
			Convey("Then no hook warning is emitted and no catalog carries a hooks file", func() {
				So(err, ShouldBeNil)
				So(result.Warnings, ShouldBeEmpty)
				So(fileKeys(files), ShouldResemble, []string{
					".omp-plugin/marketplace.json",
					"plugins/beadle-canon/.claude-plugin/plugin.json",
					"plugins/beadle-canon/.mcp.json",
					"plugins/beadle-canon/skills/alpha/SKILL.md",
					"plugins/beadle-canon/skills/alpha/docs/note.txt",
				})
			})
		})
	})
}
