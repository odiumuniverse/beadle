package agent_test

import (
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// TestPermissionsBashRuleOnEmptyOpenCodeConfig pins the chained JSON Patch
// case: the first bash rule creates the "bash" object and the rule itself in
// one write, so the next sync reads the rule back and stays a no-op.
// TestPermissionsScalarBashBecomesPrettyObject pins the scalar-to-object
// transition: the payload builds {"*": ...} for the legacy scalar, and the
// children added in the same patch must not leave the container compact.
func TestPermissionsScalarBashBecomesPrettyObject(t *testing.T) {
	Convey("Given an opencode config with a scalar bash rule", t, func() {
		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, "{\n  \"permission\": {\n    \"bash\": \"allow\"\n  }\n}\n")

		Convey("When the scalar is expanded with more rules", func() {
			So(surfaceOf(t, agent.OpenCode(home, t.TempDir()), kind.Permissions).Write(t.Context(), kind.Items{
				"bash:*":          []byte("allow"),
				"bash:git status": []byte("allow"),
			}), ShouldBeNil)

			Convey("Then the whole container is pretty-printed", func() {
				So(readFile(t, path), ShouldEqual, `{
  "permission": {
    "bash": {
      "*": "allow",
      "git status": "allow"
    }
  }
}
`)
			})
		})
	})
}

func TestPermissionsBashRuleOnEmptyOpenCodeConfig(t *testing.T) {
	Convey("Given an opencode config with an empty permission object", t, func() {
		home := t.TempDir()
		path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
		writeFile(t, path, "{\n  \"permission\": {}\n}\n")

		a := agent.OpenCode(home, t.TempDir())

		Convey("When a bash rule and a tool rule are written", func() {
			So(surfaceOf(t, a, kind.Permissions).Write(t.Context(), kind.Items{
				"bash:git status": []byte("allow"),
				"tool:webfetch":   []byte("deny"),
			}), ShouldBeNil)

			Convey("Then the parent object and both rules are persisted", func() {
				back := snapshot(t, a, kind.Permissions)
				So(back.Items["bash:git status"], ShouldResemble, []byte("allow"))
				So(back.Items["tool:webfetch"], ShouldResemble, []byte("deny"))
			})

			Convey("Then the container and its children carry the file indentation", func() {
				So(readFile(t, path), ShouldEqual, `{
  "permission": {
    "bash": {
      "git status": "allow"
    },
    "webfetch": "deny"
  }
}
`)
			})

			Convey("And writing the same rules again changes nothing", func() {
				before := readFile(t, path)
				So(surfaceOf(t, a, kind.Permissions).Write(t.Context(), kind.Items{
					"bash:git status": []byte("allow"),
					"tool:webfetch":   []byte("deny"),
				}), ShouldBeNil)
				So(readFile(t, path), ShouldEqual, before)
			})
		})
	})
}
