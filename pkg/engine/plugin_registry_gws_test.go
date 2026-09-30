package engine_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// Helpers for a host plugin cache. The doctor still reads the host registries
// and resolves which copy of one key wins, so a test needs a real registry on
// disk - not a mocked plugin.
func claudePluginsDir(home string) string {
	return filepath.Join(home, ".claude", "plugins")
}

func registryPath(home string) string {
	return filepath.Join(claudePluginsDir(home), "installed_plugins.json")
}

func pluginTree(t *testing.T, home, marketplace, name, version string) string {
	t.Helper()

	dir := filepath.Join(claudePluginsDir(home), "cache", marketplace, name, version)
	write(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), fmt.Sprintf(`{"name": %q, "version": %q}`, name, version))

	plugins := readRegistry(t, home)
	plugins[name+"@"+marketplace] = []map[string]any{{
		"scope":       "user",
		"installPath": dir,
		"version":     version,
	}}

	writeRegistry(t, home, plugins)

	return dir
}

func readRegistry(t *testing.T, home string) map[string][]map[string]any {
	t.Helper()

	data, err := os.ReadFile(registryPath(home))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string][]map[string]any{}
	}

	if err != nil {
		t.Fatalf("read registry: %v", err)
	}

	var doc struct {
		Plugins map[string][]map[string]any `json:"plugins"`
	}

	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal registry: %v", err)
	}

	if doc.Plugins == nil {
		doc.Plugins = map[string][]map[string]any{}
	}

	return doc.Plugins
}

func registryBytes(t *testing.T, plugins map[string][]map[string]any) []byte {
	t.Helper()

	data, err := json.MarshalIndent(map[string]any{"plugins": plugins}, "", "  ")
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}

	return append(data, '\n')
}

func writeRegistry(t *testing.T, home string, plugins map[string][]map[string]any) {
	t.Helper()

	if err := os.MkdirAll(claudePluginsDir(home), 0o750); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}

	if err := os.WriteFile(registryPath(home), registryBytes(t, plugins), 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}
}

func removeFromRegistry(t *testing.T, home, marketplace, name string) {
	t.Helper()

	plugins := readRegistry(t, home)
	delete(plugins, name+"@"+marketplace)

	writeRegistry(t, home, plugins)
}

func containsWarning(warnings []string, substr string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, substr) {
			return true
		}
	}

	return false
}

// enableAgents turns agents on in the vault config and saves it - the shape
// every test that needs a host enabled writes.
func enableAgents(t *testing.T, f *fixture, ids ...string) {
	t.Helper()

	for _, id := range ids {
		f.config.Enable(id)
	}

	So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)
}
