package kind

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/odiumuniverse/agents-sync/pkg/merge"
)

type ID string

const (
	Rules       ID = "rules"
	MCP         ID = "mcp"
	Skills      ID = "skills"
	Permissions ID = "permissions"
	Memory      ID = "memory"
)

const RulesKey = "main"

type Items map[string][]byte

func (it Items) Keys() []string {
	return slices.Sorted(maps.Keys(it))
}

func (it Items) Equal(other Items) bool {
	return maps.EqualFunc(it, other, bytes.Equal)
}

type Spec struct {
	ID        ID
	Singleton bool
	Merge     func(base, vault, agent []byte) (merged []byte, ok bool)
	Lift      func(vault, projected, updated []byte) []byte
	Group     func(key string) string
}

var specs = []Spec{
	{ID: Rules, Singleton: true, Merge: mergeText, Group: sameKey},
	{ID: MCP, Merge: mergeJSON, Lift: liftJSON, Group: sameKey},
	{ID: Skills, Merge: mergeFile, Group: firstSegment},
	{ID: Permissions, Merge: mergeNever, Group: sameKey},
	{ID: Memory, Merge: mergeFile, Group: firstSegment},
}

func All() []Spec {
	return slices.Clone(specs)
}

func Lookup(id ID) (Spec, bool) {
	for _, spec := range specs {
		if spec.ID == id {
			return spec, true
		}
	}

	return Spec{}, false
}

func Parse(name string) (ID, error) {
	if _, ok := Lookup(ID(name)); !ok {
		return "", fmt.Errorf("unknown resource kind %q (expected %s)", name, strings.Join(Names(), ", "))
	}

	return ID(name), nil
}

func Names() []string {
	names := make([]string, 0, len(specs))

	for _, spec := range specs {
		names = append(names, string(spec.ID))
	}

	return names
}

func CanonicalJSON(data []byte) ([]byte, error) {
	value, err := decodeJSON(data)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode json: %w", err)
	}

	return out, nil
}

func ConflictDocument(base, vault, agent []byte, agentLabel string) []byte {
	return merge.Text(base, vault, agent, merge.TextOptions{AgentLabel: agentLabel}).Merged
}

func HasMarkers(data []byte) bool {
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")

		if strings.HasPrefix(line, "<<<<<<< ") || strings.HasPrefix(line, ">>>>>>> ") ||
			strings.HasPrefix(line, "||||||| ") || line == "=======" {
			return true
		}
	}

	return false
}

func sameKey(key string) string {
	return key
}

func firstSegment(key string) string {
	group, _, _ := strings.Cut(key, "/")

	return group
}

func mergeText(base, vault, agent []byte) ([]byte, bool) {
	if vault == nil || agent == nil {
		return nil, false
	}

	result := merge.Text(base, vault, agent, merge.TextOptions{})
	if result.Conflicts > 0 {
		return nil, false
	}

	if result.Merged == nil {
		return []byte{}, true
	}

	return result.Merged, true
}

func mergeFile(base, vault, agent []byte) ([]byte, bool) {
	if !IsText(base) || !IsText(vault) || !IsText(agent) {
		return nil, false
	}

	return mergeText(base, vault, agent)
}

func mergeJSON(base, vault, agent []byte) ([]byte, bool) {
	if vault == nil || agent == nil {
		return nil, false
	}

	var baseValue any

	if base != nil {
		decoded, err := decodeJSON(base)
		if err != nil {
			return nil, false
		}

		baseValue = decoded
	}

	vaultValue, err := decodeJSON(vault)
	if err != nil {
		return nil, false
	}

	agentValue, err := decodeJSON(agent)
	if err != nil {
		return nil, false
	}

	merged, conflicts := merge.JSON(baseValue, vaultValue, agentValue)
	if len(conflicts) > 0 {
		return nil, false
	}

	out, err := json.Marshal(merged)
	if err != nil {
		return nil, false
	}

	return out, true
}

func mergeNever(_, _, _ []byte) ([]byte, bool) {
	return nil, false
}

func liftJSON(vault, projected, updated []byte) []byte {
	vaultValue, err := decodeJSON(vault)
	if err != nil {
		return updated
	}

	projectedValue, err := decodeJSON(projected)
	if err != nil {
		return updated
	}

	updatedValue, err := decodeJSON(updated)
	if err != nil {
		return updated
	}

	out, err := json.Marshal(applyDelta(vaultValue, projectedValue, updatedValue))
	if err != nil {
		return updated
	}

	return out
}

func applyDelta(vault, projected, updated any) any {
	vaultObj, vaultOK := vault.(map[string]any)
	projectedObj, projectedOK := projected.(map[string]any)
	updatedObj, updatedOK := updated.(map[string]any)

	if !vaultOK || !projectedOK || !updatedOK {
		if reflect.DeepEqual(projected, updated) {
			return vault
		}

		return updated
	}

	out := maps.Clone(vaultObj)

	for _, key := range unionKeys(projectedObj, updatedObj) {
		projectedValue, inProjected := projectedObj[key]
		updatedValue, inUpdated := updatedObj[key]

		switch {
		case inProjected && !inUpdated:
			delete(out, key)
		case inProjected && reflect.DeepEqual(projectedValue, updatedValue):
		default:
			out[key] = applyDelta(vaultObj[key], projectedValue, updatedValue)
		}
	}

	return out
}

func unionKeys(a, b map[string]any) []string {
	keys := make(map[string]struct{}, len(a)+len(b))

	for key := range a {
		keys[key] = struct{}{}
	}

	for key := range b {
		keys[key] = struct{}{}
	}

	return slices.Sorted(maps.Keys(keys))
}

func decodeJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	var value any

	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}

	return value, nil
}

func IsText(data []byte) bool {
	return data == nil || (utf8.Valid(data) && bytes.IndexByte(data, 0) < 0)
}
