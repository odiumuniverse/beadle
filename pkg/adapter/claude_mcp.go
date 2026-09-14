package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/odiumuniverse/agents-sync/pkg/mcp"
)

const claudeID = "claude-code"

var claudeCoreKeys = map[string]struct{}{
	fieldType:    {},
	fieldCommand: {},
	fieldArgs:    {},
	fieldEnv:     {},
	fieldURL:     {},
	fieldHeaders: {},
}

func claudeFromEntry(entry map[string]any) mcp.Server {
	server := singleCommandFromEntry(entry, claudeCoreKeys, claudeID)
	server.Env = mapValues(server.Env, claudeCanonicalizeRefs)
	server.Headers = mapValues(server.Headers, claudeCanonicalizeRefs)

	return server
}

func claudeToEntry(server mcp.Server) map[string]any {
	server.Env = mapValues(server.Env, claudeRenderRefs)
	server.Headers = mapValues(server.Headers, claudeRenderRefs)

	entry := make(map[string]any)

	if server.URL != "" && server.Transport != "" && server.Transport != transportStdio {
		entry[fieldType] = server.Transport
		entry[fieldURL] = server.URL
	} else {
		entry[fieldType] = transportStdio
		renderCommandFields(entry, server, fieldEnv)
	}

	if len(server.Headers) > 0 {
		entry[fieldHeaders] = server.Headers
	}

	mergeExtension(entry, server.Extensions, claudeID)

	return entry
}

func (c *ClaudeCode) ProjectMCP(dir string) (mcp.Servers, bool, error) {
	return exportMCPDocument(filepath.Join(dir, ".mcp.json"), claudeFromEntry)
}

func (c *ClaudeCode) LocalProjectMCP(dir string) (mcp.Servers, bool, error) {
	data, err := os.ReadFile(c.configPath())
	if errors.Is(err, fs.ErrNotExist) {
		return mcp.Servers{}, false, nil
	}

	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", c.configPath(), err)
	}

	var doc struct {
		Projects map[string]struct {
			MCPServers map[string]map[string]any `json:"mcpServers"`
		} `json:"projects"`
	}

	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", c.configPath(), err)
	}

	entry, ok := doc.Projects[dir]
	if !ok || len(entry.MCPServers) == 0 {
		return mcp.Servers{}, false, nil
	}

	servers := make(mcp.Servers, len(entry.MCPServers))
	for name, raw := range entry.MCPServers {
		servers[name] = claudeFromEntry(raw)
	}

	return servers, true, nil
}
