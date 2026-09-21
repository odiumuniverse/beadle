package agent

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

type mcpCodec struct {
	owned  []string
	decode func(entry map[string]any) (mcp.Server, bool)
	encode func(server mcp.Server) map[string]any
}

const mcpServersPointer = "/mcpServers"

type mcpPlacement struct {
	primary string
	shadow  string
}

type mcpSurface struct {
	file    func() string
	pointer string
	codec   mcpCodec
	target  func(data []byte) (mcpPlacement, error)
	traits  Traits
}

func fixedPath(path string) func() string {
	return func() string { return path }
}

func (s *mcpSurface) Kind() kind.ID { return kind.MCP }

func (s *mcpSurface) Path() string { return s.file() }

func (s *mcpSurface) WatchPaths() []string { return []string{s.file()} }

func (s *mcpSurface) Traits() Traits { return s.traits }

func (s *mcpSurface) resolve(data []byte) (mcpPlacement, error) {
	if s.target == nil {
		return mcpPlacement{primary: s.pointer}, nil
	}

	return s.target(data)
}

func (s *mcpSurface) Read(context.Context) (Snapshot, error) {
	path := s.file()

	data, present, err := readFile(path)
	if err != nil {
		return Snapshot{}, err
	}

	if !present {
		return Snapshot{Items: kind.Items{}}, nil
	}

	placement, err := s.resolve(data)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%s: %w", path, err)
	}

	entries, err := s.merged(data, placement)
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

		placement, err := s.resolve(data)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", path, err)
		}

		ops, err := s.writeOps(data, placement, desired)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", path, err)
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

func (s *mcpSurface) merged(data []byte, placement mcpPlacement) (map[string]map[string]any, error) {
	entries, _, err := decodeObjects(data, placement.primary)
	if err != nil {
		return nil, err
	}

	if entries == nil {
		entries = map[string]map[string]any{}
	}

	if placement.shadow == "" {
		return entries, nil
	}

	shadow, _, err := decodeObjects(data, placement.shadow)
	if err != nil {
		return nil, err
	}

	for name, entry := range shadow {
		if existing, exists := entries[name]; exists {
			if _, managed := s.codec.decode(existing); managed {
				continue
			}
		}

		entries[name] = entry
	}

	return entries, nil
}

func (s *mcpSurface) writeOps(data []byte, placement mcpPlacement, desired kind.Items) ([]patchOp, error) {
	entries, found, err := decodeObjects(data, placement.primary)
	if err != nil {
		return nil, err
	}

	shadow := map[string]map[string]any{}

	if placement.shadow != "" {
		shadow, _, err = decodeObjects(data, placement.shadow)
		if err != nil {
			return nil, err
		}
	}

	var ops []patchOp

	if !found {
		ops, err = ensureOps(data, placement.primary)
		if err != nil {
			return nil, err
		}
	}

	for _, name := range unionNames(entries, shadow, desired) {
		nameOps, err := s.nameOps(placement, name, entries, shadow, desired)
		if err != nil {
			return nil, err
		}

		ops = append(ops, nameOps...)
	}

	return ops, nil
}

func (s *mcpSurface) nameOps(
	placement mcpPlacement, name string, entries, shadow map[string]map[string]any, desired kind.Items,
) ([]patchOp, error) {
	primaryEntry, inPrimary := entries[name]
	shadowEntry, inShadow := shadow[name]

	primaryManaged := inPrimary && s.managed(primaryEntry)
	shadowManaged := placement.shadow != "" && inShadow && s.managed(shadowEntry)

	if _, keep := desired[name]; !keep {
		return removeNameOps(placement, name, primaryManaged, shadowManaged), nil
	}

	if inPrimary {
		op, needed, err := s.entryOp(placement.primary, name, entries, desired)
		if err != nil {
			return nil, err
		}

		if !needed {
			return nil, nil
		}

		ops := []patchOp{op}

		if shadowManaged {
			ops = append(ops, removeOp(pointerJoin(placement.shadow, name)))
		}

		return ops, nil
	}

	target, source := placement.primary, entries
	if shadowManaged {
		target, source = placement.shadow, shadow
	}

	op, needed, err := s.entryOp(target, name, source, desired)
	if err != nil || !needed {
		return nil, err
	}

	return []patchOp{op}, nil
}

func removeNameOps(placement mcpPlacement, name string, primaryManaged, shadowManaged bool) []patchOp {
	var ops []patchOp

	if primaryManaged {
		ops = append(ops, removeOp(pointerJoin(placement.primary, name)))
	}

	if shadowManaged {
		ops = append(ops, removeOp(pointerJoin(placement.shadow, name)))
	}

	return ops
}

func (s *mcpSurface) managed(entry map[string]any) bool {
	_, ok := s.codec.decode(entry)

	return ok
}

func (s *mcpSurface) entryOp(pointer, name string, entries map[string]map[string]any, desired kind.Items) (patchOp, bool, error) {
	path := pointerJoin(pointer, name)
	entry, exists := entries[name]

	want, ok := desired[name]
	if !ok {
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

func unionNames(entries, shadow map[string]map[string]any, desired kind.Items) []string {
	names := map[string]struct{}{}

	for name := range desired {
		names[name] = struct{}{}
	}

	for name := range entries {
		names[name] = struct{}{}
	}

	for name := range shadow {
		names[name] = struct{}{}
	}

	return slices.Sorted(maps.Keys(names))
}

func ensureOps(data []byte, pointer string) ([]patchOp, error) {
	var ops []patchOp

	var raw any

	for _, prefix := range pointerPrefixes(pointer) {
		found, err := decodePointer(data, prefix, &raw)
		if err != nil {
			return nil, err
		}

		if !found {
			ops = append(ops, addOp(prefix, map[string]any{}))
		}

		raw = nil
	}

	return ops, nil
}

func pointerPrefixes(pointer string) []string {
	tokens := strings.Split(strings.TrimPrefix(pointer, "/"), "/")

	prefixes := make([]string, 0, len(tokens))

	for i := range tokens {
		prefixes = append(prefixes, "/"+strings.Join(tokens[:i+1], "/"))
	}

	return prefixes
}
