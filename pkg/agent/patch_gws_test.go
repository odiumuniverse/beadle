package agent_test

import (
	"encoding/json"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"github.com/tailscale/hujson"

	"github.com/odiumuniverse/beadle/pkg/agent"
)

// patchJSONDecode parses a JSONC document into a plain value.
func patchJSONDecode(t *testing.T, data []byte) map[string]any {
	t.Helper()

	root, err := hujson.Parse(data)
	if err != nil {
		t.Fatalf("parse patched document: %v", err)
	}

	standard, err := hujson.Standardize(root.Pack())
	if err != nil {
		t.Fatalf("standardize patched document: %v", err)
	}

	var decoded map[string]any

	if err := json.Unmarshal(standard, &decoded); err != nil {
		t.Fatalf("decode patched document: %v", err)
	}

	return decoded
}

func TestPatchJSON(t *testing.T) {
	Convey("Given a JSONC document with comments and foreign members", t, func() {
		const doc = `{
  // keep this comment
  "hooks": {
    "Stop": [{"command": "old"}] // trailing note
  },
  "foreign": {"x": 1}
}
`

		Convey("When a member is replaced and another is added", func() {
			out, err := agent.PatchJSON([]byte(doc), []agent.PatchOp{
				{Op: "add", Path: "/hooks/Stop", Value: []any{map[string]any{"command": "new"}}},
				{Op: "add", Path: "/hooks/PreToolUse", Value: []any{map[string]any{"command": "added"}}},
			})
			So(err, ShouldBeNil)

			Convey("Then the comments and foreign members stay", func() {
				So(string(out), ShouldContainSubstring, "// keep this comment")
				So(string(out), ShouldContainSubstring, "// trailing note")
				So(string(out), ShouldContainSubstring, `"foreign"`)

				parsed := patchJSONDecode(t, out)
				hooksDoc, ok := parsed["hooks"].(map[string]any)
				So(ok, ShouldBeTrue)
				So(hooksDoc["Stop"], ShouldResemble, []any{map[string]any{"command": "new"}})
			})

			Convey("Then the replaced member stays compact and the added one is pretty", func() {
				So(string(out), ShouldContainSubstring, `"Stop": [{"command":"new"}], // trailing note`)
				So(string(out), ShouldContainSubstring, "\"PreToolUse\": [\n      {\n        \"command\": \"added\"\n      }\n    ]")
			})
		})

		Convey("When there are no operations", func() {
			out, err := agent.PatchJSON([]byte(doc), nil)
			So(err, ShouldBeNil)
			So(string(out), ShouldEqual, doc)
		})
	})
}
