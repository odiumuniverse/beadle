package merge

import (
	"maps"
	"reflect"
	"slices"
)

type JSONConflict struct {
	Path  string `json:"path"`
	Base  any    `json:"base,omitempty"`
	Vault any    `json:"vault,omitempty"`
	Agent any    `json:"agent,omitempty"`
}

func JSON(base, vault, agent any) (any, []JSONConflict) {
	vaultObj, vaultOK := vault.(map[string]any)
	agentObj, agentOK := agent.(map[string]any)

	if vaultOK && agentOK {
		baseObj, _ := base.(map[string]any)

		return mergeObjects("", baseObj, vaultObj, agentObj)
	}

	switch {
	case reflect.DeepEqual(vault, agent):
		return vault, nil
	case reflect.DeepEqual(vault, base):
		return agent, nil
	case reflect.DeepEqual(agent, base):
		return vault, nil
	default:
		return vault, []JSONConflict{{Path: "", Base: base, Vault: vault, Agent: agent}}
	}
}

func mergeObjects(path string, base, vault, agent map[string]any) (map[string]any, []JSONConflict) {
	out := make(map[string]any)

	var conflicts []JSONConflict

	for _, key := range unionKeys(base, vault, agent) {
		value, present, keyConflicts := mergeKey(path, key, base, vault, agent)

		if present {
			out[key] = value
		}

		conflicts = append(conflicts, keyConflicts...)
	}

	return out, conflicts
}

func mergeKey(path, key string, base, vault, agent map[string]any) (any, bool, []JSONConflict) {
	baseValue, baseOK := base[key]
	vaultValue, vaultOK := vault[key]
	agentValue, agentOK := agent[key]

	keyPath := joinPath(path, key)

	switch {
	case !vaultOK && !agentOK:
		return nil, false, nil
	case vaultOK && agentOK && reflect.DeepEqual(vaultValue, agentValue):
		return vaultValue, true, nil
	case sameAsBase(baseOK, baseValue, vaultOK, vaultValue):
		return agentValue, agentOK, nil
	case sameAsBase(baseOK, baseValue, agentOK, agentValue):
		return vaultValue, vaultOK, nil
	}

	return mergeChanged(keyPath, baseValue, baseOK, vaultValue, vaultOK, agentValue, agentOK)
}

func mergeChanged(path string, baseValue any, baseOK bool, vaultValue any, vaultOK bool, agentValue any, agentOK bool) (any, bool, []JSONConflict) {
	if isObject(vaultOK, vaultValue) && isObject(agentOK, agentValue) {
		var baseObj map[string]any
		if baseOK {
			baseObj, _ = baseValue.(map[string]any)
		}

		vaultObj, _ := vaultValue.(map[string]any)
		agentObj, _ := agentValue.(map[string]any)

		child, conflicts := mergeObjects(path, baseObj, vaultObj, agentObj)

		return child, true, conflicts
	}

	conflict := JSONConflict{
		Path:  path,
		Base:  valueOrNil(baseOK, baseValue),
		Vault: valueOrNil(vaultOK, vaultValue),
		Agent: valueOrNil(agentOK, agentValue),
	}

	if !vaultOK {
		return nil, false, []JSONConflict{conflict}
	}

	return vaultValue, true, []JSONConflict{conflict}
}

func unionKeys(base, vault, agent map[string]any) []string {
	seen := make(map[string]struct{}, len(vault)+len(agent))

	for _, m := range []map[string]any{base, vault, agent} {
		for key := range m {
			seen[key] = struct{}{}
		}
	}

	return slices.Sorted(maps.Keys(seen))
}

func sameAsBase(basePresent bool, baseValue any, present bool, value any) bool {
	if basePresent != present {
		return false
	}

	if !basePresent {
		return true
	}

	return reflect.DeepEqual(baseValue, value)
}

func isObject(present bool, value any) bool {
	if !present {
		return false
	}

	_, ok := value.(map[string]any)

	return ok
}

func valueOrNil(present bool, value any) any {
	if !present {
		return nil
	}

	return value
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}

	return path + "." + key
}
