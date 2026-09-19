//go:build !windows

package merge_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/merge"
)

func TestTextOracle(t *testing.T) {
	t.Parallel()

	if os.Getenv("BEADLE_GIT_ORACLE") == "" {
		t.Skip("set BEADLE_GIT_ORACLE=1 to compare against git merge-file")
	}

	git, err := exec.LookPath("git")
	require.NoError(t, err)

	tests := map[string]struct{ base, vault, agent string }{
		"no changes":          {base: "a\nb\n", vault: "a\nb\n", agent: "a\nb\n"},
		"vault change":        {base: "a\nb\n", vault: "A\nb\n", agent: "a\nb\n"},
		"agent change":        {base: "a\nb\n", vault: "a\nb\n", agent: "a\nB\n"},
		"both same":           {base: "a\nb\n", vault: "a\nX\n", agent: "a\nX\n"},
		"independent":         {base: "a\nb\nc\n", vault: "A\nb\nc\n", agent: "a\nb\nC\n"},
		"conflict":            {base: "a\nb\n", vault: "a\nV\n", agent: "a\nA\n"},
		"vault deletes":       {base: "a\nb\n", vault: "a\n", agent: "a\nb\n"},
		"agent deletes":       {base: "a\nb\n", vault: "a\nb\n", agent: "a\n"},
		"no trailing newline": {base: "a\nb", vault: "a\nV", agent: "a\nA"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			basePath := filepath.Join(dir, "base")
			vaultPath := filepath.Join(dir, "vault")
			agentPath := filepath.Join(dir, "agent")

			require.NoError(t, os.WriteFile(basePath, []byte(tt.base), 0o600))
			require.NoError(t, os.WriteFile(vaultPath, []byte(tt.vault), 0o600))
			require.NoError(t, os.WriteFile(agentPath, []byte(tt.agent), 0o600))

			cmd := exec.CommandContext(t.Context(), git, "merge-file", "--diff3", "-p", //nolint:gosec // G204: git path comes from exec.LookPath
				"-L", "vault", "-L", "base", "-L", "agent",
				vaultPath, basePath, agentPath)

			expected, err := cmd.Output()

			var exitErr *exec.ExitError
			if err != nil && !errors.As(err, &exitErr) {
				require.NoError(t, err)
			}

			got := merge.Text([]byte(tt.base), []byte(tt.vault), []byte(tt.agent), merge.TextOptions{})
			require.Equal(t, string(expected), string(got.Merged))
		})
	}
}
