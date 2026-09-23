package agentplugins_test

import (
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agentplugins"
)

// renderedPackage renders a fresh package into a temp directory and returns
// the directory, so a mutation in one case cannot leak into another.
func renderedPackage(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	if _, err := agentplugins.Write(dir, renderFull(t).Files); err != nil {
		t.Fatalf("write: %v", err)
	}

	return dir
}

//nolint:funlen // one Convey per schema violation keeps the cases together
func TestValidatePackage(t *testing.T) {
	Convey("Given a rendered package", t, func() {
		dir := renderedPackage(t)

		Convey("When the manifest is closed", func() {
			Convey("Then the package validates", func() {
				So(agentplugins.Validate(dir), ShouldBeNil)
			})
		})

		Convey("When the manifest carries an unknown key", func() {
			write(t, filepath.Join(dir, agentplugins.ManifestFile),
				`{"$schema":"`+agentplugins.SchemaURL+`","name":"n","vendor":"x"}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, `unknown key "vendor"`)
			})
		})

		Convey("When the manifest misses the name", func() {
			write(t, filepath.Join(dir, agentplugins.ManifestFile),
				`{"$schema":"`+agentplugins.SchemaURL+`"}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, `"name" must match the plugin name pattern`)
			})
		})

		Convey("When the manifest name is not a slug", func() {
			write(t, filepath.Join(dir, agentplugins.ManifestFile),
				`{"$schema":"`+agentplugins.SchemaURL+`","name":"Bad Name"}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "plugin name pattern")
			})
		})

		Convey("When the manifest schema is not the v1 const", func() {
			write(t, filepath.Join(dir, agentplugins.ManifestFile),
				`{"$schema":"https://example.com/other.json","name":"n"}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, agentplugins.SchemaURL)
			})
		})

		Convey("When the manifest has no version", func() {
			write(t, filepath.Join(dir, agentplugins.ManifestFile),
				`{"$schema":"`+agentplugins.SchemaURL+`","name":"n"}`)

			Convey("Then the package still validates: version is optional", func() {
				So(agentplugins.Validate(dir), ShouldBeNil)
			})
		})

		Convey("When a skill directory has no SKILL.md", func() {
			write(t, filepath.Join(dir, agentplugins.SkillsDir, "broken", "notes.md"), "nested\n")

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "the skill has no regular SKILL.md")
			})
		})

		Convey("When a skills entry is not a directory", func() {
			write(t, filepath.Join(dir, agentplugins.SkillsDir, "loose.md"), "loose\n")

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "is not a skill directory")
			})
		})

		Convey("When mcp.json is a bare server map", func() {
			write(t, filepath.Join(dir, agentplugins.MCPFile), `{"fs":{"type":"stdio","command":"npx"}}`)

			Convey("Then the validator rejects it: the wrapper is required", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, `unknown top-level key "fs"`)
			})
		})

		Convey("When mcp.json carries an unknown top-level key", func() {
			write(t, filepath.Join(dir, agentplugins.MCPFile),
				`{"$schema":"`+agentplugins.MCPSchemaURL+`","mcpServers":{},"servers":{}}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, `unknown top-level key "servers"`)
			})
		})

		Convey("When a server carries an unknown key", func() {
			write(t, filepath.Join(dir, agentplugins.MCPFile),
				`{"$schema":"`+agentplugins.MCPSchemaURL+`","mcpServers":{"fs":{"type":"stdio","command":"npx","transport":"stdio"}}}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, `unknown key "transport"`)
			})
		})

		Convey("When a stdio server carries a url", func() {
			write(t, filepath.Join(dir, agentplugins.MCPFile),
				`{"$schema":"`+agentplugins.MCPSchemaURL+`","mcpServers":{"fs":{"type":"stdio","command":"npx","url":"https://example.com"}}}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "is stdio and carries a url")
			})
		})

		Convey("When a stdio server has an empty command", func() {
			write(t, filepath.Join(dir, agentplugins.MCPFile),
				`{"$schema":"`+agentplugins.MCPSchemaURL+`","mcpServers":{"fs":{"type":"stdio","command":""}}}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "needs a non-empty command")
			})
		})

		Convey("When a remote server has a relative url", func() {
			write(t, filepath.Join(dir, agentplugins.MCPFile),
				`{"$schema":"`+agentplugins.MCPSchemaURL+`","mcpServers":{"fs":{"type":"streamable-http","url":"/mcp"}}}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "needs an https url")
			})
		})

		Convey("When a server uses a transport outside the union", func() {
			write(t, filepath.Join(dir, agentplugins.MCPFile),
				`{"$schema":"`+agentplugins.MCPSchemaURL+`","mcpServers":{"fs":{"type":"ws","url":"wss://example.com"}}}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "outside the v1 union")
			})
		})

		Convey("When an env key uses a reserved name", func() {
			write(t, filepath.Join(dir, agentplugins.MCPFile),
				`{"$schema":"`+agentplugins.MCPSchemaURL+`","mcpServers":{"fs":{"type":"stdio","command":"npx","env":{"PLUGIN_ROOT":"x"}}}}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "is a reserved name")
			})
		})

		Convey("When args carry a non-string", func() {
			write(t, filepath.Join(dir, agentplugins.MCPFile),
				`{"$schema":"`+agentplugins.MCPSchemaURL+`","mcpServers":{"fs":{"type":"stdio","command":"npx","args":[1]}}}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "must be an array of strings")
			})
		})
	})
}

//nolint:funlen // one Convey per schema edge keeps the cases together
func TestValidateSchemaEdges(t *testing.T) {
	writeManifest := func(t *testing.T, dir, name string) {
		t.Helper()

		write(t, filepath.Join(dir, agentplugins.ManifestFile),
			`{"$schema":"`+agentplugins.SchemaURL+`","name":"`+name+`"}`)
	}

	writeMCP := func(t *testing.T, dir, server string) {
		t.Helper()

		write(t, filepath.Join(dir, agentplugins.MCPFile),
			`{"$schema":"`+agentplugins.MCPSchemaURL+`","mcpServers":{"fs":`+server+`}}`)
	}

	Convey("Given a rendered package", t, func() {
		dir := renderedPackage(t)

		Convey("When the plugin name uses a dot", func() {
			writeManifest(t, dir, "acme.tools")

			Convey("Then the validator accepts it: dots belong to the §5.5 charset", func() {
				So(agentplugins.Validate(dir), ShouldBeNil)
			})
		})

		Convey("When the plugin name carries a double hyphen", func() {
			writeManifest(t, dir, "has--double")

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "plugin name pattern")
			})
		})

		Convey("When the plugin name carries a double dot", func() {
			writeManifest(t, dir, "has..dots")

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "plugin name pattern")
			})
		})

		Convey("When the plugin name is 64 characters", func() {
			writeManifest(t, dir, strings.Repeat("a", 64))

			Convey("Then the validator accepts it", func() {
				So(agentplugins.Validate(dir), ShouldBeNil)
			})
		})

		Convey("When the plugin name is 65 characters", func() {
			writeManifest(t, dir, strings.Repeat("a", 65))

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "plugin name pattern")
			})
		})

		Convey("When mcpServers is null", func() {
			write(t, filepath.Join(dir, agentplugins.MCPFile),
				`{"$schema":"`+agentplugins.MCPSchemaURL+`","mcpServers":null}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "must be an object of servers")
			})
		})

		Convey("When args is null", func() {
			writeMCP(t, dir, `{"type":"stdio","command":"npx","args":null}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "args must be an array of strings")
			})
		})

		Convey("When env is null", func() {
			writeMCP(t, dir, `{"type":"stdio","command":"npx","env":null}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "env must be an object of strings")
			})
		})

		Convey("When headers is null", func() {
			writeMCP(t, dir, `{"type":"streamable-http","url":"https://example.com/mcp","headers":null}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "headers must be an object of strings")
			})
		})

		Convey("When a header uses the reserved name", func() {
			writeMCP(t, dir, `{"type":"streamable-http","url":"https://example.com/mcp","headers":{"PLUGIN_ROOT":"x"}}`)

			Convey("Then the validator accepts it: the reservation covers env only", func() {
				So(agentplugins.Validate(dir), ShouldBeNil)
			})
		})

		Convey("When a remote server uses plain http on a non-loopback host", func() {
			writeMCP(t, dir, `{"type":"streamable-http","url":"http://example.com/mcp"}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "needs an https url")
			})
		})

		Convey("When a remote server uses http on loopback", func() {
			writeMCP(t, dir, `{"type":"streamable-http","url":"http://127.0.0.1:8080/mcp"}`)

			Convey("Then the validator accepts it", func() {
				So(agentplugins.Validate(dir), ShouldBeNil)
			})
		})

		Convey("When cwd is an absolute path", func() {
			writeMCP(t, dir, `{"type":"stdio","command":"npx","cwd":"/tmp"}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "cwd must be a ./-relative")
			})
		})

		Convey("When cwd climbs above the package root", func() {
			writeMCP(t, dir, `{"type":"stdio","command":"npx","cwd":"${PLUGIN_ROOT}/../outside"}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "cwd must be a ./-relative")
			})
		})

		Convey("When cwd is a ${PLUGIN_ROOT} path", func() {
			writeMCP(t, dir, `{"type":"stdio","command":"npx","cwd":"${PLUGIN_ROOT}/work"}`)

			Convey("Then the validator accepts it", func() {
				So(agentplugins.Validate(dir), ShouldBeNil)
			})
		})

		Convey("When cwd is a ./-relative path", func() {
			writeMCP(t, dir, `{"type":"stdio","command":"npx","cwd":"./work"}`)

			Convey("Then the validator accepts it", func() {
				So(agentplugins.Validate(dir), ShouldBeNil)
			})
		})

		Convey("When cwd is a ${PLUGIN_DATA} path", func() {
			writeMCP(t, dir, `{"type":"stdio","command":"npx","cwd":"${PLUGIN_DATA}/work"}`)

			Convey("Then the validator accepts it", func() {
				So(agentplugins.Validate(dir), ShouldBeNil)
			})
		})

		Convey("When a ./-relative cwd climbs above the package root", func() {
			writeMCP(t, dir, `{"type":"stdio","command":"npx","cwd":"./../outside"}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "cwd must be a ./-relative")
			})
		})

		Convey("When a ${PLUGIN_DATA} cwd climbs above its root", func() {
			writeMCP(t, dir, `{"type":"stdio","command":"npx","cwd":"${PLUGIN_DATA}/../outside"}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "cwd must be a ./-relative")
			})
		})

		Convey("When a remote url carries user-info", func() {
			//nolint:gosec // G101: a synthetic user-info url, not a credential
			writeMCP(t, dir, `{"type":"streamable-http","url":"https://user:secret@example.com/mcp"}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "without user-info or a fragment")
			})
		})

		Convey("When a remote url carries a fragment", func() {
			writeMCP(t, dir, `{"type":"streamable-http","url":"https://example.com/mcp#frag"}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "without user-info or a fragment")
			})
		})

		Convey("When a remote server uses http on a loopback IP", func() {
			writeMCP(t, dir, `{"type":"streamable-http","url":"http://127.5.5.5:9000/mcp"}`)

			Convey("Then the validator accepts it", func() {
				So(agentplugins.Validate(dir), ShouldBeNil)
			})
		})

		Convey("When a remote server carries args", func() {
			writeMCP(t, dir, `{"type":"streamable-http","url":"https://example.com/mcp","args":["x"]}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "is streamable-http and carries a args")
			})
		})

		Convey("When a remote server carries env", func() {
			writeMCP(t, dir, `{"type":"streamable-http","url":"https://example.com/mcp","env":{"A":"b"}}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "is streamable-http and carries a env")
			})
		})

		Convey("When a remote server carries cwd", func() {
			writeMCP(t, dir, `{"type":"streamable-http","url":"https://example.com/mcp","cwd":"${PLUGIN_ROOT}/work"}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "is streamable-http and carries a cwd")
			})
		})

		Convey("When a stdio server carries headers", func() {
			writeMCP(t, dir, `{"type":"stdio","command":"npx","headers":{"X-Token":"y"}}`)

			Convey("Then the validator rejects it", func() {
				err := agentplugins.Validate(dir)
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "is stdio and carries a headers")
			})
		})
	})
}
