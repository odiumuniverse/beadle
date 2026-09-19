package skill_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/skill"
)

func TestIsStubDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	marker := filepath.Join(dir, skill.StubMarkerFile)

	_, ok := skill.IsStubDir(dir)
	require.False(t, ok)

	require.NoError(t, os.WriteFile(marker, []byte("v1 acme/tool 1.0.0 2026-09-16T10:30:00Z\n"), 0o600))

	key, ok := skill.IsStubDir(dir)
	require.True(t, ok)
	require.Equal(t, "acme/tool", key)

	for _, broken := range []string{
		"v2 acme/tool 1.0.0 2026-09-16T10:30:00Z\n",
		"v1 acme/tool 1.0.0 not-a-date\n",
		"v1 acme/tool\n",
		"",
	} {
		require.NoError(t, os.WriteFile(marker, []byte(broken), 0o600))

		_, ok = skill.IsStubDir(dir)
		require.False(t, ok, "marker %q must not be recognized", broken)
	}
}
