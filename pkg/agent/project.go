package agent

import (
	"context"
	"path/filepath"

	"github.com/odiumuniverse/agents-sync/pkg/kind"
	"github.com/odiumuniverse/agents-sync/pkg/mcp"
)

func ClaudeProjectMCP(ctx context.Context, dir string) (kind.Items, bool, error) {
	surface := &mcpSurface{file: fixedPath(filepath.Join(dir, ".mcp.json")), pointer: mcpServersPointer, codec: claudeMCP}

	snap, err := surface.Read(ctx)
	if err != nil {
		return nil, false, err
	}

	return snap.Items, snap.Present, nil
}

func ClaudeLocalMCP(home, dir string) (kind.Items, bool, error) {
	data, present, err := readFile(filepath.Join(home, ".claude.json"))
	if err != nil || !present {
		return kind.Items{}, false, err
	}

	pointer := pointerJoin(pointerJoin("/projects", dir), "mcpServers")

	entries, found, err := decodeObjects(data, pointer)
	if err != nil {
		return nil, false, err
	}

	if !found || len(entries) == 0 {
		return kind.Items{}, false, nil
	}

	items := make(kind.Items, len(entries))

	for name, entry := range entries {
		if server, ok := claudeMCP.decode(entry); ok {
			items[name] = mcp.Encode(server)
		}
	}

	return items, true, nil
}
