package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/odiumuniverse/verger/pkg/hostpath"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
)

// PiMCPAdapterPackage is the npm package that adds MCP support to Pi. Pi
// itself has no built-in MCP: ~/.pi/agent/mcp.json is a Pi-owned override
// file that only this adapter reads (pi-mcp-adapter README: «Pi global
// override (~/.pi/agent/mcp.json by default)»).
const PiMCPAdapterPackage = "pi-mcp-adapter"

// piSettingsFile is the settings document the resolver lists among the probes;
// its contents are the only thing a path cannot answer.
const piSettingsFile = "settings.json"

// PiMCPAdapterPresent reports whether the third-party pi-mcp-adapter is
// registered for Pi. Detection is a best-effort check of where `pi install`
// records a package: the packages/extensions lists in the settings file and
// the managed npm catalog (node_modules/<package>), in the user scope
// (~/.pi/agent) and, when cwd is given, the project scope (<cwd>/.pi). A
// package hand-installed into a global npm root leaves no file marker beadle
// can see; callers word their findings accordingly.
func PiMCPAdapterPresent(home, cwd string) bool {
	// The resolver names the probe paths; the *content* scan of settings.json
	// stays here, because a path cannot say whether a file mentions the
	// adapter package.
	//
	// Live probe (pi 0.74.2, macOS, isolated HOME,
	// `pi install npm:pi-mcp-adapter`): the user-scope install writes ONE
	// thing, the registration in <agentDir>/settings.json, and puts the package
	// payload in the GLOBAL npm root — not in <agentDir>/node_modules and not
	// in <agentDir>/npm/node_modules. So the resolver's user set is right and
	// beadle's old `npm/node_modules` probe could never fire: it is dropped.
	// The payload's real home is a user-level npm prefix, which is outside
	// every surface beadle resolves, so an adapter that is not a pi install
	// target simply has no adapter.
	probes := surfaces(hostpath.Pi, home).AdapterProbes

	if piAdapterProbesPresent(probes) {
		return true
	}

	return cwd != "" && piAdapterProbesPresent(projectSurfaces(hostpath.Pi, cwd).AdapterProbes)
}

// piAdapterProbesPresent checks the resolver's probe list for one scope: the
// settings file is scanned, the rest are existence checks.
func piAdapterProbesPresent(probes []string) bool {
	for _, probe := range probes {
		if filepath.Base(probe) == piSettingsFile && piSettingsMentionAdapter(probe) {
			return true
		}

		if strings.Contains(filepath.ToSlash(probe), PiMCPAdapterPackage) && fsutil.Exists(probe) {
			return true
		}
	}

	return false
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
