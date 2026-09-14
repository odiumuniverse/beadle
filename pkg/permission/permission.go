package permission

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

const (
	EffectAllow = "allow"
	EffectAsk   = "ask"
	EffectDeny  = "deny"
)

const (
	ModeOff  = "off"
	ModeSync = "sync"
)

const (
	KindBash = "bash"
	KindTool = "tool"
	KindMCP  = "mcp"
)

type Rules map[string]string

func BashKey(pattern string) string { return KindBash + ":" + pattern }

func ToolKey(name string) string { return KindTool + ":" + name }

func MCPKey(server, tool string) string { return KindMCP + ":" + server + ":" + tool }

func Split(key string) (kind, server, pattern string, ok bool) {
	kind, rest, found := strings.Cut(key, ":")
	if !found {
		return "", "", "", false
	}

	switch kind {
	case KindBash, KindTool:
		if rest == "" {
			return "", "", "", false
		}

		return kind, "", rest, true
	case KindMCP:
		server, tool, found := strings.Cut(rest, ":")
		if !found || server == "" || tool == "" {
			return "", "", "", false
		}

		return kind, server, tool, true
	default:
		return "", "", "", false
	}
}

func (r Rules) Set(key, effect string) {
	if existing, ok := r[key]; ok && priority(effect) <= priority(existing) {
		return
	}

	r[key] = effect
}

func priority(effect string) int {
	switch effect {
	case EffectDeny:
		return 3
	case EffectAsk:
		return 2
	default:
		return 1
	}
}

func (r Rules) Marshal() ([]byte, error) {
	if r == nil {
		return []byte("{}\n"), nil
	}

	data, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("encode rules: %w", err)
	}

	return append(data, '\n'), nil
}

func Parse(data []byte) (Rules, error) {
	rules := Rules{}

	if len(data) == 0 {
		return rules, nil
	}

	if err := json.Unmarshal(data, &rules); err != nil {
		return nil, fmt.Errorf("parse rules: %w", err)
	}

	return rules, nil
}

func (r Rules) Keys() []string {
	return slices.Sorted(maps.Keys(r))
}

type Override struct {
	Allow       []string                   `json:"allow,omitempty"`
	Ask         []string                   `json:"ask,omitempty"`
	Deny        []string                   `json:"deny,omitempty"`
	BashDefault string                     `json:"bash_default,omitempty"`
	Extra       map[string]json.RawMessage `json:"extra,omitempty"`
}

func (o Override) Empty() bool {
	return len(o.Allow) == 0 && len(o.Ask) == 0 && len(o.Deny) == 0 && o.BashDefault == "" && len(o.Extra) == 0
}

func (o *Override) Append(effect, rule string) {
	switch effect {
	case EffectAsk:
		o.Ask = append(o.Ask, rule)
	case EffectDeny:
		o.Deny = append(o.Deny, rule)
	default:
		o.Allow = append(o.Allow, rule)
	}
}

func (o Override) Marshal() ([]byte, error) {
	data, err := json.Marshal(o)
	if err != nil {
		return nil, fmt.Errorf("encode override: %w", err)
	}

	return append(data, '\n'), nil
}

func ParseOverride(data []byte) (Override, error) {
	override := Override{}

	if len(data) == 0 {
		return override, nil
	}

	if err := json.Unmarshal(data, &override); err != nil {
		return Override{}, fmt.Errorf("parse override: %w", err)
	}

	return override, nil
}

func RawValue(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}

	return data
}
