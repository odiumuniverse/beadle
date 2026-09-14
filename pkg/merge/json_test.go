package merge_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/merge"
)

func decode(t *testing.T, s string) any {
	t.Helper()

	var v any

	require.NoError(t, json.Unmarshal([]byte(s), &v))

	return v
}

func TestJSON(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		base, vault, agent string
		want               string
		wantConflicts      []string
	}{
		"new key from agent": {
			base: `{}`, vault: `{}`, agent: `{"a":1}`,
			want: `{"a":1}`,
		},
		"new key from vault": {
			base: `{}`, vault: `{"a":1}`, agent: `{}`,
			want: `{"a":1}`,
		},
		"both add same key same value": {
			base: `{}`, vault: `{"a":1}`, agent: `{"a":1}`,
			want: `{"a":1}`,
		},
		"both add same key different value": {
			base: `{}`, vault: `{"a":1}`, agent: `{"a":2}`,
			want: `{"a":1}`, wantConflicts: []string{"a"},
		},
		"independent nested changes": {
			base:  `{"s":{"command":["x"],"env":{"A":"1"}}}`,
			vault: `{"s":{"command":["y"],"env":{"A":"1"}}}`,
			agent: `{"s":{"command":["x"],"env":{"A":"2"}}}`,
			want:  `{"s":{"command":["y"],"env":{"A":"2"}}}`,
		},
		"vault update, agent untouched": {
			base: `{"s":1}`, vault: `{"s":2}`, agent: `{"s":1}`,
			want: `{"s":2}`,
		},
		"agent update, vault untouched": {
			base: `{"s":1}`, vault: `{"s":1}`, agent: `{"s":2}`,
			want: `{"s":2}`,
		},
		"vault deletes, agent untouched": {
			base: `{"s":1,"t":2}`, vault: `{"t":2}`, agent: `{"s":1,"t":2}`,
			want: `{"t":2}`,
		},
		"agent deletes, vault untouched": {
			base: `{"s":1,"t":2}`, vault: `{"s":1,"t":2}`, agent: `{"t":2}`,
			want: `{"t":2}`,
		},
		"delete versus modify": {
			base: `{"s":1}`, vault: `{}`, agent: `{"s":2}`,
			want: `{}`, wantConflicts: []string{"s"},
		},
		"arrays are atomic": {
			base: `{"s":[1]}`, vault: `{"s":[2]}`, agent: `{"s":[3]}`,
			want: `{"s":[2]}`, wantConflicts: []string{"s"},
		},
		"arrays both changed same": {
			base: `{"s":[1]}`, vault: `{"s":[2]}`, agent: `{"s":[2]}`,
			want: `{"s":[2]}`,
		},
		"deep conflict path": {
			base: `{"s":{"env":{"K":"1"}}}`, vault: `{"s":{"env":{"K":"2"}}}`, agent: `{"s":{"env":{"K":"3"}}}`,
			want: `{"s":{"env":{"K":"2"}}}`, wantConflicts: []string{"s.env.K"},
		},
		"type change versus modify": {
			base: `{"s":1}`, vault: `{"s":{"x":1}}`, agent: `{"s":2}`,
			want: `{"s":{"x":1}}`, wantConflicts: []string{"s"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			merged, conflicts := merge.JSON(decode(t, tt.base), decode(t, tt.vault), decode(t, tt.agent))

			got, err := json.Marshal(merged)
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))

			var paths []string

			for _, c := range conflicts {
				paths = append(paths, c.Path)
			}

			require.Equal(t, tt.wantConflicts, paths)
		})
	}
}
