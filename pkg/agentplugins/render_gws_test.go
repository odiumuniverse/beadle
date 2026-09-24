package agentplugins_test

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
	"github.com/odiumuniverse/beadle/pkg/mcp"
	"github.com/odiumuniverse/beadle/pkg/skill"
)

func skillDoc(name string) string {
	return "---\nname: " + name + "\ndescription: test skill\n---\n\n# " + name + "\n"
}

func canonSkills() map[string]skill.Tree {
	return map[string]skill.Tree{
		"alpha": {agentplugins.SkillFile: []byte(skillDoc("alpha"))},
		"beta":  {agentplugins.SkillFile: []byte(skillDoc("beta")), "scripts/run.sh": []byte("#!/bin/sh\n")},
		"gamma": {agentplugins.SkillFile: []byte(skillDoc("gamma"))},
		"empty": {agentplugins.SkillFile: []byte("   \n")},
		"Bad Name": {
			agentplugins.SkillFile: []byte(skillDoc("bad")),
		},
	}
}

func canonServers() mcp.Servers {
	return mcp.Servers{
		"fs": {
			Transport: mcp.TransportStdio,
			Command:   []string{"npx", "-y", "mcp-fs", "${PLUGIN_ROOT}/data"},
			Env:       map[string]string{"CACHE_DIR": "${PLUGIN_ROOT}/cache"},
		},
		"rel": {Transport: mcp.TransportStdio, Command: []string{"./bin/server", "--port=1"}},
		"remote": {
			Transport: mcp.TransportHTTP,
			URL:       "https://example.com/mcp",
			Headers:   map[string]string{"Authorization": "Bearer ${API_KEY}"},
		},
		"sse":         {Transport: mcp.TransportSSE, URL: "https://example.com/sse"},
		"ws":          {Transport: mcp.TransportWS, URL: "wss://example.com/mcp"},
		"escape":      {Transport: mcp.TransportStdio, Command: []string{"run", "${PLUGIN_ROOT}/../outside"}},
		"spaced":      {Transport: mcp.TransportStdio, Command: []string{"run", "${PLUGIN_ROOT}/a b/../../x"}},
		"flag":        {Transport: mcp.TransportStdio, Command: []string{"run", "--dir=../outside"}},
		"bareRel":     {Transport: mcp.TransportStdio, Command: []string{"run", "foo/../../x"}},
		"absolute":    {Transport: mcp.TransportStdio, Command: []string{"/usr/bin/x"}},
		"interpolate": {Transport: mcp.TransportStdio, Command: []string{"${PLUGIN_ROOT}/bin/x"}},
		"contradict":  {Transport: mcp.TransportHTTP, URL: "https://example.com/mcp", Command: []string{"npx"}},
		"stdioURL":    {Transport: mcp.TransportStdio, URL: "https://example.com/mcp"},
		"empty":       {},
		"httpNoURL":   {Transport: mcp.TransportHTTP, Command: []string{"npx"}},
		"plainHTTP":   {Transport: mcp.TransportHTTP, URL: "http://example.com/mcp"},
		"loopback":    {Transport: mcp.TransportHTTP, URL: "http://127.0.0.1:8080/mcp"},
		//nolint:gosec // G101: a synthetic user-info url, not a credential
		"userInfo":  {Transport: mcp.TransportHTTP, URL: "https://user:secret@example.com/mcp"},
		"fragment":  {Transport: mcp.TransportHTTP, URL: "https://example.com/mcp#frag"},
		"spacedCmd": {Transport: mcp.TransportStdio, Command: []string{"node script.js"}},
		"tokenArgs": {Transport: mcp.TransportStdio, Command: []string{"npx", "--token=${API_TOKEN}"}},
		"tokenURL":  {Transport: mcp.TransportHTTP, URL: "https://example.com/${TENANT}/mcp"},
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// snapshotDir maps every regular file under dir to its bytes.
func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()

	out := map[string]string{}

	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}

		data, readErr := os.ReadFile(path) //nolint:gosec // G304: tests read their own temp files
		if readErr != nil {
			return readErr
		}

		out[rel] = string(data)

		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", dir, err)
	}

	return out
}

func renderFull(t *testing.T) agentplugins.Package {
	t.Helper()

	pkg, err := agentplugins.Render(canonSkills(), canonServers(), agentplugins.Options{
		Name:        "beadle-canon",
		Version:     "1.2.3",
		Description: "The beadle canon: portable skills and MCP servers",
		License:     "MIT",
		Keywords:    []string{"beadle", "skills", "mcp"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	return pkg
}

func TestRenderPackage(t *testing.T) {
	Convey("Given a canon with skills and MCP servers", t, func() {
		pkg := renderFull(t)

		Convey("When the package is rendered", func() {
			Convey("Then the skill trees are copied and only portable servers are in it", func() {
				So(slices.Sorted(maps.Keys(pkg.Files)), ShouldResemble, []string{
					agentplugins.MCPFile,
					agentplugins.ManifestFile,
					"skills/alpha/SKILL.md",
					"skills/beta/SKILL.md",
					"skills/beta/scripts/run.sh",
					"skills/gamma/SKILL.md",
				})

				So(pkg.Skills.Rendered, ShouldEqual, 3)
				So(pkg.Skills.Skipped, ShouldEqual, 2)
				So(pkg.MCP.Rendered, ShouldEqual, 7)
				So(pkg.MCP.Skipped, ShouldEqual, 15)

				warnings := strings.Join(pkg.Warnings, "\n")
				So(warnings, ShouldContainSubstring, "skill empty is skipped: the skill has no SKILL.md body")
				So(warnings, ShouldContainSubstring, "skill Bad Name is skipped: the name is not a valid skill slug")
				So(warnings, ShouldContainSubstring, "mcp ws is skipped: transport ws is not part of the Agent Plugins v1 union")
				So(warnings, ShouldContainSubstring, "mcp escape is skipped: a ${PLUGIN_ROOT} or relative reference escapes the package root")
				So(warnings, ShouldContainSubstring, "mcp spaced is skipped: a ${PLUGIN_ROOT} or relative reference escapes the package root")
				So(warnings, ShouldContainSubstring, "mcp flag is skipped: a ${PLUGIN_ROOT} or relative reference escapes the package root")
				So(warnings, ShouldContainSubstring, "mcp bareRel is skipped: a ${PLUGIN_ROOT} or relative reference escapes the package root")
				So(warnings, ShouldContainSubstring, "mcp absolute is skipped: the command \"/usr/bin/x\" is neither a bare name nor a ./relative path")
				So(warnings, ShouldContainSubstring, "mcp interpolate is skipped: the command \"${PLUGIN_ROOT}/bin/x\" is neither a bare name nor a ./relative path")
				So(warnings, ShouldContainSubstring, "mcp spacedCmd is skipped: the command \"node script.js\" is neither a bare name nor a ./relative path")
				So(warnings, ShouldContainSubstring, "mcp contradict is skipped: the server carries both a command and a url")
				So(warnings, ShouldContainSubstring, "mcp stdioURL is skipped: the stdio transport carries a url")
				So(warnings, ShouldContainSubstring, "mcp httpNoURL is skipped: the http transport carries no url")
				So(warnings, ShouldContainSubstring, "mcp plainHTTP is skipped: the url \"http://example.com/mcp\" must be https without user-info or a fragment (http is allowed on loopback only)")
				So(warnings, ShouldContainSubstring, "mcp userInfo is skipped: the url \"https://user:secret@example.com/mcp\" must be https without user-info or a fragment (http is allowed on loopback only)")
				So(warnings, ShouldContainSubstring, "mcp fragment is skipped: the url \"https://example.com/mcp#frag\" must be https without user-info or a fragment (http is allowed on loopback only)")
				So(warnings, ShouldContainSubstring, "mcp empty is skipped: the server has no command")
				So(warnings, ShouldContainSubstring, "mcp remote: headers Authorization references ${API_KEY}; clients do not expand it")
				So(warnings, ShouldContainSubstring, "mcp tokenArgs: args [0] references ${API_TOKEN}; clients do not expand it")
				So(warnings, ShouldContainSubstring, "mcp tokenURL: url references ${TENANT}; clients do not expand it")
			})

			Convey("Then the manifest carries exactly the closed schema keys", func() {
				var manifest map[string]json.RawMessage

				So(json.Unmarshal(pkg.Files[agentplugins.ManifestFile], &manifest), ShouldBeNil)

				So(slices.Sorted(maps.Keys(manifest)), ShouldResemble, []string{
					"$schema", "description", "keywords", "license", "name", "version",
				})

				var name, version, schema string

				So(json.Unmarshal(manifest["name"], &name), ShouldBeNil)
				So(json.Unmarshal(manifest["version"], &version), ShouldBeNil)
				So(json.Unmarshal(manifest["$schema"], &schema), ShouldBeNil)

				So(name, ShouldEqual, "beadle-canon")
				So(version, ShouldEqual, "1.2.3")
				So(schema, ShouldEqual, agentplugins.SchemaURL)
			})

			Convey("Then mcp.json carries the wrapper and keeps references unexpanded", func() {
				var document struct {
					Schema     string                    `json:"$schema"`
					MCPServers map[string]map[string]any `json:"mcpServers"`
				}

				So(json.Unmarshal(pkg.Files[agentplugins.MCPFile], &document), ShouldBeNil)
				So(document.Schema, ShouldEqual, agentplugins.MCPSchemaURL)

				So(slices.Sorted(maps.Keys(document.MCPServers)), ShouldResemble,
					[]string{"fs", "loopback", "rel", "remote", "sse", "tokenArgs", "tokenURL"})

				fsServer := document.MCPServers["fs"]
				So(fsServer["type"], ShouldEqual, "stdio")
				So(fsServer["command"], ShouldEqual, "npx")
				So(fsServer["args"], ShouldResemble, []any{"-y", "mcp-fs", "${PLUGIN_ROOT}/data"})
				So(fsServer["env"], ShouldResemble, map[string]any{"CACHE_DIR": "${PLUGIN_ROOT}/cache"})

				relServer := document.MCPServers["rel"]
				So(relServer["command"], ShouldEqual, "./bin/server")
				So(relServer["args"], ShouldResemble, []any{"--port=1"})

				remote := document.MCPServers["remote"]
				So(remote["type"], ShouldEqual, "streamable-http")
				So(remote["url"], ShouldEqual, "https://example.com/mcp")
				So(remote["headers"], ShouldResemble, map[string]any{"Authorization": "Bearer ${API_KEY}"})

				So(document.MCPServers["sse"]["type"], ShouldEqual, "sse")
			})
		})
	})
}

// TestRenderSecretRefsArePortable pins the export contract of a canon secret:
// the whole-value {secret:NAME} reference becomes the portable ${NAME} form,
// it is announced once, and the client-managed warning for the same value is
// suppressed — the placeholder is not a plaintext reference the client fails
// to expand.
//
//nolint:gosec // G101: the fixture values are synthetic ${NAME} references, not credentials
func TestRenderSecretRefsArePortable(t *testing.T) {
	Convey("Given two canon servers carrying the same {secret:NAME} reference", t, func() {
		servers := mcp.Servers{
			"api": {
				Transport: mcp.TransportStdio,
				Command:   []string{"npx", "-y", "mcp-api"},
				Env:       map[string]string{"TOKEN": "{secret:GH_TOKEN}", "PLAIN": "${API_KEY}"},
			},
			"api2": {
				Transport: mcp.TransportStdio,
				Command:   []string{"npx", "-y", "mcp-api2"},
				Env:       map[string]string{"TOKEN": "{secret:GH_TOKEN}"},
			},
		}

		pkg, err := agentplugins.Render(nil, servers, agentplugins.Options{Name: "beadle-canon", Version: "1.0.0"})
		So(err, ShouldBeNil)

		Convey("When the package is rendered", func() {
			Convey("Then the reference is portable and warned once", func() {
				var document struct {
					MCPServers map[string]map[string]any `json:"mcpServers"`
				}

				So(json.Unmarshal(pkg.Files[agentplugins.MCPFile], &document), ShouldBeNil)
				So(document.MCPServers["api"]["env"], ShouldResemble, map[string]any{"TOKEN": "${GH_TOKEN}", "PLAIN": "${API_KEY}"})
				So(document.MCPServers["api2"]["env"], ShouldResemble, map[string]any{"TOKEN": "${GH_TOKEN}"})

				warnings := strings.Join(pkg.Warnings, "\n")

				So(strings.Count(warnings, "secret GH_TOKEN is exported as ${GH_TOKEN}; the installing host supplies the value"), ShouldEqual, 1)
				So(warnings, ShouldNotContainSubstring, "TOKEN references ${GH_TOKEN}")
				So(warnings, ShouldContainSubstring, "mcp api: env PLAIN references ${API_KEY}; clients do not expand it")
			})
		})
	})
}

func TestRenderDeterministicAndIdempotent(t *testing.T) {
	Convey("Given a rendered package", t, func() {
		first := renderFull(t)

		dir := t.TempDir()

		_, err := agentplugins.Write(dir, first.Files)
		So(err, ShouldBeNil)
		So(agentplugins.Validate(dir), ShouldBeNil)

		Convey("When the render runs again", func() {
			second := renderFull(t)

			Convey("Then the bytes are identical", func() {
				So(second.Files, ShouldResemble, first.Files)
			})
		})

		Convey("When the canon shrinks and the package is written again", func() {
			write(t, filepath.Join(dir, "KEEP.md"), "mine\n")
			write(t, filepath.Join(dir, "skills", "foreign.md"), "foreign\n")
			write(t, filepath.Join(dir, "skills", "old", "SKILL.md"), skillDoc("old"))
			write(t, filepath.Join(dir, "skills", "old", "notes.md"), "stale tree\n")

			So(os.MkdirAll(filepath.Join(dir, "skills", "emptydir"), 0o750), ShouldBeNil)

			shrunk := agentplugins.Package{Files: agentplugins.Files{
				agentplugins.ManifestFile: first.Files[agentplugins.ManifestFile],
				"skills/alpha/SKILL.md":   first.Files["skills/alpha/SKILL.md"],
			}}

			report, err := agentplugins.Write(dir, shrunk.Files)
			So(err, ShouldBeNil)

			Convey("Then only provably beadle-owned paths are pruned and foreign files survive", func() {
				names := slices.Sorted(maps.Keys(snapshotDir(t, dir)))
				So(names, ShouldResemble, []string{
					"KEEP.md",
					agentplugins.ManifestFile,
					"skills/alpha/SKILL.md",
					"skills/beta/scripts/run.sh", // a stale tree file of a kept skill stays
					"skills/foreign.md",          // a loose foreign file stays
					"skills/old/notes.md",        // the stale discovery file went, its directory stays
				})

				So(readFile(t, filepath.Join(dir, "KEEP.md")), ShouldEqual, "mine\n")
				So(readFile(t, filepath.Join(dir, "skills", "foreign.md")), ShouldEqual, "foreign\n")
				So(readFile(t, filepath.Join(dir, "skills", "old", "notes.md")), ShouldEqual, "stale tree\n")

				_, err := os.Stat(filepath.Join(dir, "skills", "old", "SKILL.md"))
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)

				_, err = os.Stat(filepath.Join(dir, "skills", "gamma"))
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)

				_, err = os.Stat(filepath.Join(dir, "skills", "emptydir"))
				So(err, ShouldBeNil)

				// The report names the pruned discovery files and the stale
				// directory that kept foreign files: the package does not
				// validate until the user cleans it (never deleting unknown
				// files is the safe direction).
				So(report.Pruned, ShouldContain, "skills/old/SKILL.md")
				So(report.Pruned, ShouldContain, "skills/gamma/SKILL.md")
				So(report.Pruned, ShouldContain, agentplugins.MCPFile)
				So(report.Leftover, ShouldResemble, []string{"skills/beta", "skills/old"})

				err = agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "the skill has no regular SKILL.md")
			})
		})

		Convey("When the package is written over a directory with foreign files", func() {
			before := snapshotDir(t, dir)

			write(t, filepath.Join(dir, "KEEP.md"), "mine\n")

			_, err := agentplugins.Write(dir, first.Files)
			So(err, ShouldBeNil)

			Convey("Then beadle's files are unchanged and the foreign file survives", func() {
				after := snapshotDir(t, dir)

				So(after["KEEP.md"], ShouldEqual, "mine\n")

				for name, data := range before {
					So(after[name], ShouldEqual, data)
				}
			})
		})
	})
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: tests read their own temp files
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(data)
}

func TestWriteRefusesSymlinkEscape(t *testing.T) {
	Convey("Given an output directory with a symlink pointing outside", t, func() {
		dir := t.TempDir()
		outside := t.TempDir()

		So(os.Symlink(outside, filepath.Join(dir, "link")), ShouldBeNil)

		Convey("When a package path would go through the symlink", func() {
			_, err := agentplugins.Write(dir, agentplugins.Files{
				"link/evil/nested.md": []byte("evil\n"),
			})

			Convey("Then the write is refused and nothing is created outside", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "resolves outside the output directory")

				_, statErr := os.Stat(filepath.Join(outside, "evil"))
				So(errors.Is(statErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}
