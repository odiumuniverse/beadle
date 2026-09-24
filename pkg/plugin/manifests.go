package plugin

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
)

// stringList decodes a JSON field that is a string or a list of strings.
type stringList []string

func (l *stringList) UnmarshalJSON(data []byte) error {
	var one string

	if err := json.Unmarshal(data, &one); err == nil {
		if one == "" {
			*l = nil
		} else {
			*l = stringList{one}
		}

		return nil
	}

	var many []string

	if err := json.Unmarshal(data, &many); err != nil {
		return errors.New("expected a string or a list of strings")
	}

	filtered := make([]string, 0, len(many))

	for _, item := range many {
		if item != "" {
			filtered = append(filtered, item)
		}
	}

	*l = stringList(filtered)

	return nil
}

// manifestState classifies a manifest read: missing (the directory is not a
// plugin of that host), parsed, or invalid (a broken manifest is a warning and
// the plugin is skipped).
type manifestState int

const (
	manifestMissing manifestState = iota
	manifestParsed
	manifestInvalid
)

// readJSONManifest reads and parses one plugin manifest file.
func readJSONManifest(path, source string, value any) (manifestState, []string) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the path is built from the caller-provided home directory
	if errors.Is(err, fs.ErrNotExist) {
		return manifestMissing, nil
	}

	if err != nil {
		return manifestInvalid, []string{warnf(source, "cannot read %s: %v", path, err)}
	}

	if err := json.Unmarshal(data, value); err != nil {
		return manifestInvalid, []string{warnf(source, "cannot parse %s: %v", path, err)}
	}

	return manifestParsed, nil
}

// fallback returns value when it is non-empty and fallback otherwise.
func fallback(value, alternative string) string {
	if value != "" {
		return value
	}

	return alternative
}

// resolveMCPNames lists the MCP server names of a plugin payload.
func resolveMCPNames(source, installPath string) ([]string, []string) {
	data, _, warns, ok := MCPDocument(source, installPath)

	return mcpServerNames(data), validWarns(warns, ok)
}

// validWarns returns the warnings only when the document was readable.
func validWarns(warns []string, ok bool) []string {
	if !ok || len(warns) == 0 {
		return nil
	}

	return warns
}

// isDir reports whether path is an existing directory.
func isDir(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.IsDir()
}

// isRegularFile reports whether path is an existing regular file (a symlink
// to one counts).
func isRegularFile(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.Mode().IsRegular()
}
