package kind_test

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/kind"
)

func specOf(t *testing.T, id kind.ID) kind.Spec {
	t.Helper()

	spec, ok := kind.Lookup(id)
	if !ok {
		t.Fatalf("kind %q not found", id)
	}

	return spec
}

func TestMergeText(t *testing.T) {
	Convey("Given a table of text merge cases", t, func() {
		tests := []struct {
			name               string
			base, vault, agent []byte
			want               string
			ok                 bool
		}{
			{name: "independent edits merge", base: []byte("a\nb\nc\n"), vault: []byte("A\nb\nc\n"), agent: []byte("a\nb\nC\n"), want: "A\nb\nC\n", ok: true},
			{name: "same line edited differently", base: []byte("a\n"), vault: []byte("b\n"), agent: []byte("c\n")},
			{name: "deleted versus modified", base: []byte("a\n"), vault: nil, agent: []byte("b\n")},
			{name: "added differently on both sides", base: nil, vault: []byte("x\n"), agent: []byte("y\n")},
		}

		for _, tt := range tests {
			Convey("When "+tt.name, func() {
				merged, ok := specOf(t, kind.Rules).Merge(tt.base, tt.vault, tt.agent)
				So(ok, ShouldEqual, tt.ok)

				if tt.ok {
					So(string(merged), ShouldEqual, tt.want)
				}
			})
		}
	})
}

func TestMergeJSON(t *testing.T) {
	Convey("Given a table of JSON merge cases", t, func() {
		tests := []struct {
			name               string
			base, vault, agent string
			want               string
			ok                 bool
		}{
			{
				name:  "disjoint field edits merge",
				base:  `{"command":["a"],"transport":"stdio"}`,
				vault: `{"command":["a"],"env":{"K":"1"},"transport":"stdio"}`,
				agent: `{"command":["b"],"transport":"stdio"}`,
				want:  `{"command":["b"],"env":{"K":"1"},"transport":"stdio"}`,
				ok:    true,
			},
			{
				name:  "same field edited differently",
				base:  `{"command":["a"]}`,
				vault: `{"command":["b"]}`,
				agent: `{"command":["c"]}`,
			},
			{
				name:  "both added compatible fields",
				vault: `{"command":["a"],"transport":"stdio"}`,
				agent: `{"command":["a"],"env":{"K":"1"},"transport":"stdio"}`,
				want:  `{"command":["a"],"env":{"K":"1"},"transport":"stdio"}`,
				ok:    true,
			},
		}

		for _, tt := range tests {
			Convey("When "+tt.name, func() {
				var base []byte
				if tt.base != "" {
					base = []byte(tt.base)
				}

				merged, ok := specOf(t, kind.MCP).Merge(base, []byte(tt.vault), []byte(tt.agent))
				So(ok, ShouldEqual, tt.ok)

				if tt.ok {
					So(string(merged), ShouldEqualJSON, tt.want)
				}
			})
		}
	})
}

func TestMergeFileRefusesBinary(t *testing.T) {
	Convey("Given the skills file merge", t, func() {
		Convey("When one side is binary", func() {
			_, ok := specOf(t, kind.Skills).Merge([]byte("a"), []byte("b"), []byte{'c', 0})

			Convey("Then it refuses", func() {
				So(ok, ShouldBeFalse)
			})
		})

		Convey("When non-adjacent edits are merged", func() {
			merged, ok := specOf(t, kind.Skills).Merge([]byte("a\nb\nc\n"), []byte("A\nb\nc\n"), []byte("a\nb\nC\n"))

			Convey("Then it merges", func() {
				So(ok, ShouldBeTrue)
				So(string(merged), ShouldEqual, "A\nb\nC\n")
			})
		})

		Convey("When adjacent edits are merged", func() {
			_, ok := specOf(t, kind.Skills).Merge([]byte("a\nb\n"), []byte("A\nb\n"), []byte("a\nB\n"))

			Convey("Then it conflicts, as in git", func() {
				So(ok, ShouldBeFalse)
			})
		})
	})
}

func TestPermissionsNeverMergeScalars(t *testing.T) {
	Convey("Given the permissions merge", t, func() {
		Convey("When both sides change the rule", func() {
			_, ok := specOf(t, kind.Permissions).Merge([]byte("allow"), []byte("ask"), []byte("deny"))

			Convey("Then it never merges", func() {
				So(ok, ShouldBeFalse)
			})
		})
	})
}

func TestLiftKeepsFieldsTheAgentCannotSee(t *testing.T) {
	Convey("Given the MCP lift", t, func() {
		lift := specOf(t, kind.MCP).Lift

		vault := []byte(`{"transport":"sse","url":"https://old","headers":{"A":"1"},"timeout":5}`)
		projected := []byte(`{"transport":"http","url":"https://old","headers":{"A":"1"}}`)

		Convey("When the agent changes a visible field", func() {
			lifted := lift(vault, projected, []byte(`{"transport":"http","url":"https://new","headers":{"A":"1"}}`))

			Convey("Then hidden fields survive", func() {
				So(string(lifted), ShouldEqualJSON, `{"transport":"sse","url":"https://new","headers":{"A":"1"},"timeout":5}`)
			})
		})

		Convey("When the agent removes a visible field", func() {
			lifted := lift(vault, projected, []byte(`{"transport":"http","url":"https://old"}`))

			Convey("Then it is removed too", func() {
				So(string(lifted), ShouldEqualJSON, `{"transport":"sse","url":"https://old","timeout":5}`)
			})
		})

		Convey("When the agent adds a header", func() {
			lifted := lift(vault, projected, []byte(`{"transport":"http","url":"https://old","headers":{"A":"1","B":"2"}}`))

			Convey("Then the added header is kept", func() {
				So(string(lifted), ShouldEqualJSON, `{"transport":"sse","url":"https://old","headers":{"A":"1","B":"2"},"timeout":5}`)
			})
		})
	})
}

func TestCanonicalJSON(t *testing.T) {
	Convey("Given an unordered JSON object", t, func() {
		Convey("When it is canonicalized", func() {
			out, err := kind.CanonicalJSON([]byte(`{ "b": 1.0, "a": [2, 1] }`))

			Convey("Then the canonical form is byte for byte stable", func() {
				So(err, ShouldBeNil)
				So(string(out), ShouldEqual, `{"a":[2,1],"b":1.0}`)
			})
		})

		Convey("When the JSON is broken", func() {
			_, err := kind.CanonicalJSON([]byte(`{`))

			Convey("Then it fails", func() {
				So(err, ShouldBeError)
			})
		})
	})
}

func TestHasMarkers(t *testing.T) {
	Convey("Given text with and without conflict markers", t, func() {
		Convey("When full marker lines are present", func() {
			Convey("Then they are detected", func() {
				So(kind.HasMarkers([]byte("x\n<<<<<<< vault\na\n=======\nb\n>>>>>>> agent\n")), ShouldBeTrue)
				So(kind.HasMarkers([]byte("=======\r\n")), ShouldBeTrue)
			})
		})

		Convey("When the marker is inline prose or absent", func() {
			Convey("Then it is not detected", func() {
				So(kind.HasMarkers([]byte("inline <<<<<<< is prose\n")), ShouldBeFalse)
				So(kind.HasMarkers([]byte("plain text\n")), ShouldBeFalse)
			})
		})
	})
}

func TestGroups(t *testing.T) {
	Convey("Given the kind specs", t, func() {
		Convey("When groups and singletons are read", func() {
			Convey("Then they match the kind semantics", func() {
				So(specOf(t, kind.Skills).Group("alpha/scripts/run.sh"), ShouldEqual, "alpha")
				So(specOf(t, kind.Permissions).Group("bash:git *"), ShouldEqual, "bash:git *")
				So(specOf(t, kind.Rules).Singleton, ShouldBeTrue)
			})
		})
	})
}

func TestParse(t *testing.T) {
	Convey("Given the kind parser", t, func() {
		Convey("When a known kind is parsed", func() {
			id, err := kind.Parse("mcp")

			Convey("Then it is returned", func() {
				So(err, ShouldBeNil)
				So(id, ShouldEqual, kind.MCP)
			})
		})

		Convey("When an unknown kind is parsed", func() {
			_, err := kind.Parse("plugins")

			Convey("Then parsing fails", func() {
				So(err, ShouldBeError)
			})
		})
	})
}
