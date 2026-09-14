package secret

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/odiumuniverse/agents-sync/pkg/mcp"
)

func Extract(servers mcp.Servers, store *Store) (mcp.Servers, bool, error) {
	if len(servers) == 0 {
		return servers, false, nil
	}

	tree, err := decode(servers)
	if err != nil {
		return servers, false, err
	}

	replaced := 0

	out := walkStrings(tree, "", func(key, value string) string {
		if name, ok := ParseEnvRef(value); ok {
			if store.Has(name) {
				replaced++

				return Ref(name)
			}

			return value
		}

		if !IsSecret(key, value) {
			return value
		}

		name := store.NameFor(key, value)
		store.Set(name, value)

		replaced++

		return Ref(name)
	})

	if replaced == 0 {
		return servers, false, nil
	}

	result, err := encode(out)
	if err != nil {
		return servers, false, err
	}

	return result, true, nil
}

func Resolve(servers mcp.Servers, store *Store, mode string) (mcp.Servers, []string, error) {
	if len(servers) == 0 {
		return servers, nil, nil
	}

	tree, err := decode(servers)
	if err != nil {
		return servers, nil, err
	}

	missing := map[string]struct{}{}
	refs := 0

	out := walkStrings(tree, "", func(_, value string) string {
		name, ok := ParseRef(value)
		if !ok {
			return value
		}

		refs++

		if mode == ModeEnv {
			return EnvRef(name)
		}

		secret, ok := store.Get(name)
		if !ok {
			missing[name] = struct{}{}

			return value
		}

		return secret
	})

	if refs == 0 {
		return servers, nil, nil
	}

	result, err := encode(out)
	if err != nil {
		return servers, nil, err
	}

	return result, slices.Sorted(maps.Keys(missing)), nil
}

func Refs(servers mcp.Servers) ([]string, error) {
	if len(servers) == 0 {
		return nil, nil
	}

	tree, err := decode(servers)
	if err != nil {
		return nil, err
	}

	names := map[string]struct{}{}

	walkStrings(tree, "", func(_, value string) string {
		if name, ok := ParseRef(value); ok {
			names[name] = struct{}{}
		}

		return value
	})

	return slices.Sorted(maps.Keys(names)), nil
}

func walkStrings(node any, key string, fn func(key, value string) string) any {
	switch typed := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))

		for _, child := range slices.Sorted(maps.Keys(typed)) {
			out[child] = walkStrings(typed[child], child, fn)
		}

		return out
	case []any:
		out := make([]any, len(typed))

		for i, item := range typed {
			out[i] = walkStrings(item, key, fn)
		}

		return out
	case string:
		return fn(key, typed)
	default:
		return node
	}
}

func decode(servers mcp.Servers) (any, error) {
	data, err := json.Marshal(servers)
	if err != nil {
		return nil, fmt.Errorf("encode servers: %w", err)
	}

	var tree any
	if err := json.Unmarshal(data, &tree); err != nil {
		return nil, fmt.Errorf("decode servers: %w", err)
	}

	return tree, nil
}

func encode(tree any) (mcp.Servers, error) {
	data, err := json.Marshal(tree)
	if err != nil {
		return nil, fmt.Errorf("encode servers: %w", err)
	}

	servers := mcp.Servers{}
	if err := json.Unmarshal(data, &servers); err != nil {
		return nil, fmt.Errorf("decode servers: %w", err)
	}

	return servers, nil
}
