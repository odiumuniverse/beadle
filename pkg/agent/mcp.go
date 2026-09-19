package agent

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

type mcpCodec struct {
	owned  []string
	decode func(entry map[string]any) (mcp.Server, bool)
	encode func(server mcp.Server) map[string]any
}

const mcpServersPointer = "/mcpServers"

type mcpSurface struct {
	file    func() string
	pointer string
	codec   mcpCodec
	traits  Traits
}

func fixedPath(path string) func() string {
	return func() string { return path }
}

func (s *mcpSurface) Kind() kind.ID { return kind.MCP }

func (s *mcpSurface) Path() string { return s.file() }

func (s *mcpSurface) WatchPaths() []string { return []string{s.file()} }

func (s *mcpSurface) Traits() Traits { return s.traits }

func (s *mcpSurface) Read(context.Context) (Snapshot, error) {
	path := s.file()

	data, present, err := readFile(path)
	if err != nil {
		return Snapshot{}, err
	}

	if !present {
		return Snapshot{Items: kind.Items{}}, nil
	}

	entries, _, err := decodeObjects(data, s.pointer)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%s: %w", path, err)
	}

	items := make(kind.Items, len(entries))

	for name, entry := range entries {
		if server, ok := s.codec.decode(entry); ok {
			items[name] = mcp.Encode(server)
		}
	}

	return Snapshot{Items: items, Present: true}, nil
}

func (s *mcpSurface) Write(_ context.Context, desired kind.Items) error {
	path := s.file()

	return updateFile(path, 0o600, func(data []byte, present bool) ([]byte, bool, error) {
		if !present {
			return nil, false, fmt.Errorf("%s: %w", path, ErrNotConfigured)
		}

		entries, found, err := decodeObjects(data, s.pointer)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", path, err)
		}

		ops, err := s.ops(entries, found, desired)
		if err != nil {
			return nil, false, err
		}

		if len(ops) == 0 {
			return nil, false, nil
		}

		out, err := applyPatch(data, ops)
		if err != nil {
			return nil, false, fmt.Errorf("patch %s: %w", path, err)
		}

		return out, true, nil
	})
}

func (s *mcpSurface) ops(entries map[string]map[string]any, found bool, desired kind.Items) ([]patchOp, error) {
	var ops []patchOp

	if !found {
		ops = append(ops, addOp(s.pointer, map[string]any{}))
	}

	for _, name := range s.names(entries, desired) {
		op, needed, err := s.entryOp(name, entries, desired)
		if err != nil {
			return nil, err
		}

		if needed {
			ops = append(ops, op)
		}
	}

	return ops, nil
}

func (s *mcpSurface) names(entries map[string]map[string]any, desired kind.Items) []string {
	names := map[string]struct{}{}

	for name := range desired {
		names[name] = struct{}{}
	}

	for name, entry := range entries {
		if _, managed := s.codec.decode(entry); managed {
			names[name] = struct{}{}
		}
	}

	return slices.Sorted(maps.Keys(names))
}

func (s *mcpSurface) entryOp(name string, entries map[string]map[string]any, desired kind.Items) (patchOp, bool, error) {
	path := pointerJoin(s.pointer, name)
	entry, exists := entries[name]

	want, keep := desired[name]
	if !keep {
		return removeOp(path), true, nil
	}

	if exists {
		if current, managed := s.codec.decode(entry); managed && bytes.Equal(mcp.Encode(current), want) {
			return patchOp{}, false, nil
		}
	}

	server, err := mcp.Decode(want)
	if err != nil {
		return patchOp{}, false, fmt.Errorf("server %s: %w", name, err)
	}

	rendered := s.codec.encode(server)

	for key, value := range entry {
		if !slices.Contains(s.codec.owned, key) {
			rendered[key] = value
		}
	}

	if exists && jsonEqual(entry, rendered) {
		return patchOp{}, false, nil
	}

	return addOp(path, rendered), true, nil
}

func (s *mcpSurface) Project(key string, value []byte) (string, []byte, bool) {
	server, err := mcp.Decode(value)
	if err != nil {
		return key, value, true
	}

	entry, err := roundTrip(s.codec.encode(server))
	if err != nil {
		return key, value, true
	}

	back, ok := s.codec.decode(entry)
	if !ok {
		return "", nil, false
	}

	return key, mcp.Encode(back), true
}
