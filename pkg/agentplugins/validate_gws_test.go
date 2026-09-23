package agentplugins_test

import (
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agentplugins"
)

// renderedPackage renders a fresh package into a temp directory and returns
// the directory, so a mutation in one case cannot leak into another.
func renderedPackage(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	if err := agentplugins.Write(dir, renderFull(t).Files); err != nil {
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
				So(err.Error(), ShouldContainSubstring, "needs an absolute http(s) url")
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
