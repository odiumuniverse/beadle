package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/config"
)

func TestPluginPinRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), config.FileName)

	cfg := config.Default()
	require.NoError(t, cfg.SetPluginPin("claude-code", "acme/tool", "1.0.0"))
	require.NoError(t, cfg.SetPluginPin("opencode", "acme/tool", "2.1.0"))
	require.NoError(t, cfg.Save(path))

	loaded, err := config.Load(path)
	require.NoError(t, err)

	version, ok := loaded.PluginPin("claude-code", "acme/tool")
	require.True(t, ok)
	require.Equal(t, "1.0.0", version)

	version, ok = loaded.PluginPin("opencode", "acme/tool")
	require.True(t, ok)
	require.Equal(t, "2.1.0", version)

	_, ok = loaded.PluginPin("gemini-cli", "acme/tool")
	require.False(t, ok)

	loaded.UnsetPluginPin("claude-code", "acme/tool")
	_, ok = loaded.PluginPin("claude-code", "acme/tool")
	require.False(t, ok)

	require.NoError(t, loaded.Save(path))

	reloaded, err := config.Load(path)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"acme/tool": "2.1.0"}, reloaded.Agents["opencode"].PluginPins)
	require.Nil(t, reloaded.Agents["claude-code"].PluginPins)
}

func TestPluginPinValidation(t *testing.T) {
	t.Parallel()

	bad := map[string]string{
		"missing marketplace": "",
		"no slash":            "acme",
		"empty marketplace":   "/tool",
		"empty name":          "acme/",
		"nested name":         "acme/tool/extra",
		"absolute":            "/acme/tool",
		"dotdot marketplace":  "../acme/tool",
		"dotdot name":         "acme/../tool",
	}

	for name, key := range bad {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			require.Error(t, config.ValidatePluginPin(key, "1.0.0"))
		})
	}

	badVersions := map[string]string{
		"empty":     "",
		"slash":     "1.0/0",
		"traversal": "..",
		"space":     "1 0",
		"unicode":   "версия",
	}

	for name, version := range badVersions {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			require.Error(t, config.ValidatePluginPin("acme/tool", version))
		})
	}

	require.NoError(t, config.ValidatePluginPin("acme/tool", "v1.2.3-beta_4"))

	cfg := config.Default()
	require.Error(t, cfg.SetPluginPin("claude-code", "acme", "1.0.0"))
	require.Error(t, cfg.SetPluginPin("claude-code", "acme/tool", "../1.0.0"))
}

func TestLoadRejectsInvalidPin(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), config.FileName)
	require.NoError(t, os.WriteFile(path, []byte(`{
  "version": 2,
  "agents": {"claude-code": {"enabled": true, "plugin_pins": {"acme/tool": "../evil"}}}
}`), 0o600))

	_, err := config.Load(path)
	require.ErrorContains(t, err, "invalid version")
}
