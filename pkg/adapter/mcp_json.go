package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
	"github.com/odiumuniverse/agents-sync/pkg/mcp"
)

func exportMCPDocument(path string, decode func(map[string]any) mcp.Server) (mcp.Servers, bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: agent config paths are resolved by the adapter
	if errors.Is(err, fs.ErrNotExist) {
		return mcp.Servers{}, false, nil
	}

	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}

	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}

	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", path, err)
	}

	servers := make(mcp.Servers, len(doc.MCPServers))
	for name, entry := range doc.MCPServers {
		servers[name] = decode(entry)
	}

	return servers, true, nil
}

func applyMCPDocument(path string, servers mcp.Servers, render func(mcp.Server) map[string]any) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: agent config paths are resolved by the adapter
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("agent mcp config %s: %w", path, ErrNotConfigured)
	}

	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	rendered := make(map[string]any, len(servers))
	for name, server := range servers {
		rendered[name] = render(server)
	}

	out, err := patchJSON(data, "/mcpServers", rendered)
	if err != nil {
		return fmt.Errorf("patch %s: %w", path, err)
	}

	if err := fsutil.WriteFileAtomic(path, out, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}
