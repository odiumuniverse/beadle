package fsutil_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
)

func TestExpandHome(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	require.NoError(t, err)

	tests := map[string]string{
		"~":         home,
		"~/x":       filepath.Join(home, "x"),
		"/abs/path": "/abs/path",
		"relative":  "relative",
	}

	for in, want := range tests {
		t.Run(in, func(t *testing.T) {
			t.Parallel()

			got, err := fsutil.ExpandHome(in)
			require.NoError(t, err)
			require.Equal(t, want, got)
		})
	}
}

func TestExists(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.True(t, fsutil.Exists(dir))
	require.False(t, fsutil.Exists(filepath.Join(dir, "missing")))
}
