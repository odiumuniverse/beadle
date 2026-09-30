package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odiumuniverse/verger/pkg/hostpath"
)

// OpenCodeInlineAgents lists the agent names declared inline in the OpenCode
// config files (opencode.json(c) → agents) that have no agents/<name>.md or
// agent/<name>.md file next to the config. Inline agents are a surface
// beadle does not manage (A-28 §5.5), and both config files are read: a
// jsonc without the key does not hide a json that has one.
func OpenCodeInlineAgents(home string) ([]string, error) {
	// The candidate list and the root are the shared resolver's, so the inline
	// agents cannot land in a tree OpenCode does not read.
	resolved := surfaces(hostpath.OpenCode, home)
	dir := roots(hostpath.OpenCode, home).ConfigRoot

	names := map[string]bool{}

	for _, path := range append(resolved.MCPDocCandidates, resolved.MCPDoc) {
		data, err := os.ReadFile(path) //nolint:gosec // host directories are resolved by the adapter
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}

		var agents map[string]any

		found, err := decodePointer(data, "/agents", &agents)
		if err != nil {
			return nil, err
		}

		if !found {
			continue
		}

		for name := range agents {
			names[name] = true
		}
	}

	if len(names) == 0 {
		return nil, nil
	}

	for _, sub := range []string{"agents", "agent"} {
		entries, err := os.ReadDir(filepath.Join(dir, sub))
		if err != nil {
			continue
		}

		for _, entry := range entries {
			delete(names, strings.TrimSuffix(entry.Name(), ".md"))
		}
	}

	return slices.Sorted(maps.Keys(names)), nil
}
