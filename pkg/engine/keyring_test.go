package engine_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/agents-sync/pkg/agent"
	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/engine"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
	"github.com/odiumuniverse/agents-sync/pkg/secret"
	"github.com/odiumuniverse/agents-sync/pkg/vault"
)

type keyringFake struct {
	values  map[string]string
	sets    int
	failGet error
	failSet error
}

func (f *keyringFake) Get(account string) (string, bool, error) {
	if f.failGet != nil {
		return "", false, f.failGet
	}

	value, ok := f.values[account]

	return value, ok, nil
}

func (f *keyringFake) Set(account, value string) error {
	f.sets++

	if f.failSet != nil {
		return f.failSet
	}

	f.values[account] = value

	return nil
}

func (f *keyringFake) Delete(account string) (bool, error) {
	if _, ok := f.values[account]; !ok {
		return false, nil
	}

	delete(f.values, account)

	return true, nil
}

func secretsIndex(t *testing.T, path string, names ...string) {
	t.Helper()

	values := map[string]string{}

	for _, name := range names {
		values[name] = ""
	}

	encoded, err := json.Marshal(map[string]any{"version": 2, "backend": "keyring", "secrets": values})
	require.NoError(t, err)

	write(t, path, string(encoded)+"\n")
}

func newKeyringFixture(t *testing.T, keyring secret.Keyring, names ...string) *fixture {
	t.Helper()

	home := t.TempDir()
	v := vault.New(filepath.Join(t.TempDir(), "vault"))

	require.NoError(t, v.Init())

	cfg, err := config.Load(v.ConfigPath())
	require.NoError(t, err)

	cfg.Enable(agent.ClaudeCodeID)
	cfg.Enable(agent.OpenCodeID)
	require.NoError(t, cfg.Save(v.ConfigPath()))

	secretsIndex(t, v.SecretsPath(), names...)

	e, err := engine.New(v, cfg, agent.All(home, t.TempDir()), engine.WithHome(home), engine.WithKeyring(keyring))
	require.NoError(t, err)

	return &fixture{home: home, vault: v, config: cfg, engine: e}
}

func TestKeyringModeExtractsAndResolves(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	keyring := &keyringFake{values: map[string]string{}}
	f := newKeyringFixture(t, keyring)

	write(t, f.claudeConfig(), `{"mcpServers": {"ctx7": {"type": "http", "url": "https://mcp.example.com",
	  "headers": {"Authorization": "Bearer abc123"}}}}`)
	write(t, f.openCodeConfig(), `{"mcp": {}}`)

	report := f.sync(t)
	require.Empty(t, report.Warnings)
	require.Equal(t, "Bearer abc123", keyring.values["AUTHORIZATION"], "the value lands in the keyring")

	vaultRaw := read(t, f.vault.ServersPath())
	require.Contains(t, vaultRaw, "{secret:AUTHORIZATION}")
	require.NotContains(t, vaultRaw, "abc123")

	openCode := read(t, f.openCodeConfig())
	require.Contains(t, openCode, "Bearer abc123", "the keyring value resolves for the agent")
	require.NotContains(t, openCode, "{secret:")

	secretsRaw := read(t, f.vault.SecretsPath())
	require.Contains(t, secretsRaw, `"backend": "keyring"`)
	require.NotContains(t, secretsRaw, "abc123", "the index holds names only")
}

func TestKeyringModeMissingValueSkips(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	keyring := &keyringFake{values: map[string]string{}}
	f := newKeyringFixture(t, keyring, "AUTHORIZATION")

	write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	write(t, f.openCodeConfig(), `{"mcp": {}}`)
	write(t, f.vault.ServersPath(), `{"ctx7":{"transport":"http","url":"https://mcp.example.com","headers":{"Authorization":"{secret:AUTHORIZATION}"}}}`)

	report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
	require.NoError(t, err)

	result, ok := report.Kind(kind.MCP).Agent(agent.OpenCodeID)
	require.True(t, ok)
	require.Equal(t, engine.ActionSkipped, result.Action)
	require.Contains(t, result.Note, "AUTHORIZATION")
	require.JSONEq(t, `{"mcp": {}}`, read(t, f.openCodeConfig()), "never wiped, never given a broken reference")

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityError, "AUTHORIZATION has no value"), "missing values surface in doctor: %v", issues)
}

func TestKeyringModeDryRunAndDoctorAreReadOnly(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	keyring := &keyringFake{values: map[string]string{}}
	f := newKeyringFixture(t, keyring)

	write(t, f.claudeConfig(), `{"mcpServers": {"ctx7": {"type": "http", "url": "https://mcp.example.com",
	  "headers": {"Authorization": "Bearer abc123"}}}}`)
	write(t, f.openCodeConfig(), `{"mcp": {}}`)

	_, err := f.engine.Sync(t.Context(), engine.SyncOptions{DryRun: true})
	require.NoError(t, err)
	require.Zero(t, keyring.sets, "a dry run never writes to the keyring")

	_, err = f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.Zero(t, keyring.sets, "doctor never writes to the keyring")

	f.sync(t)
	require.Positive(t, keyring.sets, "a real sync materializes the extracted value")
}

func TestKeyringModeSaveFailureFailsSync(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	keyring := &keyringFake{values: map[string]string{}, failSet: errors.New("keyring write denied")}
	f := newKeyringFixture(t, keyring)

	write(t, f.claudeConfig(), `{"mcpServers": {"ctx7": {"type": "http", "url": "https://mcp.example.com",
	  "headers": {"Authorization": "Bearer abc123"}}}}`)
	write(t, f.openCodeConfig(), `{"mcp": {}}`)

	before := read(t, f.vault.SecretsPath())

	_, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
	require.ErrorContains(t, err, "keyring")

	vaultRaw := read(t, f.vault.ServersPath())
	require.Contains(t, vaultRaw, "{secret:AUTHORIZATION}")
	require.NotContains(t, vaultRaw, "abc123", "the canon never holds the plaintext")
	require.Equal(t, before, read(t, f.vault.SecretsPath()), "the index is not rewritten")
}

func TestKeyringModePrefetchFailureDegradesVisibly(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	keyring := &keyringFake{values: map[string]string{}, failGet: errors.New("keyring is locked")}
	f := newKeyringFixture(t, keyring, "AUTHORIZATION")

	write(t, f.claudeConfig(), `{"mcpServers": {}}`)
	write(t, f.openCodeConfig(), `{"mcp": {}}`)
	write(t, f.vault.ServersPath(), `{"ctx7":{"transport":"http","url":"https://mcp.example.com","headers":{"Authorization":"{secret:AUTHORIZATION}"}}}`)

	report, err := f.engine.Sync(t.Context(), engine.SyncOptions{})
	require.NoError(t, err)

	warnings := 0

	for _, warning := range report.Warnings {
		if strings.Contains(warning, "keyring unavailable") {
			warnings++
		}
	}

	require.Equal(t, 1, warnings)

	result, ok := report.Kind(kind.MCP).Agent(agent.OpenCodeID)
	require.True(t, ok)
	require.Equal(t, engine.ActionSkipped, result.Action)

	issues, err := f.engine.Doctor(t.Context())
	require.NoError(t, err)
	require.True(t, hasIssue(issues, engine.SeverityWarn, "keyring unavailable"), "doctor warns once: %v", issues)
	require.False(t, hasIssue(issues, engine.SeverityError, "has no value"), "per-ref checks are skipped: %v", issues)
}

func TestKeyringModeMemoryNoteResolves(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	keyring := &keyringFake{values: map[string]string{"TOKEN": "tok-12345"}}
	f := newKeyringFixture(t, keyring, "TOKEN")
	f.emptyConfigs(t)

	write(t, claudeMemory(f, "-Users-a", "MEMORY.md"), "# token\n")
	f.sync(t)

	write(t, vaultMemory(f, "-Users-a", "MEMORY.md"), "# token {secret:TOKEN}\n")
	f.sync(t)

	note := read(t, claudeMemory(f, "-Users-a", "MEMORY.md"))
	require.Contains(t, note, "tok-12345")
	require.NotContains(t, note, "{secret:")
}
