package merge_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/merge"
)

const conflictExample = "a\n" +
	"<<<<<<< vault\n" +
	"V\n" +
	"||||||| base\n" +
	"b\n" +
	"=======\n" +
	"A\n" +
	">>>>>>> agent\n"

func TestText(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		base, vault, agent string
		want               string
		wantConflicts      int
	}{
		"no changes": {
			base: "a\nb\n", vault: "a\nb\n", agent: "a\nb\n",
			want: "a\nb\n",
		},
		"vault change only": {
			base: "a\nb\n", vault: "A\nb\n", agent: "a\nb\n",
			want: "A\nb\n",
		},
		"agent change only": {
			base: "a\nb\n", vault: "a\nb\n", agent: "a\nB\n",
			want: "a\nB\n",
		},
		"both same change": {
			base: "a\nb\n", vault: "a\nX\n", agent: "a\nX\n",
			want: "a\nX\n",
		},
		"independent changes": {
			base: "a\nb\nc\n", vault: "A\nb\nc\n", agent: "a\nb\nC\n",
			want: "A\nb\nC\n",
		},
		"both different on the same line": {
			base: "a\nb\n", vault: "a\nV\n", agent: "a\nA\n",
			want: conflictExample, wantConflicts: 1,
		},
		"both add the same line": {
			base: "a\n", vault: "a\nx\n", agent: "a\nx\n",
			want: "a\nx\n",
		},
		"agent deletes, vault untouched": {
			base: "a\nb\n", vault: "a\nb\n", agent: "a\n",
			want: "a\n",
		},
		"vault deletes, agent untouched": {
			base: "a\nb\n", vault: "a\n", agent: "a\nb\n",
			want: "a\n",
		},
		"vault modifies, agent deletes": {
			base: "a\nb\n", vault: "a\nB\n", agent: "a\n",
			want: "a\n" +
				"<<<<<<< vault\n" +
				"B\n" +
				"||||||| base\n" +
				"b\n" +
				"=======\n" +
				">>>>>>> agent\n",
			wantConflicts: 1,
		},
		"both add different lines to empty base": {
			base: "", vault: "v\n", agent: "a\n",
			wantConflicts: 1,
		},
		"one side adds to empty base": {
			base: "", vault: "v\n", agent: "",
			want: "v\n",
		},
		"no trailing newline on one line": {
			base: "a\nb", vault: "a\nb", agent: "a\nB",
			want: "a\nB",
		},
		"no trailing newline in conflict": {
			base: "a\nb", vault: "a\nV", agent: "a\nA",
			want: "a\n" +
				"<<<<<<< vault\n" +
				"V\n" +
				"||||||| base\n" +
				"b\n" +
				"=======\n" +
				"A\n" +
				">>>>>>> agent\n",
			wantConflicts: 1,
		},
		"crlf preserved": {
			base: "a\r\nb\r\n", vault: "A\r\nb\r\n", agent: "a\r\nb\r\n",
			want: "A\r\nb\r\n",
		},
		"two conflicts": {
			base: "a\nb\nc\nd\n", vault: "a\nV\nc\nV\n", agent: "a\nA\nc\nA\n",
			wantConflicts: 2,
		},
		"empty everything": {
			base: "", vault: "", agent: "",
			want: "",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := merge.Text([]byte(tt.base), []byte(tt.vault), []byte(tt.agent), merge.TextOptions{})

			require.Equal(t, tt.wantConflicts, got.Conflicts, "conflicts")

			if tt.wantConflicts == 0 || tt.want != "" {
				require.Equal(t, tt.want, string(got.Merged))
			}
		})
	}
}
