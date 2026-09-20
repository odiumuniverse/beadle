package bundle_test

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/hooks"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

var versionPattern = regexp.MustCompile(`^0\.0\.0-[0-9a-f]{12}$`)

func fixtureRequest(host bundle.Host) bundle.Request {
	return bundle.Request{
		Host: host,
		Skills: map[string]map[string][]byte{
			"alpha": {"SKILL.md": []byte("# alpha\n"), "docs/note.txt": []byte("note\n")},
		},
		Servers: kind.Items{
			"plug": mcp.Encode(mcp.Server{Command: []string{"node", "srv.js"}}),
			"web":  mcp.Encode(mcp.Server{Transport: mcp.TransportHTTP, URL: "https://example.com/mcp"}),
		},
		Hooks: map[string]hooks.Hook{
			"notify": {Event: "session-start", Command: "echo hi", Timeout: 5},
			"lint":   {Event: "post-tool", Matcher: "Bash", Command: "make lint"},
			"secret": {Event: "stop", Command: "rm -rf /"},
		},
		Approved: map[string]bool{"notify": true, "lint": true},
	}
}

func fileKeys(files map[string][]byte) []string {
	keys := slices.Collect(maps.Keys(files))
	slices.Sort(keys)

	return keys
}

func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()

	out := map[string]string{}

	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		data, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp tree
		if err != nil {
			return err
		}

		out[filepath.ToSlash(rel)] = string(data)

		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}

	return out
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return data
}

func TestBundleParseHost(t *testing.T) {
	Convey("Given aliases mapping to bundle hosts", t, func() {
		for name, want := range map[string]bundle.Host{
			"claude":          bundle.Claude,
			"claude-code":     bundle.Claude,
			"gemini":          bundle.Gemini,
			"gemini-cli":      bundle.Gemini,
			"antigravity":     bundle.Antigravity,
			"antigravity-cli": bundle.Antigravity,
		} {
			Convey("When parsing "+name, func() {
				got, err := bundle.ParseHost(name)

				Convey("Then it resolves to the host", func() {
					So(err, ShouldBeNil)
					So(got, ShouldEqual, want)
				})
			})
		}

		Convey("When parsing an unknown host", func() {
			_, err := bundle.ParseHost("cursor")

			Convey("Then it is rejected", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "unknown bundle host")
			})
		})
	})
}

func TestBundlePlanClaudeGolden(t *testing.T) {
	Convey("Given the Claude fixture request", t, func() {
		result, files, err := bundle.Plan(fixtureRequest(bundle.Claude))

		Convey("When the bundle is planned", func() {
			Convey("Then the file set, marketplace, plugin, MCP and hooks match the golden", func() {
				So(err, ShouldBeNil)
				So(result.Warnings, ShouldBeEmpty)
				So(versionPattern.MatchString(result.Version), ShouldBeTrue)

				So(fileKeys(files), ShouldResemble, []string{
					".claude-plugin/marketplace.json",
					"plugins/beadle-canon/.claude-plugin/plugin.json",
					"plugins/beadle-canon/.mcp.json",
					"plugins/beadle-canon/hooks/hooks.json",
					"plugins/beadle-canon/skills/alpha/SKILL.md",
					"plugins/beadle-canon/skills/alpha/docs/note.txt",
				})

				So(string(files[".claude-plugin/marketplace.json"]), ShouldEqualJSON, `{
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

				So(string(files["plugins/beadle-canon/hooks/hooks.json"]), ShouldEqualJSON, `{
					"SessionStart": [{"matcher": "*", "hooks": [{"type": "command", "command": "echo hi", "timeout": 5}]}],
					"PostToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "make lint"}]}]
				}`)

				So(string(files["plugins/beadle-canon/skills/alpha/SKILL.md"]), ShouldEqual, "# alpha\n")
				So(string(files["plugins/beadle-canon/hooks/hooks.json"]), ShouldNotContainSubstring, "rm -rf")
			})
		})
	})
}

func TestBundlePlanGeminiFiles(t *testing.T) {
	Convey("Given a Gemini request with an unmappable stop hook", t, func() {
		req := fixtureRequest(bundle.Gemini)
		req.Hooks["stopper"] = hooks.Hook{Event: "stop", Command: "true"}
		req.Approved["stopper"] = true

		result, files, err := bundle.Plan(req)

		Convey("When the bundle is planned", func() {
			Convey("Then the stop hook is warned about and only extension files are rendered", func() {
				So(err, ShouldBeNil)
				So(versionPattern.MatchString(result.Version), ShouldBeTrue)
				So(result.Warnings, ShouldHaveLength, 1)
				So(result.Warnings[0], ShouldContainSubstring, "hook stopper (stop): no gemini event; not rendered")

				So(fileKeys(files), ShouldResemble, []string{"gemini-extension.json", "hooks/hooks.json"})

				So(string(files["gemini-extension.json"]), ShouldEqualJSON, `{
					"name": "beadle-canon",
					"version": "`+result.Version+`",
					"description": "beadle vault canon: MCP servers and approved hooks",
					"mcpServers": {
						"plug": {"command": "node", "args": ["srv.js"]},
						"web": {"httpUrl": "https://example.com/mcp"}
					}
				}`)

				So(string(files["hooks/hooks.json"]), ShouldEqualJSON, `{
					"SessionStart": [{"matcher": "*", "hooks": [{"name": "notify", "type": "command", "command": "echo hi", "timeout": 5000}]}],
					"AfterTool": [{"matcher": "Bash", "hooks": [{"name": "lint", "type": "command", "command": "make lint"}]}]
				}`)
			})
		})
	})
}

func TestBundlePlanAntigravityGolden(t *testing.T) {
	Convey("Given an Antigravity request with unmappable and matcher cases", t, func() {
		req := fixtureRequest(bundle.Antigravity)
		req.Hooks["greet"] = hooks.Hook{Event: "session-start", Matcher: "Bash", Command: "echo start"}
		req.Approved["greet"] = true
		req.Hooks["alert"] = hooks.Hook{Event: "notification", Command: "true"}
		req.Approved["alert"] = true

		result, files, err := bundle.Plan(req)

		Convey("When the bundle is planned", func() {
			Convey("Then warnings and the owner-keyed hooks match the golden", func() {
				So(err, ShouldBeNil)
				So(versionPattern.MatchString(result.Version), ShouldBeTrue)
				So(result.Warnings, ShouldHaveLength, 2)
				So(result.Warnings[0], ShouldContainSubstring, "hook alert (notification): no antigravity event; not rendered")
				So(result.Warnings[1], ShouldContainSubstring, "antigravity ignores matchers on PreInvocation; matcher dropped")

				So(fileKeys(files), ShouldResemble, []string{
					"hooks.json",
					"mcp_config.json",
					"plugin.json",
					"skills/alpha/SKILL.md",
					"skills/alpha/docs/note.txt",
				})

				So(string(files["plugin.json"]), ShouldEqualJSON, `{"name": "beadle-canon", "version": "`+result.Version+`"}`)

				So(string(files["mcp_config.json"]), ShouldEqualJSON, `{
					"mcpServers": {
						"plug": {"command": "node", "args": ["srv.js"]},
						"web": {"serverUrl": "https://example.com/mcp"}
					}
				}`)

				So(string(files["hooks.json"]), ShouldEqualJSON, `{
					"beadle-canon": {
						"PreInvocation": [
							{"type": "command", "command": "echo start"},
							{"type": "command", "command": "echo hi", "timeout": 5}
						],
						"PostToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "make lint"}]}]
					}
				}`)
			})
		})
	})
}

func TestBundleRenderDeterministic(t *testing.T) {
	Convey("Given a bundle root and an unchanged canon", t, func() {
		root := t.TempDir()

		first, err := bundle.Render(root, fixtureRequest(bundle.Claude))
		So(err, ShouldBeNil)

		before := treeSnapshot(t, filepath.Join(root, "claude"))

		Convey("When it is rendered a second time", func() {
			second, err := bundle.Render(root, fixtureRequest(bundle.Claude))

			Convey("Then nothing is rewritten and the version is stable", func() {
				So(err, ShouldBeNil)
				So(first.Changed, ShouldBeTrue)
				So(versionPattern.MatchString(first.Version), ShouldBeTrue)
				So(second.Changed, ShouldBeFalse)
				So(second.Version, ShouldEqual, first.Version)
				So(treeSnapshot(t, filepath.Join(root, "claude")), ShouldResemble, before)
			})
		})
	})
}

func TestBundleRenderCanonChange(t *testing.T) {
	Convey("Given a rendered bundle with a stale file", t, func() {
		root := t.TempDir()

		first, err := bundle.Render(root, fixtureRequest(bundle.Claude))
		So(err, ShouldBeNil)

		stale := filepath.Join(root, "claude", "plugins", "beadle-canon", "skills", "stale.md")
		So(os.WriteFile(stale, []byte("stale\n"), 0o600), ShouldBeNil)

		req := fixtureRequest(bundle.Claude)
		req.Skills["alpha"]["SKILL.md"] = []byte("# alpha v2\n")

		Convey("When the canon changes", func() {
			second, err := bundle.Render(root, req)
			So(err, ShouldBeNil)

			_, statErr := os.Stat(stale)

			Convey("Then the version changes, the skill updates and the stale file is removed", func() {
				So(second.Changed, ShouldBeTrue)
				So(second.Version, ShouldNotEqual, first.Version)
				So(string(readFile(t, filepath.Join(root, "claude", "plugins", "beadle-canon", "skills", "alpha", "SKILL.md"))), ShouldEqual, "# alpha v2\n")
				So(statErr, ShouldNotBeNil)
			})
		})
	})
}

func TestBundleVersionDependsOnApprovalAndHost(t *testing.T) {
	Convey("Given a base plan", t, func() {
		req := fixtureRequest(bundle.Claude)
		base, _, err := bundle.Plan(req)
		So(err, ShouldBeNil)

		Convey("When an extra hook is approved", func() {
			req.Approved["secret"] = true

			approved, _, err := bundle.Plan(req)
			So(err, ShouldBeNil)

			Convey("Then the version changes", func() {
				So(approved.Version, ShouldNotEqual, base.Version)
			})
		})

		Convey("When the host changes", func() {
			gemini, _, err := bundle.Plan(fixtureRequest(bundle.Gemini))
			So(err, ShouldBeNil)

			Convey("Then the version changes", func() {
				So(gemini.Version, ShouldNotEqual, base.Version)
			})
		})
	})
}

func TestBundlePlanRejectsUnknownAgentDialect(t *testing.T) {
	Convey("Given a request with an unsupported host", t, func() {
		Convey("When the bundle is planned", func() {
			_, _, err := bundle.Plan(bundle.Request{Host: bundle.Host("cursor")})

			Convey("Then it is rejected", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "unsupported bundle host")
			})
		})
	})
}

func TestBundleGoldenJSONIsValidJSON(t *testing.T) {
	Convey("Given every bundle host", t, func() {
		for _, host := range bundle.Hosts() {
			Convey("When planning "+string(host), func() {
				_, files, err := bundle.Plan(fixtureRequest(host))
				So(err, ShouldBeNil)

				for rel, data := range files {
					if strings.HasSuffix(rel, ".json") {
						So(json.Valid(data), ShouldBeTrue)
					}
				}
			})
		}
	})
}
