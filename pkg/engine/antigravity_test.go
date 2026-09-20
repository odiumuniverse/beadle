package engine_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

func antigravityServers(t *testing.T, path string) map[string]map[string]any {
	t.Helper()

	var doc struct {
		Servers map[string]map[string]any `json:"mcpServers"`
	}

	if err := json.Unmarshal([]byte(read(t, path)), &doc); err != nil {
		t.Fatalf("unmarshal antigravity config: %v", err)
	}

	return doc.Servers
}

func TestAntigravityUnion(t *testing.T) {
	Convey("Given an Antigravity install with Claude and OpenCode servers", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)
		f.config.Enable(agent.AntigravityCLIID)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		agyConfig := filepath.Join(f.home, ".gemini", "config", "mcp_config.json")
		write(t, agyConfig, `{"mcpServers": {}}`)

		write(t, f.claudeConfig(), `{"mcpServers": {
			"alpha": {"type": "stdio", "command": "a", "args": ["x"]},
			"gamma": {"type": "http", "url": "https://gamma.example.com/mcp"}
		}}`)
		write(t, f.openCodeConfig(), `{"mcp": {"beta": {"type": "local", "command": ["b"]}}}`)

		report := f.sync(t)
		So(report.Kind(kind.MCP).VaultChanged, ShouldBeTrue)

		servers := antigravityServers(t, agyConfig)

		Convey("When the servers are read back", func() {
			for _, entry := range servers {
				So(entry, ShouldNotContainKey, "url")
				So(entry, ShouldNotContainKey, "httpUrl")
			}

			var doc map[string]any
			So(json.Unmarshal([]byte(read(t, agyConfig)), &doc), ShouldBeNil)

			raw, ok := doc["mcpServers"].(map[string]any)
			So(ok, ShouldBeTrue)

			alpha, ok := raw["alpha"].(map[string]any)
			So(ok, ShouldBeTrue)

			alpha["env"] = map[string]any{"TOKEN": "${LOCAL}"}

			edited, err := json.Marshal(doc)
			So(err, ShouldBeNil)

			write(t, agyConfig, string(edited))

			f.sync(t)

			canon, err := mcp.ParseCanonical([]byte(read(t, f.vault.ServersPath())))
			So(err, ShouldBeNil)

			report = f.sync(t)

			Convey("Then edits reach the canon and no extra surface files exist", func() {
				So(servers, ShouldHaveLength, 3)
				So(servers["alpha"]["command"], ShouldEqual, "a")
				So(servers["alpha"]["args"], ShouldResemble, []any{"x"})
				So(servers["beta"]["command"], ShouldEqual, "b")
				So(servers["gamma"]["serverUrl"], ShouldEqual, "https://gamma.example.com/mcp")

				So(canon["alpha"].Env["TOKEN"], ShouldEqual, "{env:LOCAL}")
				So(report.Kind(kind.MCP).Pulled, ShouldBeEmpty)
				So(report.VaultChanged(), ShouldBeFalse)

				agy := agent.AntigravityCLI(f.home, t.TempDir())

				for _, k := range []kind.ID{kind.Rules, kind.Skills, kind.Permissions} {
					So(agy.Surface(k), ShouldBeNil)
				}

				var rels []string

				root := filepath.Join(f.home, ".gemini")

				walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
					if err != nil {
						return err
					}

					rel, err := filepath.Rel(root, path)
					if err != nil {
						return err
					}

					if rel != "." {
						rels = append(rels, rel)
					}

					return nil
				})
				So(walkErr, ShouldBeNil)

				So(rels, ShouldResemble, []string{"config", "config/mcp_config.json"})
			})
		})
	})
}
