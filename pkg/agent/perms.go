package agent

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/odiumuniverse/agents-sync/pkg/kind"
	"github.com/odiumuniverse/agents-sync/pkg/permission"
)

type permCodec interface {
	read(block map[string]any) kind.Items
	ops(pointer string, block map[string]any, desired kind.Items) []patchOp
	project(key, effect string) (string, bool)
}

type permSurface struct {
	file    func() string
	pointer string
	codec   permCodec
	traits  Traits
}

func (s *permSurface) Kind() kind.ID { return kind.Permissions }

func (s *permSurface) Path() string { return s.file() }

func (s *permSurface) WatchPaths() []string { return []string{s.file()} }

func (s *permSurface) Traits() Traits { return s.traits }

func (s *permSurface) Read(context.Context) (Snapshot, error) {
	path := s.file()

	data, present, err := readFile(path)
	if err != nil {
		return Snapshot{}, err
	}

	if !present {
		return Snapshot{Items: kind.Items{}}, nil
	}

	var raw any

	if _, err := decodePointer(data, s.pointer, &raw); err != nil {
		return Snapshot{}, fmt.Errorf("%s: %w", path, err)
	}

	block, _ := raw.(map[string]any)

	return Snapshot{Items: s.codec.read(block), Present: true}, nil
}

func (s *permSurface) Write(_ context.Context, desired kind.Items) error {
	path := s.file()

	data, present, err := readFile(path)
	if err != nil {
		return err
	}

	if !present {
		return fmt.Errorf("%s: %w", path, ErrNotConfigured)
	}

	var raw any

	found, err := decodePointer(data, s.pointer, &raw)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	block, isObject := raw.(map[string]any)
	if found && !isObject {
		return fmt.Errorf("%s: %s is not an object, refusing to rewrite it", path, s.pointer)
	}

	ops := s.codec.ops(s.pointer, block, desired)
	if len(ops) == 0 {
		return nil
	}

	if !found {
		ops = append([]patchOp{addOp(s.pointer, map[string]any{})}, ops...)
	}

	out, err := applyPatch(data, ops)
	if err != nil {
		return fmt.Errorf("patch %s: %w", path, err)
	}

	return writeFile(path, out, 0o600)
}

func (s *permSurface) Project(key string, value []byte) (string, []byte, bool) {
	pkey, ok := s.codec.project(key, string(value))
	if !ok {
		return "", nil, false
	}

	return pkey, value, true
}

type effectList struct {
	key    string
	effect string
}

type listCodec struct {
	lists  []effectList
	parse  func(rule string) (string, bool)
	render func(key string) (string, bool)
}

func (c listCodec) read(block map[string]any) kind.Items {
	rules := permission.Rules{}

	for _, list := range c.lists {
		for _, rule := range toStringSlice(block[list.key]) {
			if key, ok := c.parse(rule); ok {
				rules.Set(key, list.effect)
			}
		}
	}

	return rulesToItems(rules)
}

func (c listCodec) ops(pointer string, block map[string]any, desired kind.Items) []patchOp {
	var ops []patchOp

	for _, list := range c.lists {
		current := toStringSlice(block[list.key])
		next := c.rebuild(current, list.effect, desired)

		if _, exists := block[list.key]; slices.Equal(current, next) && (exists || len(next) == 0) {
			continue
		}

		if next == nil {
			next = []string{}
		}

		ops = append(ops, addOp(pointerJoin(pointer, list.key), next))
	}

	return ops
}

func (c listCodec) rebuild(current []string, effect string, desired kind.Items) []string {
	var out []string

	used := map[string]bool{}

	for _, rule := range current {
		key, managed := c.parse(rule)

		switch {
		case !managed:
			out = append(out, rule)
		case string(desired[key]) == effect && !used[key]:
			out = append(out, rule)
			used[key] = true
		}
	}

	for _, key := range desired.Keys() {
		if string(desired[key]) != effect || used[key] {
			continue
		}

		if rule, ok := c.render(key); ok {
			out = append(out, rule)
		}
	}

	return out
}

func (c listCodec) project(key, effect string) (string, bool) {
	supported := slices.ContainsFunc(c.lists, func(list effectList) bool { return list.effect == effect })
	if !supported {
		return "", false
	}

	rule, ok := c.render(key)
	if !ok {
		return "", false
	}

	return c.parse(rule)
}

func rulesToItems(rules permission.Rules) kind.Items {
	items := make(kind.Items, len(rules))

	for key, effect := range rules {
		items[key] = []byte(effect)
	}

	return items
}

func validEffect(effect string) bool {
	switch effect {
	case permission.EffectAllow, permission.EffectAsk, permission.EffectDeny:
		return true
	default:
		return false
	}
}

func unionItemKeys(a, b kind.Items) []string {
	keys := make(map[string]struct{}, len(a)+len(b))

	for key := range a {
		keys[key] = struct{}{}
	}

	for key := range b {
		keys[key] = struct{}{}
	}

	return slices.Sorted(maps.Keys(keys))
}
