package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdateFileRetriesAfterConcurrentWrite(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.json")

	require.NoError(t, os.WriteFile(path, []byte(`{"a":1}`), 0o600))

	calls := 0

	err := updateFile(path, 0o600, func(data []byte, present bool) ([]byte, bool, error) {
		calls++

		require.True(t, present)

		if calls == 1 {
			require.NoError(t, os.WriteFile(path, []byte(`{"a":1,"foreign":true}`), 0o600))
		}

		if calls > 1 {
			require.Contains(t, string(data), `"foreign"`, "the retry must build from the fresh content")
		}

		return withKey(t, data, "patch"), true, nil
	})

	require.NoError(t, err)
	require.GreaterOrEqual(t, calls, 2)

	got, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
	require.NoError(t, err)
	require.Contains(t, string(got), `"foreign"`)
	require.Contains(t, string(got), `"patch"`)

	assertNoTempFiles(t, filepath.Dir(path))
}

func withKey(t *testing.T, data []byte, key string) []byte {
	t.Helper()

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))

	doc[key] = true

	out, err := json.Marshal(doc)
	require.NoError(t, err)

	return out
}

func TestUpdateFileGivesUpOnPersistentChange(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.json")

	require.NoError(t, os.WriteFile(path, []byte(`{"writer":0}`), 0o600))

	calls := 0

	err := updateFile(path, 0o600, func(data []byte, present bool) ([]byte, bool, error) {
		calls++

		require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf(`{"writer":%d}`, calls)), 0o600))

		return []byte(`{"ours":true}`), true, nil
	})

	require.ErrorIs(t, err, errConcurrentWrite)
	require.Contains(t, err.Error(), "after 5 attempts")
	require.Equal(t, casAttempts, calls)

	got, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf(`{"writer":%d}`, casAttempts), string(got))
	require.NotContains(t, string(got), "ours")

	assertNoTempFiles(t, filepath.Dir(path))
}

func TestUpdateFileNoChangeSkipsWrite(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.json")

	require.NoError(t, os.WriteFile(path, []byte(`{"a":1}`), 0o600))

	err := updateFile(path, 0o600, func(data []byte, present bool) ([]byte, bool, error) {
		require.True(t, present)

		require.NoError(t, os.WriteFile(path, []byte(`{"a":1,"foreign":true}`), 0o600))

		return nil, false, nil
	})

	require.NoError(t, err)

	got, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
	require.NoError(t, err)
	require.Equal(t, `{"a":1,"foreign":true}`, string(got), "a no-op build must not touch the file")
}

func TestUpdateFileCreatesMissingFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sub", "config.json")

	err := updateFile(path, 0o640, func(data []byte, present bool) ([]byte, bool, error) {
		require.False(t, present)

		return []byte("created"), true, nil
	})
	require.NoError(t, err)

	got, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
	require.NoError(t, err)
	require.Equal(t, "created", string(got))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o640), info.Mode().Perm())

	calls := 0

	err = updateFile(path, 0o640, func(data []byte, present bool) ([]byte, bool, error) {
		calls++

		require.True(t, present)

		return nil, false, nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
}

func TestUpdateFileKeepsExistingPerm(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.json")

	require.NoError(t, os.WriteFile(path, []byte(`{"a":1}`), 0o644)) //nolint:gosec // G306: the test needs a non-default mode to prove preservation

	err := updateFile(path, 0o600, func(data []byte, present bool) ([]byte, bool, error) {
		require.True(t, present)

		return []byte(`{"a":2}`), true, nil
	})
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	for _, entry := range entries {
		require.NotContains(t, entry.Name(), ".tmp-")
	}
}
