package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func newSecretsVault(t *testing.T) string {
	t.Helper()

	home := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("AGENTSYNC_HOME", filepath.Join(home, ".agent-sync"))
	t.Setenv("XDG_CONFIG_HOME", "")

	_, err := runCLI(t, "init")
	require.NoError(t, err)

	return home
}

func readSecrets(t *testing.T, home string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(home, ".agent-sync", "mcp", "secrets.json")) //nolint:gosec // G304: test reads its own temp file
	require.NoError(t, err)

	return string(data)
}

func TestKeyringListShowsBackend(t *testing.T) { //nolint:paralleltest // mutates HOME via t.Setenv
	home := newSecretsVault(t)

	out, err := runCLI(t, "secrets", "set", "TOKEN", "s3cr3t")
	require.NoError(t, err)
	require.Contains(t, out, "stored TOKEN")

	out, err = runCLI(t, "secrets", "list")
	require.NoError(t, err)
	require.Contains(t, out, "secrets: 1 (backend: file, mode: literal)")
	require.Contains(t, out, "TOKEN")

	require.Contains(t, readSecrets(t, home), "s3cr3t")
}

func TestKeyringMigrateRefusesWithoutTool(t *testing.T) {
	home := newSecretsVault(t)

	_, err := runCLI(t, "secrets", "set", "TOKEN", "s3cr3t")
	require.NoError(t, err)

	before := readSecrets(t, home)

	t.Setenv("PATH", t.TempDir())

	_, err = runCLI(t, "secrets", "migrate", "keyring")
	require.ErrorContains(t, err, "keyring unavailable")

	require.Equal(t, before, readSecrets(t, home), "the refusal happens before any change")
	require.NotContains(t, readSecrets(t, home), `"backend"`)
}

func TestKeyringMigrateAlreadyKeyringProbes(t *testing.T) {
	home := newSecretsVault(t)

	writeFile(t, filepath.Join(home, ".agent-sync", "mcp", "secrets.json"), `{"version": 2, "backend": "keyring", "secrets": {}}`)

	t.Setenv("PATH", t.TempDir())

	_, err := runCLI(t, "secrets", "migrate", "keyring")
	require.ErrorContains(t, err, "keyring unavailable")
}

func TestKeyringMigrateFileIsNoop(t *testing.T) { //nolint:paralleltest // mutates HOME via t.Setenv
	home := newSecretsVault(t)

	_, err := runCLI(t, "secrets", "set", "TOKEN", "s3cr3t")
	require.NoError(t, err)

	out, err := runCLI(t, "secrets", "migrate", "file")
	require.NoError(t, err)
	require.Contains(t, out, "backend is already file")

	_, err = runCLI(t, "secrets", "migrate", "gpg")
	require.ErrorContains(t, err, "unknown backend")

	require.Contains(t, readSecrets(t, home), "s3cr3t")
}
