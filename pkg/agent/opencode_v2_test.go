package agent_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"github.com/tailscale/hujson"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

func jsonDoc(t *testing.T, text string) map[string]any {
	t.Helper()

	standard, err := hujson.Standardize([]byte(text))
	if err != nil {
		t.Fatalf("standardize json: %v", err)
	}

	var doc map[string]any

	if err := json.Unmarshal(standard, &doc); err != nil {
		t.Fatalf("parse json: %v", err)
	}

	return doc
}

func jsonMap(t *testing.T, doc map[string]any, keys ...string) map[string]any {
	t.Helper()

	current := doc

	for _, key := range keys {
		next, ok := current[key].(map[string]any)
		if !ok {
			t.Fatalf("json %q is not an object", key)
		}

		current = next
	}

	return current
}

func jsonList(t *testing.T, doc map[string]any, key string) []any {
	t.Helper()

	list, ok := doc[key].([]any)
	if !ok {
		t.Fatalf("json %q is not an array", key)
	}

	return list
}

func openCodeSurface(t *testing.T, home string, k kind.ID) agent.Surface {
	t.Helper()

	return surfaceOf(t, agent.OpenCode(home, t.TempDir()), k)
}

func TestOpenCodeV2MCPRoundTrip(t *testing.T) {
	Convey("Given an OpenCode config in the V2 MCP dialect", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{
  // keep me
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "servers": {
      "alpha": {"type": "local", "command": ["npx", "alpha"], "disabled": true}
    }
  }
}
`)

		surface := openCodeSurface(t, home, kind.MCP)

		snap, err := surface.Read(t.Context())
		So(err, ShouldBeNil)
		So(snap.Items, ShouldHaveLength, 1)
		So(server(t, snap.Items["alpha"]).Command, ShouldResemble, []string{"npx", "alpha"})

		Convey("When the canon updates the server", func() {
			desired := kind.Items{
				"alpha": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"npx", "alpha2"}}),
			}

			So(surface.Write(t.Context(), desired), ShouldBeNil)

			Convey("Then it stays under mcp.servers, foreign fields and comments survive", func() {
				text := readFile(t, path)
				So(text, ShouldContainSubstring, "keep me")
				So(text, ShouldContainSubstring, "alpha2")

				doc := jsonDoc(t, text)
				So(jsonMap(t, doc, "mcp", "servers"), ShouldHaveLength, 1)
				So(jsonMap(t, doc, "mcp", "servers", "alpha")["disabled"], ShouldEqual, true)

				before := text

				So(surface.Write(t.Context(), desired), ShouldBeNil)
				So(readFile(t, path), ShouldEqual, before)
			})
		})
	})
}

func TestOpenCodeV2MCPRemoteKeepsForeignFields(t *testing.T) {
	Convey("Given a V2 remote server with OAuth and timeout", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"mcp":{"servers":{"beta":{"type":"remote","url":"https://mcp.example/mcp","oauth":false,"timeout":{"catalog":30000}}}}}`)

		surface := openCodeSurface(t, home, kind.MCP)

		snap, err := surface.Read(t.Context())
		So(err, ShouldBeNil)
		So(server(t, snap.Items["beta"]).Transport, ShouldEqual, mcp.TransportHTTP)

		desired := kind.Items{
			"beta": mcp.Encode(mcp.Server{
				Transport: mcp.TransportHTTP,
				URL:       "https://mcp.example/mcp",
				Headers:   map[string]string{"A": "1"},
			}),
		}

		So(surface.Write(t.Context(), desired), ShouldBeNil)

		text := readFile(t, path)
		doc := jsonDoc(t, text)
		beta := jsonMap(t, doc, "mcp", "servers", "beta")
		So(beta["oauth"], ShouldEqual, false)
		So(beta["headers"], ShouldResemble, map[string]any{"A": "1"})
		So(jsonMap(t, beta, "timeout")["catalog"], ShouldEqual, float64(30000))
	})
}

func TestOpenCodeMixedMCPRead(t *testing.T) {
	Convey("Given a mixed OpenCode MCP config", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"mcp":{"servers":{"both":{"type":"local","command":["native-both"]},"dup":{"type":"local"}},"both":{"type":"local","command":["v1-both"]},"dup":{"type":"local","command":["v1-dup"]},"timeout":{"type":"local","command":["timeout-mcp"]}}}`)

		snap := snapshot(t, agent.OpenCode(home, t.TempDir()), kind.MCP)

		Convey("Then both layers are visible and a valid native entry wins", func() {
			So(snap.Items, ShouldHaveLength, 3)
			So(server(t, snap.Items["both"]).Command, ShouldResemble, []string{"native-both"})
			So(server(t, snap.Items["dup"]).Command, ShouldResemble, []string{"v1-dup"})
			So(server(t, snap.Items["timeout"]).Command, ShouldResemble, []string{"timeout-mcp"})
		})
	})
}

func TestOpenCodeMixedMCPWrite(t *testing.T) {
	Convey("Given a v1-only server next to a native container", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"mcp":{"servers":{"b":{"type":"local","command":["b-mcp"]}},"a":{"type":"local","command":["a-mcp"]}}}`)

		surface := openCodeSurface(t, home, kind.MCP)
		desired := kind.Items{
			"a": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"a2-mcp"}}),
			"b": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"b-mcp"}}),
		}

		So(surface.Write(t.Context(), desired), ShouldBeNil)

		Convey("Then the v1 server is updated in place and the native one stays native", func() {
			doc := jsonDoc(t, readFile(t, path))
			So(jsonMap(t, doc, "mcp", "a")["command"], ShouldResemble, []any{"a2-mcp"})
			So(jsonMap(t, doc, "mcp", "servers"), ShouldHaveLength, 1)
			So(jsonMap(t, doc, "mcp", "servers", "b")["command"], ShouldResemble, []any{"b-mcp"})
		})
	})
}

func TestOpenCodeMixedMCPDedupeAndDelete(t *testing.T) {
	Convey("Given the same server in both MCP layers", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"mcp":{"servers":{"x":{"type":"local","command":["n-mcp"]}},"x":{"type":"local","command":["v-mcp"]}}}`)

		surface := openCodeSurface(t, home, kind.MCP)

		same := kind.Items{
			"x": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"n-mcp"}}),
		}

		Convey("When the canon matches the native copy, the duplicate stays untouched", func() {
			before := readFile(t, path)
			So(surface.Write(t.Context(), same), ShouldBeNil)
			So(readFile(t, path), ShouldEqual, before)
		})

		Convey("When the canon changes, the native copy is updated and the v1 duplicate is dropped", func() {
			changed := kind.Items{
				"x": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"n2-mcp"}}),
			}

			So(surface.Write(t.Context(), changed), ShouldBeNil)

			doc := jsonDoc(t, readFile(t, path))
			So(jsonMap(t, doc, "mcp", "servers", "x")["command"], ShouldResemble, []any{"n2-mcp"})
			So(jsonMap(t, doc, "mcp"), ShouldNotContainKey, "x")
		})
	})

	Convey("Given the same server in both layers and a dropped canon", t, func() {
		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"mcp":{"servers":{"x":{"type":"local","command":["n-mcp"]}},"x":{"type":"local","command":["v-mcp"]}}}`)

		surface := openCodeSurface(t, home, kind.MCP)

		Convey("Then it disappears from both layers and does not come back", func() {
			So(surface.Write(t.Context(), kind.Items{}), ShouldBeNil)

			doc := jsonDoc(t, readFile(t, path))
			So(jsonMap(t, doc, "mcp", "servers"), ShouldBeEmpty)
			So(jsonMap(t, doc, "mcp"), ShouldNotContainKey, "x")

			snap, err := surface.Read(t.Context())
			So(err, ShouldBeNil)
			So(snap.Items, ShouldBeEmpty)
		})
	})

	Convey("Given a native-only server", t, func() {
		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"mcp":{"servers":{"gone":{"type":"local","command":["g-mcp"]}}}}`)

		surface := openCodeSurface(t, home, kind.MCP)

		Convey("When it is dropped from the canon, it disappears from servers", func() {
			So(surface.Write(t.Context(), kind.Items{}), ShouldBeNil)

			doc := jsonDoc(t, readFile(t, path))
			So(jsonMap(t, doc, "mcp", "servers"), ShouldBeEmpty)
		})
	})
}

func TestOpenCodeV2PermissionsRoundTrip(t *testing.T) {
	Convey("Given a V2 permissions array with foreign rules", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"permissions":[{"action":"shell","resource":"git push *","effect":"deny"},{"action":"read","resource":"*.env","effect":"ask"},{"action":"*","resource":"*","effect":"allow"},{"action":"todowrite","resource":"*","effect":"deny"}]}`)

		surface := openCodeSurface(t, home, kind.Permissions)

		snap, err := surface.Read(t.Context())
		So(err, ShouldBeNil)
		So(snap.Items, ShouldHaveLength, 1)
		So(string(snap.Items["bash:git push *"]), ShouldEqual, "deny")

		Convey("When a canon rule is added", func() {
			desired := kind.Items{
				"bash:git push *": []byte("deny"),
				"tool:edit":       []byte("allow"),
			}

			So(surface.Write(t.Context(), desired), ShouldBeNil)

			Convey("Then it is appended and the foreign rules survive", func() {
				text := readFile(t, path)
				rules := jsonList(t, jsonDoc(t, text), "permissions")
				So(rules, ShouldHaveLength, 5)
				So(rules[len(rules)-1], ShouldResemble, map[string]any{"action": "edit", "resource": "*", "effect": "allow"})
				So(text, ShouldContainSubstring, `"*.env"`)
				So(text, ShouldContainSubstring, `"todowrite"`)

				before := text

				So(surface.Write(t.Context(), desired), ShouldBeNil)
				So(readFile(t, path), ShouldEqual, before)
			})
		})
	})
}

func TestOpenCodeV2PermissionsLastWins(t *testing.T) {
	Convey("Given duplicated native rules for one key", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"permissions":[{"action":"read","resource":"*","effect":"deny"},{"action":"read","resource":"*","effect":"allow"}]}`)

		surface := openCodeSurface(t, home, kind.Permissions)

		snap, err := surface.Read(t.Context())
		So(err, ShouldBeNil)
		So(string(snap.Items["tool:read"]), ShouldEqual, "allow")

		Convey("When the canon keeps the effective value, nothing is rewritten", func() {
			before := readFile(t, path)
			So(surface.Write(t.Context(), kind.Items{"tool:read": []byte("allow")}), ShouldBeNil)
			So(readFile(t, path), ShouldEqual, before)
		})
	})

	Convey("Given a v1 rule shadowed by a native rule", t, func() {
		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"permission":{"read":"deny"},"permissions":[{"action":"read","resource":"*","effect":"allow"}]}`)

		surface := openCodeSurface(t, home, kind.Permissions)

		snap, err := surface.Read(t.Context())
		So(err, ShouldBeNil)
		So(string(snap.Items["tool:read"]), ShouldEqual, "allow")

		Convey("When the canon flips it, the native rule changes and the v1 copy is dropped", func() {
			So(surface.Write(t.Context(), kind.Items{"tool:read": []byte("deny")}), ShouldBeNil)

			doc := jsonDoc(t, readFile(t, path))
			rules := jsonList(t, doc, "permissions")
			So(rules, ShouldHaveLength, 1)
			So(rules[0], ShouldResemble, map[string]any{"action": "read", "resource": "*", "effect": "deny"})
			So(jsonMap(t, doc, "permission"), ShouldBeEmpty)

			before := readFile(t, path)
			So(surface.Write(t.Context(), kind.Items{"tool:read": []byte("deny")}), ShouldBeNil)
			So(readFile(t, path), ShouldEqual, before)
		})
	})
}

func jsonRule(t *testing.T, value any) map[string]any {
	t.Helper()

	rule, ok := value.(map[string]any)
	if !ok {
		t.Fatal("rule is not an object")
	}

	return rule
}

func TestOpenCodeV2PermissionsAliases(t *testing.T) {
	Convey("Given V2 rules with renamed and new actions", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"permissions":[{"action":"subagent","resource":"*","effect":"ask"},{"action":"question","resource":"*","effect":"deny"},{"action":"external_directory","resource":"~/projects/*","effect":"allow"},{"action":"external_directory","resource":"*","effect":"allow"},{"action":"execute","resource":"*","effect":"ask"}]}`)

		surface := openCodeSurface(t, home, kind.Permissions)

		snap, err := surface.Read(t.Context())
		So(err, ShouldBeNil)
		So(snap.Items, ShouldHaveLength, 4)
		So(string(snap.Items["tool:task"]), ShouldEqual, "ask")
		So(string(snap.Items["tool:external_directory"]), ShouldEqual, "allow")
		So(string(snap.Items["tool:execute"]), ShouldEqual, "ask")

		Convey("When the task rule changes, it is rendered back as subagent", func() {
			desired := kind.Items{
				"tool:task":               []byte("deny"),
				"tool:question":           []byte("deny"),
				"tool:external_directory": []byte("allow"),
				"tool:execute":            []byte("ask"),
			}
			So(surface.Write(t.Context(), desired), ShouldBeNil)

			rules := jsonList(t, jsonDoc(t, readFile(t, path)), "permissions")
			So(rules, ShouldHaveLength, 5)
			So(rules[len(rules)-1], ShouldResemble, map[string]any{"action": "subagent", "resource": "*", "effect": "deny"})

			stale := false

			for _, rule := range rules {
				obj := jsonRule(t, rule)
				if obj["action"] == "subagent" && obj["effect"] == "ask" {
					stale = true
				}
			}

			So(stale, ShouldBeFalse)
		})
	})
}

func TestOpenCodeV2PermissionsStarIsForeign(t *testing.T) {
	Convey("Given the V2 wildcard shell rule", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"permissions":[{"action":"shell","resource":"*","effect":"ask"}]}`)

		surface := openCodeSurface(t, home, kind.Permissions)
		before := readFile(t, path)

		Convey("Then it is not managed and the canon wildcard is not rendered", func() {
			snap, err := surface.Read(t.Context())
			So(err, ShouldBeNil)
			So(snap.Items, ShouldBeEmpty)

			So(surface.Write(t.Context(), kind.Items{"bash:*": []byte("deny")}), ShouldBeNil)
			So(readFile(t, path), ShouldEqual, before)
		})
	})
}

func TestOpenCodeMixedPermissions(t *testing.T) {
	Convey("Given a v1-only permission rule", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"permission":{"read":"deny"}}`)

		surface := openCodeSurface(t, home, kind.Permissions)

		Convey("When the canon updates it, it is updated in the v1 layer", func() {
			So(surface.Write(t.Context(), kind.Items{"tool:read": []byte("ask")}), ShouldBeNil)

			doc := jsonDoc(t, readFile(t, path))
			So(jsonMap(t, doc, "permission")["read"], ShouldEqual, "ask")
			So(doc, ShouldNotContainKey, "permissions")
		})
	})

	Convey("Given the same rule in both layers with the same effect", t, func() {
		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"permission":{"read":"allow"},"permissions":[{"action":"read","resource":"*","effect":"allow"}]}`)

		surface := openCodeSurface(t, home, kind.Permissions)

		Convey("When the canon keeps it, nothing is rewritten", func() {
			before := readFile(t, path)
			So(surface.Write(t.Context(), kind.Items{"tool:read": []byte("allow")}), ShouldBeNil)
			So(readFile(t, path), ShouldEqual, before)
		})

		Convey("When the canon drops it, it disappears from both layers", func() {
			So(surface.Write(t.Context(), kind.Items{}), ShouldBeNil)

			doc := jsonDoc(t, readFile(t, path))
			So(jsonMap(t, doc, "permission"), ShouldBeEmpty)
			So(jsonList(t, doc, "permissions"), ShouldBeEmpty)
		})
	})

	Convey("Given a native rule and a diverging v1 duplicate", t, func() {
		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"permission":{"read":"deny"},"permissions":[{"action":"read","resource":"*","effect":"allow"}]}`)

		surface := openCodeSurface(t, home, kind.Permissions)

		Convey("When the canon keeps the native value, the v1 copy is left as is", func() {
			before := readFile(t, path)

			So(surface.Write(t.Context(), kind.Items{"tool:read": []byte("allow")}), ShouldBeNil)
			So(readFile(t, path), ShouldEqual, before)
		})
	})

	Convey("Given an unmanaged v1 rule", t, func() {
		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"permission":{"doom_loop":"allow"}}`)

		surface := openCodeSurface(t, home, kind.Permissions)

		Convey("Then it is preserved on an empty write", func() {
			before := readFile(t, path)
			So(surface.Write(t.Context(), kind.Items{}), ShouldBeNil)
			So(readFile(t, path), ShouldEqual, before)
		})
	})
}

func TestOpenCodeV2DetectionGuards(t *testing.T) {
	Convey("Given malformed V2 shapes", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"mcp":{"servers":"nope"}}`)

		Convey("Then reads and writes are refused", func() {
			_, err := openCodeSurface(t, home, kind.MCP).Read(t.Context())
			So(err, ShouldBeError)

			err = openCodeSurface(t, home, kind.MCP).Write(t.Context(), kind.Items{})
			So(err, ShouldBeError)
		})
	})

	Convey("Given a non-array permissions value", t, func() {
		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"permissions":{}}`)

		Convey("Then reads and writes are refused", func() {
			_, err := openCodeSurface(t, home, kind.Permissions).Read(t.Context())
			So(err, ShouldBeError)

			err = openCodeSurface(t, home, kind.Permissions).Write(t.Context(), kind.Items{})
			So(err, ShouldBeError)
		})
	})

	Convey("Given a v1 server that is literally named servers", t, func() {
		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"mcp":{"servers":{"type":"local","command":["x-mcp"]}}}`)

		surface := openCodeSurface(t, home, kind.MCP)

		Convey("Then it is treated as a v1 entry", func() {
			snap, err := surface.Read(t.Context())
			So(err, ShouldBeNil)
			So(server(t, snap.Items["servers"]).Command, ShouldResemble, []string{"x-mcp"})

			before := readFile(t, path)
			So(surface.Write(t.Context(), snap.Items), ShouldBeNil)
			So(readFile(t, path), ShouldEqual, before)
		})
	})
}

func TestOpenCodePermissionsFollowMCPDialect(t *testing.T) {
	Convey("Given a V1 config with top-level mcp servers", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"mcp":{"a":{"type":"local","command":["a-mcp"]}}}`)

		Convey("When permissions and a server are written", func() {
			So(openCodeSurface(t, home, kind.Permissions).Write(t.Context(), kind.Items{"tool:read": []byte("allow")}), ShouldBeNil)
			So(openCodeSurface(t, home, kind.MCP).Write(t.Context(), kind.Items{"b": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"b-mcp"}})}), ShouldBeNil)

			Convey("Then both stay in the V1 dialect: no V2 array in a V1 file", func() {
				doc := jsonDoc(t, readFile(t, path))
				So(jsonMap(t, doc, "permission")["read"], ShouldEqual, "allow")
				So(doc, ShouldNotContainKey, "permissions")
				So(jsonMap(t, doc, "mcp"), ShouldContainKey, "b")
				So(jsonMap(t, doc, "mcp"), ShouldNotContainKey, "servers")
			})
		})
	})

	Convey("Given an empty v1 mcp object", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"mcp":{}}`)

		Convey("When permissions are written", func() {
			So(openCodeSurface(t, home, kind.Permissions).Write(t.Context(), kind.Items{"tool:read": []byte("allow")}), ShouldBeNil)

			Convey("Then they use the V1 object, not the V2 array", func() {
				doc := jsonDoc(t, readFile(t, path))
				So(jsonMap(t, doc, "permission")["read"], ShouldEqual, "allow")
				So(doc, ShouldNotContainKey, "permissions")
			})
		})
	})

	Convey("Given a V2 mcp container", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"mcp":{"servers":{}}}`)

		Convey("When permissions are written", func() {
			So(openCodeSurface(t, home, kind.Permissions).Write(t.Context(), kind.Items{"tool:read": []byte("allow")}), ShouldBeNil)

			Convey("Then they use the V2 array, not the V1 object", func() {
				doc := jsonDoc(t, readFile(t, path))
				So(doc, ShouldContainKey, "permissions")
				So(jsonList(t, doc, "permissions"), ShouldHaveLength, 1)
				So(doc, ShouldNotContainKey, "permission")
			})
		})
	})
}

func TestOpenCodeDialectDefaultsPerKind(t *testing.T) {
	Convey("Given an empty v1 mcp object", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"mcp":{}}`)

		surface := openCodeSurface(t, home, kind.MCP)

		Convey("Then new servers stay in the v1 dialect", func() {
			So(surface.Write(t.Context(), kind.Items{"a": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"a-mcp"}})}), ShouldBeNil)

			doc := jsonDoc(t, readFile(t, path))
			So(jsonMap(t, doc, "mcp"), ShouldContainKey, "a")
			So(jsonMap(t, doc, "mcp"), ShouldNotContainKey, "servers")
		})
	})

	Convey("Given a config without any dialect markers", t, func() {
		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"model":"x"}`)

		Convey("Then MCP and permissions go to their native V2 shapes", func() {
			So(openCodeSurface(t, home, kind.MCP).Write(t.Context(), kind.Items{"a": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"a-mcp"}})}), ShouldBeNil)
			So(openCodeSurface(t, home, kind.Permissions).Write(t.Context(), kind.Items{"tool:read": []byte("allow")}), ShouldBeNil)

			doc := jsonDoc(t, readFile(t, path))
			So(jsonMap(t, doc, "mcp", "servers"), ShouldContainKey, "a")
			So(doc, ShouldContainKey, "permissions")
			So(doc, ShouldNotContainKey, "permission")

			rules := jsonList(t, doc, "permissions")
			So(rules, ShouldHaveLength, 1)
		})
	})

	Convey("Given a config that only has the v1 permissions map", t, func() {
		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, `{"permission":{"read":"deny"}}`)

		Convey("Then MCP goes native while permissions stay v1", func() {
			So(openCodeSurface(t, home, kind.MCP).Write(t.Context(), kind.Items{"a": mcp.Encode(mcp.Server{Transport: mcp.TransportStdio, Command: []string{"a-mcp"}})}), ShouldBeNil)
			So(openCodeSurface(t, home, kind.Permissions).Write(t.Context(), kind.Items{"tool:read": []byte("allow")}), ShouldBeNil)

			doc := jsonDoc(t, readFile(t, path))
			So(jsonMap(t, doc, "mcp", "servers"), ShouldContainKey, "a")
			So(jsonMap(t, doc, "permission")["read"], ShouldEqual, "allow")
			So(doc, ShouldNotContainKey, "permissions")
		})
	})
}
