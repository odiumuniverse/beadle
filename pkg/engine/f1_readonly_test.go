package engine_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDoctorReadOnlyOnRealFilesystem(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)
	f.emptyConfigs(t)

	plugin := pluginTree(t, f.home, "acme", "tool", "1.0.0")
	writeSkill(t, plugin, "alpha", "# alpha\n")

	f.sync(t)

	before := hashTree(t, f.home)

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)

	for _, issue := range issues {
		require.NotContains(t, issue.Message, "symlinks are not supported")
	}

	require.Equal(t, before, hashTree(t, f.home), "doctor must be read-only")
}
