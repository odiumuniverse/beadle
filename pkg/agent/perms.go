package agent

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/permission"
)

type permCodec interface {
	read(raw any) kind.Items
	ops(pointer string, raw any, found bool, desired kind.Items) ([]patchOp, error)
	project(key, effect string) (string, bool)
}

type permPlacement struct {
	v2      bool
	primary string
	shadow  string
}

type permResolution struct {
	pointer     string
	codec       permCodec
	shadow      string
	shadowCodec permCodec
}

type permSurface struct {
	file    func() string
	pointer string
	codec   permCodec
	v2Codec permCodec
	target  func(data []byte) (permPlacement, error)
	traits  Traits
}

func (s *permSurface) Kind() kind.ID { return kind.Permissions }

func (s *permSurface) Path() string { return s.file() }

func (s *permSurface) WatchPaths() []string { return []string{s.file()} }

func (s *permSurface) Traits() Traits { return s.traits }

func (s *permSurface) resolve(data []byte) (permResolution, error) {
	if s.target == nil {
		return permResolution{pointer: s.pointer, codec: s.codec}, nil
	}

	placement, err := s.target(data)
	if err != nil {
		return permResolution{}, err
	}

	resolved := permResolution{pointer: placement.primary, codec: s.codec}
	if placement.v2 {
		resolved.codec = s.v2Codec
		resolved.shadow = placement.shadow
		resolved.shadowCodec = s.codec
	}

	return resolved, nil
}

func (s *permSurface) Read(context.Context) (Snapshot, error) {
	path := s.file()

	data, present, err := readFile(path)
	if err != nil {
		return Snapshot{}, err
	}

	if !present {
		return Snapshot{Items: kind.Items{}}, nil
	}

	resolved, err := s.resolve(data)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%s: %w", path, err)
	}

	items, err := readPermItems(data, resolved)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%s: %w", path, err)
	}

	return Snapshot{Items: items, Present: true}, nil
}

func (s *permSurface) Write(_ context.Context, desired kind.Items) error {
	path := s.file()

	return updateFile(path, 0o600, func(data []byte, present bool) ([]byte, bool, error) {
		if !present {
			return nil, false, fmt.Errorf("%s: %w", path, ErrNotConfigured)
		}

		resolved, err := s.resolve(data)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", path, err)
		}

		ops, err := writePermOps(data, resolved, desired)
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

func (s *permSurface) Project(key string, value []byte) (string, []byte, bool) {
	pkey, ok := s.codec.project(key, string(value))

	if !ok && s.v2Codec != nil {
		pkey, ok = s.v2Codec.project(key, string(value))
	}

	if !ok {
		return "", nil, false
	}

	return pkey, value, true
}

func readPermItems(data []byte, resolved permResolution) (kind.Items, error) {
	var raw any

	if _, err := decodePointer(data, resolved.pointer, &raw); err != nil {
		return nil, err
	}

	items := resolved.codec.read(raw)

	if resolved.shadow == "" {
		return items, nil
	}

	raw = nil

	if _, err := decodePointer(data, resolved.shadow, &raw); err != nil {
		return nil, err
	}

	for key, value := range resolved.shadowCodec.read(raw) {
		if _, exists := items[key]; !exists {
			items[key] = value
		}
	}

	return items, nil
}

func writePermOps(data []byte, resolved permResolution, desired kind.Items) ([]patchOp, error) {
	var primaryRaw, shadowRaw any

	primaryFound, err := decodePointer(data, resolved.pointer, &primaryRaw)
	if err != nil {
		return nil, err
	}

	shadowFound := false

	if resolved.shadow != "" {
		shadowFound, err = decodePointer(data, resolved.shadow, &shadowRaw)
		if err != nil {
			return nil, err
		}
	}

	primary := resolved.codec.read(primaryRaw)

	shadow := kind.Items{}
	if resolved.shadowCodec != nil {
		shadow = resolved.shadowCodec.read(shadowRaw)
	}

	primaryDesired, shadowDesired := permTargets(desired, primary, shadow)

	ops, err := resolved.codec.ops(resolved.pointer, primaryRaw, primaryFound, primaryDesired)
	if err != nil {
		return nil, err
	}

	if resolved.shadow == "" {
		return ops, nil
	}

	shadowOps, err := resolved.shadowCodec.ops(resolved.shadow, shadowRaw, shadowFound, shadowDesired)
	if err != nil {
		return nil, err
	}

	return append(ops, shadowOps...), nil
}

func permTargets(desired, primary, shadow kind.Items) (kind.Items, kind.Items) {
	primaryDesired := kind.Items{}
	shadowDesired := kind.Items{}

	for _, key := range unionItemKeys(desired, unionItems(primary, shadow)) {
		want, keep := desired[key]
		_, inPrimary := primary[key]
		_, inShadow := shadow[key]

		switch {
		case !keep:
		case inPrimary:
			primaryDesired[key] = want

			if inShadow && string(primary[key]) == string(want) {
				shadowDesired[key] = shadow[key]
			}
		case inShadow:
			shadowDesired[key] = want
		default:
			primaryDesired[key] = want
		}
	}

	return primaryDesired, shadowDesired
}

func unionItems(a, b kind.Items) kind.Items {
	out := make(kind.Items, len(a)+len(b))

	maps.Copy(out, a)
	maps.Copy(out, b)

	return out
}

func withContainer(pointer string, found bool, ops []patchOp) []patchOp {
	if found || len(ops) == 0 {
		return ops
	}

	return append([]patchOp{addOp(pointer, map[string]any{})}, ops...)
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

func (c listCodec) read(raw any) kind.Items {
	block, _ := raw.(map[string]any)

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

func (c listCodec) ops(pointer string, raw any, found bool, desired kind.Items) ([]patchOp, error) {
	block, ok := raw.(map[string]any)
	if found && !ok {
		return nil, fmt.Errorf("%s is not an object, refusing to rewrite it", pointer)
	}

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

	return withContainer(pointer, found, ops), nil
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
