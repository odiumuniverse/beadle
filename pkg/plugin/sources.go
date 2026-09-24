package plugin

import (
	"errors"
	"fmt"
)

// Source IDs name the plugin hosts beadle reads. They match the agent IDs of
// pkg/agent; a test keeps the two lists in sync.
const (
	SourceClaudeCode     = "claude-code"
	SourceCodex          = "codex"
	SourceGeminiCLI      = "gemini-cli"
	SourceAntigravityCLI = "antigravity-cli"
	SourceCursor         = "cursor"
)

// sourceOrder lists the plugin hosts in host-registration order. The order
// breaks ties when the same plugin namespace is found in several hosts:
// the first source wins, and the others are reported.
var sourceOrder = []string{
	SourceClaudeCode,
	SourceGeminiCLI,
	SourceAntigravityCLI,
	SourceCursor,
	SourceCodex,
}

// SourceHosts returns the supported plugin host IDs in host-registration
// order.
func SourceHosts() []string {
	return append([]string(nil), sourceOrder...)
}

// ReadAll reads the installed plugins of every supported host. The Claude
// Code registry keeps its strict error semantics: a malformed registry fails
// the read. Every other host is best-effort: a broken or missing manifest is
// a warning and the plugin is skipped, so one broken host cannot stop the
// sync of the others.
func ReadAll(home string) (Manifest, error) {
	if home == "" {
		return Manifest{}, errors.New("empty home directory")
	}

	manifest, err := Read(home)
	if err != nil {
		return Manifest{}, err
	}

	for _, source := range sourceOrder {
		if source == SourceClaudeCode {
			continue
		}

		plugins, warns := readSource(source, home)

		manifest.Plugins = append(manifest.Plugins, plugins...)
		manifest.Warnings = append(manifest.Warnings, warns...)
	}

	sortPlugins(manifest.Plugins)

	return manifest, nil
}

func readSource(source, home string) ([]Plugin, []string) {
	switch source {
	case SourceCodex:
		return readCodex(home)
	case SourceGeminiCLI:
		return readGemini(home)
	case SourceAntigravityCLI:
		return readAntigravity(home)
	case SourceCursor:
		return readCursor(home)
	default:
		return nil, nil
	}
}

// warnf renders one reader warning with its source prefix.
func warnf(source, format string, args ...any) string {
	return source + ": " + fmt.Sprintf(format, args...)
}
