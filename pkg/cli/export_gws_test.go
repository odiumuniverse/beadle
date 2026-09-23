package cli

import (
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agentplugins"
)

func exportSkillDoc(name string) string {
	return "---\nname: " + name + "\ndescription: export test skill\n---\n\n# " + name + "\n"
}

const exportServers = `{
  "fs": {
    "transport": "stdio",
    "command": ["npx", "-y", "mcp-fs", "${PLUGIN_ROOT}/data"],
    "env": {"CACHE_DIR": "${PLUGIN_ROOT}/cache"}
  },
  "remote": {
    "transport": "http",
    "url": "https://example.com/mcp",
    "headers": {"Authorization": "Bearer ${API_KEY}"}
  },
  "escape": {
    "transport": "stdio",
    "command": ["run", "${PLUGIN_ROOT}/../outside"]
  }
}`

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: tests read their own temp files
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(data)
}

// snapshotTree maps every regular file under dir to its bytes.
func snapshotTree(t *testing.T, dir string) map[string]string {
	t.Helper()

	out := map[string]string{}

	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}

		out[rel] = readFile(t, path)

		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", dir, err)
	}

	return out
}

func TestExportRequiresInitializedVault(t *testing.T) {
	Convey("Given a vault directory without a config", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))

		out := filepath.Join(t.TempDir(), "package")

		Convey("When the export runs", func() {
			_, _, err := gwsRunSplit(t, "export", "agent-plugins", "--out", out)

			Convey("Then it refuses with the init hint and writes nothing", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "run beadle init")

				_, statErr := os.Stat(filepath.Join(home, ".beadle", "config.json"))
				So(errors.Is(statErr, fs.ErrNotExist), ShouldBeTrue)

				_, statErr = os.Stat(out)
				So(errors.Is(statErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestExportAgentPluginsEndToEnd(t *testing.T) {
	Convey("Given a vault canon with skills and MCP servers", t, func() {
		home := t.TempDir()

		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		vaultDir := filepath.Join(home, ".beadle")

		// init seeds the built-in skill; the fixture canon is what this test
		// exports.
		So(os.RemoveAll(filepath.Join(vaultDir, "skills", "beadle-conflicts")), ShouldBeNil)

		writeFile(t, filepath.Join(vaultDir, "skills", "alpha", "SKILL.md"), exportSkillDoc("alpha"))
		writeFile(t, filepath.Join(vaultDir, "skills", "beta", "SKILL.md"), exportSkillDoc("beta"))
		writeFile(t, filepath.Join(vaultDir, "skills", "gamma", "SKILL.md"), exportSkillDoc("gamma"))
		writeFile(t, filepath.Join(vaultDir, "skills", "gamma", "notes.md"), "nested\n")
		writeFile(t, filepath.Join(vaultDir, "mcp", "servers.json"), exportServers)

		out := filepath.Join(t.TempDir(), "package")
		foreign := filepath.Join(out, "KEEP.md")

		writeFile(t, foreign, "mine\n")

		stdout, stderr, err := gwsRunSplit(t, "export", "agent-plugins", "--out", out)

		Convey("When the export runs", func() {
			Convey("Then the package is conformant and the report names what was skipped", func() {
				So(err, ShouldBeNil)
				So(stdout, ShouldContainSubstring, "skills: 3 rendered, 0 skipped; mcp: 2 rendered, 1 skipped")
				So(stderr, ShouldContainSubstring, "mcp escape is skipped: a ${PLUGIN_ROOT} or relative reference escapes the package root")
				So(stderr, ShouldContainSubstring, "mcp remote: headers Authorization references ${API_KEY}; clients do not expand it")

				So(agentplugins.Validate(out), ShouldBeNil)

				So(readFile(t, filepath.Join(out, "skills", "alpha", "SKILL.md")), ShouldEqual, exportSkillDoc("alpha"))
				So(readFile(t, filepath.Join(out, "skills", "beta", "SKILL.md")), ShouldEqual, exportSkillDoc("beta"))

				Convey("And a nested skill file is copied along", func() {
					So(readFile(t, filepath.Join(out, "skills", "gamma", "SKILL.md")), ShouldEqual, exportSkillDoc("gamma"))
					So(readFile(t, filepath.Join(out, "skills", "gamma", "notes.md")), ShouldEqual, "nested\n")
				})

				var manifest map[string]json.RawMessage

				So(json.Unmarshal([]byte(readFile(t, filepath.Join(out, "plugin.json"))), &manifest), ShouldBeNil)
				So(slices.Sorted(maps.Keys(manifest)), ShouldResemble, []string{
					"$schema", "description", "keywords", "license", "name", "version",
				})

				var document struct {
					Schema     string                    `json:"$schema"`
					MCPServers map[string]map[string]any `json:"mcpServers"`
				}

				So(json.Unmarshal([]byte(readFile(t, filepath.Join(out, "mcp.json"))), &document), ShouldBeNil)
				So(document.Schema, ShouldEqual, agentplugins.MCPSchemaURL)
				So(slices.Sorted(maps.Keys(document.MCPServers)), ShouldResemble, []string{"fs", "remote"})
				So(document.MCPServers["fs"]["command"], ShouldEqual, "npx")
				So(document.MCPServers["fs"]["env"], ShouldResemble, map[string]any{"CACHE_DIR": "${PLUGIN_ROOT}/cache"})
				So(document.MCPServers["remote"]["type"], ShouldEqual, "streamable-http")
				So(document.MCPServers["remote"]["headers"], ShouldResemble, map[string]any{"Authorization": "Bearer ${API_KEY}"})

				Convey("And the foreign file in the output directory is untouched", func() {
					So(readFile(t, foreign), ShouldEqual, "mine\n")
				})
			})
		})

		Convey("When the export runs again", func() {
			before := snapshotTree(t, out)

			_, _, err := gwsRunSplit(t, "export", "agent-plugins", "--out", out)

			Convey("Then the second run is byte-for-byte stable", func() {
				So(err, ShouldBeNil)

				after := snapshotTree(t, out)

				So(slices.Sorted(maps.Keys(after)), ShouldResemble, slices.Sorted(maps.Keys(before)))

				for name, data := range before {
					So(after[name], ShouldEqual, data)
				}
			})
		})

		Convey("When a skill disappears from the canon and the export runs again", func() {
			So(os.RemoveAll(filepath.Join(vaultDir, "skills", "gamma")), ShouldBeNil)

			_, stderr, err := gwsRunSplit(t, "export", "agent-plugins", "--out", out)

			Convey("Then the stale discovery file is pruned, the tree file stays and the report warns", func() {
				So(err, ShouldBeNil)
				So(stderr, ShouldContainSubstring, "removed stale skills/gamma/SKILL.md (no longer rendered)")
				So(stderr, ShouldContainSubstring, "skills/gamma is no longer rendered but keeps foreign files")

				_, statErr := os.Stat(filepath.Join(out, "skills", "gamma", "SKILL.md"))
				So(errors.Is(statErr, fs.ErrNotExist), ShouldBeTrue)

				So(readFile(t, filepath.Join(out, "skills", "gamma", "notes.md")), ShouldEqual, "nested\n")
			})
		})

		Convey("When --out is missing", func() {
			stdout, _, err := gwsRunSplit(t, "export", "agent-plugins")

			Convey("Then the command refuses to guess a directory", func() {
				So(err, ShouldBeError)
				So(strings.Contains(err.Error(), "--out is required"), ShouldBeTrue)
				So(stdout, ShouldBeEmpty)
			})
		})
	})
}
