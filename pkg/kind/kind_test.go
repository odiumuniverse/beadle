package kind_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/kind"
)

func specOf(t *testing.T, id kind.ID) kind.Spec {
	t.Helper()

	spec, ok := kind.Lookup(id)
	require.True(t, ok)

	return spec
}

func TestMergeText(t *testing.T) {
	t.Parallel()

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
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			merged, ok := specOf(t, kind.Rules).Merge(tt.base, tt.vault, tt.agent)
			require.Equal(t, tt.ok, ok)

			if tt.ok {
				require.Equal(t, tt.want, string(merged))
			}
		})
	}
}

func TestMergeJSON(t *testing.T) {
	t.Parallel()

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
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var base []byte
			if tt.base != "" {
				base = []byte(tt.base)
			}

			merged, ok := specOf(t, kind.MCP).Merge(base, []byte(tt.vault), []byte(tt.agent))
			require.Equal(t, tt.ok, ok)

			if tt.ok {
				require.JSONEq(t, tt.want, string(merged))
			}
		})
	}
}

func TestMergeFileRefusesBinary(t *testing.T) {
	t.Parallel()

	_, ok := specOf(t, kind.Skills).Merge([]byte("a"), []byte("b"), []byte{'c', 0})
	require.False(t, ok)

	merged, ok := specOf(t, kind.Skills).Merge([]byte("a\nb\nc\n"), []byte("A\nb\nc\n"), []byte("a\nb\nC\n"))
	require.True(t, ok, "non-adjacent edits of a text file merge")
	require.Equal(t, "A\nb\nC\n", string(merged))

	_, ok = specOf(t, kind.Skills).Merge([]byte("a\nb\n"), []byte("A\nb\n"), []byte("a\nB\n"))
	require.False(t, ok, "adjacent edits conflict, as in git")
}

func TestPermissionsNeverMergeScalars(t *testing.T) {
	t.Parallel()

	_, ok := specOf(t, kind.Permissions).Merge([]byte("allow"), []byte("ask"), []byte("deny"))
	require.False(t, ok)
}

func TestLiftKeepsFieldsTheAgentCannotSee(t *testing.T) {
	t.Parallel()

	lift := specOf(t, kind.MCP).Lift

	vault := []byte(`{"transport":"sse","url":"https://old","headers":{"A":"1"},"timeout":5}`)
	projected := []byte(`{"transport":"http","url":"https://old","headers":{"A":"1"}}`)

	lifted := lift(vault, projected, []byte(`{"transport":"http","url":"https://new","headers":{"A":"1"}}`))
	require.JSONEq(t, `{"transport":"sse","url":"https://new","headers":{"A":"1"},"timeout":5}`, string(lifted))

	lifted = lift(vault, projected, []byte(`{"transport":"http","url":"https://old"}`))
	require.JSONEq(t, `{"transport":"sse","url":"https://old","timeout":5}`, string(lifted), "a field the agent removed is removed")

	lifted = lift(vault, projected, []byte(`{"transport":"http","url":"https://old","headers":{"A":"1","B":"2"}}`))
	require.JSONEq(t, `{"transport":"sse","url":"https://old","headers":{"A":"1","B":"2"},"timeout":5}`, string(lifted))
}

func TestCanonicalJSON(t *testing.T) {
	t.Parallel()

	want := `{"a":[2,1],"b":1.0}`

	out, err := kind.CanonicalJSON([]byte(`{ "b": 1.0, "a": [2, 1] }`))
	require.NoError(t, err)
	require.Equal(t, want, string(out), "the canonical form is byte for byte stable")

	_, err = kind.CanonicalJSON([]byte(`{`))
	require.Error(t, err)
}

func TestHasMarkers(t *testing.T) {
	t.Parallel()

	require.True(t, kind.HasMarkers([]byte("x\n<<<<<<< vault\na\n=======\nb\n>>>>>>> agent\n")))
	require.True(t, kind.HasMarkers([]byte("=======\r\n")))
	require.False(t, kind.HasMarkers([]byte("inline <<<<<<< is prose\n")))
	require.False(t, kind.HasMarkers([]byte("plain text\n")))
}

func TestGroups(t *testing.T) {
	t.Parallel()

	require.Equal(t, "alpha", specOf(t, kind.Skills).Group("alpha/scripts/run.sh"))
	require.Equal(t, "bash:git *", specOf(t, kind.Permissions).Group("bash:git *"))
	require.True(t, specOf(t, kind.Rules).Singleton)
}

func TestParse(t *testing.T) {
	t.Parallel()

	id, err := kind.Parse("mcp")
	require.NoError(t, err)
	require.Equal(t, kind.MCP, id)

	_, err = kind.Parse("plugins")
	require.Error(t, err)
}
