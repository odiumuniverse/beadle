package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
)

// PiMCPAdapterPackage is the npm package that adds MCP support to Pi. Pi
// itself has no built-in MCP: ~/.pi/agent/mcp.json is a Pi-owned override
// file that only this adapter reads (pi-mcp-adapter README: «Pi global
// override (~/.pi/agent/mcp.json by default)»).
const PiMCPAdapterPackage = "pi-mcp-adapter"

// PiMCPAdapterPresent reports whether the third-party pi-mcp-adapter is
// registered for Pi. Detection is a best-effort check of where `pi install`
// records a package: the packages/extensions lists in the settings file and
// the managed npm catalog (node_modules/<package>), in the user scope
// (~/.pi/agent) and, when cwd is given, the project scope (<cwd>/.pi). A
// package hand-installed into a global npm root leaves no file marker beadle
// can see; callers word their findings accordingly.
func PiMCPAdapterPresent(home, cwd string) bool {
	if piAdapterDirPresent(filepath.Join(home, ".pi", "agent")) {
		return true
	}

	return cwd != "" && piAdapterDirPresent(filepath.Join(cwd, ".pi"))
}

// piAdapterDirPresent checks one Pi config directory (user or project scope).
func piAdapterDirPresent(dir string) bool {
	if piSettingsMentionAdapter(filepath.Join(dir, "settings.json")) {
		return true
	}

	return slices.ContainsFunc([]string{
		filepath.Join(dir, "npm", "node_modules", PiMCPAdapterPackage),
		filepath.Join(dir, "extensions", PiMCPAdapterPackage),
	}, fsutil.Exists)
}

// piSettingsMentionAdapter scans the packages and local extensions of a Pi
// settings file for the adapter package name; both the string and the
// {source: …} package forms are accepted.
func piSettingsMentionAdapter(path string) bool {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the path is built from the caller's home or cwd
	if err != nil {
		return false
	}

	var settings struct {
		Packages   []json.RawMessage `json:"packages"`
		Extensions []string          `json:"extensions"`
	}

	if err := json.Unmarshal(data, &settings); err != nil {
		return false
	}

	for _, raw := range settings.Packages {
		var source string
		if err := json.Unmarshal(raw, &source); err == nil {
			if strings.Contains(source, PiMCPAdapterPackage) {
				return true
			}

			continue
		}

		var entry struct {
			Source string `json:"source"`
		}

		if err := json.Unmarshal(raw, &entry); err == nil && strings.Contains(entry.Source, PiMCPAdapterPackage) {
			return true
		}
	}

	for _, extension := range settings.Extensions {
		if strings.Contains(extension, PiMCPAdapterPackage) {
			return true
		}
	}

	return false
}
