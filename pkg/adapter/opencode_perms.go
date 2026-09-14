package adapter

import (
	"encoding/json"
	"strings"

	"github.com/odiumuniverse/agents-sync/pkg/permission"
)

var openCodeTools = map[string]struct{}{
	"edit":      {},
	"glob":      {},
	"grep":      {},
	"read":      {},
	"skill":     {},
	"task":      {},
	"todowrite": {},
	"webfetch":  {},
	"websearch": {},
}

var openCodeNonMCPKeys = map[string]struct{}{
	"external_directory": {},
}

func openCodeExportPermissions(raw map[string]any) (permission.Rules, permission.Override) {
	rules := permission.Rules{}
	override := permission.Override{}

	for key, value := range raw {
		if key == "bash" {
			exportOpenCodeBash(value, rules, &override)

			continue
		}

		effect, ok := value.(string)
		if !ok {
			override.Extra = setExtra(override.Extra, key, value)

			continue
		}

		if _, portable := openCodeTools[key]; portable {
			rules.Set(permission.ToolKey(key), effect)

			continue
		}

		if server, tool, ok := splitOpenCodeMCPKey(key); ok {
			rules.Set(permission.MCPKey(server, tool), effect)

			continue
		}

		override.Extra = setExtra(override.Extra, key, effect)
	}

	return rules, override
}

func exportOpenCodeBash(value any, rules permission.Rules, override *permission.Override) {
	bash, ok := value.(map[string]any)
	if !ok {
		override.Extra = setExtra(override.Extra, "bash", value)

		return
	}

	for pattern, rawEffect := range bash {
		effect, ok := rawEffect.(string)
		if !ok {
			continue
		}

		if pattern == "*" {
			override.BashDefault = effect

			continue
		}

		rules.Set(permission.BashKey(pattern), effect)
	}
}

func openCodeRenderPermissions(rules permission.Rules, override permission.Override) map[string]any {
	out := map[string]any{}

	for key, raw := range override.Extra {
		var value any
		if err := json.Unmarshal(raw, &value); err == nil {
			out[key] = value
		}
	}

	bash := map[string]any{}
	if override.BashDefault != "" {
		bash["*"] = override.BashDefault
	}

	for _, key := range rules.Keys() {
		kind, server, pattern, ok := permission.Split(key)
		if !ok {
			continue
		}

		switch kind {
		case permission.KindTool:
			out[pattern] = rules[key]
		case permission.KindBash:
			bash[pattern] = rules[key]
		case permission.KindMCP:
			out[server+"_"+pattern] = rules[key]
		}
	}

	if len(bash) > 0 {
		out["bash"] = bash
	}

	return out
}

func splitOpenCodeMCPKey(key string) (string, string, bool) {
	if _, skip := openCodeNonMCPKeys[key]; skip {
		return "", "", false
	}

	server, tool, ok := cutServerTool(key)
	if !ok {
		return "", "", false
	}

	for _, r := range server {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return "", "", false
		}
	}

	return server, tool, true
}

func cutServerTool(key string) (string, string, bool) {
	server, tool, ok := strings.Cut(key, "_")
	if !ok || server == "" || tool == "" {
		return "", "", false
	}

	return server, tool, true
}

func setExtra(extra map[string]json.RawMessage, key string, value any) map[string]json.RawMessage {
	if extra == nil {
		extra = map[string]json.RawMessage{}
	}

	if raw := permission.RawValue(value); raw != nil {
		extra[key] = raw
	}

	return extra
}
