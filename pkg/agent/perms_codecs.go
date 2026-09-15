package agent

import (
	"regexp"
	"strings"

	"github.com/odiumuniverse/agents-sync/pkg/kind"
	"github.com/odiumuniverse/agents-sync/pkg/permission"
)

var claudeTools = map[string]string{
	"edit":      "Edit",
	"glob":      "Glob",
	"grep":      "Grep",
	"read":      "Read",
	"skill":     "Skill",
	"task":      "Task",
	"todowrite": "TodoWrite",
	"webfetch":  "WebFetch",
	"websearch": "WebSearch",
}

var claudePerms = listCodec{
	lists: []effectList{
		{key: permission.EffectAllow, effect: permission.EffectAllow},
		{key: permission.EffectAsk, effect: permission.EffectAsk},
		{key: permission.EffectDeny, effect: permission.EffectDeny},
	},
	parse:  parseClaudeRule,
	render: renderClaudeRule,
}

func parseClaudeRule(rule string) (string, bool) {
	if rest, ok := strings.CutPrefix(rule, "mcp__"); ok {
		server, tool, ok := strings.Cut(rest, "__")
		if !ok || server == "" || tool == "" {
			return "", false
		}

		return permission.MCPKey(server, tool), true
	}

	name, spec, hasSpec := strings.Cut(rule, "(")
	if !hasSpec {
		for canonical, display := range claudeTools {
			if display == rule {
				return permission.ToolKey(canonical), true
			}
		}

		return "", false
	}

	if name != "Bash" || !strings.HasSuffix(spec, ")") {
		return "", false
	}

	pattern := strings.TrimSuffix(spec, ")")
	if prefix, ok := strings.CutSuffix(pattern, ":*"); ok {
		pattern = prefix + "*"
	}

	if pattern == "" {
		return "", false
	}

	return permission.BashKey(pattern), true
}

func renderClaudeRule(key string) (string, bool) {
	kind, server, pattern, ok := permission.Split(key)
	if !ok {
		return "", false
	}

	switch kind {
	case permission.KindTool:
		name, ok := claudeTools[pattern]

		return name, ok
	case permission.KindBash:
		if prefix, ok := strings.CutSuffix(pattern, "*"); ok && !strings.Contains(prefix, "*") {
			prefix = strings.TrimSuffix(prefix, " ")
			if prefix == "" {
				return "", false
			}

			return "Bash(" + prefix + ":*)", true
		}

		return "Bash(" + pattern + ")", true
	case permission.KindMCP:
		return "mcp__" + server + "__" + pattern, true
	default:
		return "", false
	}
}

var (
	geminiShellRule = regexp.MustCompile(`^run_shell_command\((.+)\)$`)
	cursorShellRule = regexp.MustCompile(`^Shell\((.+)\)$`)
	cursorMCPRule   = regexp.MustCompile(`^Mcp\(([^:]+):(.+)\)$`)
)

var geminiPerms = listCodec{
	lists: []effectList{
		{key: "allowed", effect: permission.EffectAllow},
		{key: "confirmationRequired", effect: permission.EffectAsk},
		{key: "exclude", effect: permission.EffectDeny},
	},
	parse: func(rule string) (string, bool) {
		if match := geminiShellRule.FindStringSubmatch(rule); match != nil {
			return permission.BashKey(match[1]), true
		}

		return "", false
	},
	render: func(key string) (string, bool) {
		kind, _, pattern, ok := permission.Split(key)
		if !ok || kind != permission.KindBash {
			return "", false
		}

		return "run_shell_command(" + pattern + ")", true
	},
}

var cursorPerms = listCodec{
	lists: []effectList{
		{key: permission.EffectAllow, effect: permission.EffectAllow},
		{key: permission.EffectDeny, effect: permission.EffectDeny},
	},
	parse: func(rule string) (string, bool) {
		if match := cursorShellRule.FindStringSubmatch(rule); match != nil {
			return permission.BashKey(match[1]), true
		}

		if match := cursorMCPRule.FindStringSubmatch(rule); match != nil {
			return permission.MCPKey(match[1], match[2]), true
		}

		return "", false
	},
	render: func(key string) (string, bool) {
		kind, server, pattern, ok := permission.Split(key)
		if !ok {
			return "", false
		}

		switch kind {
		case permission.KindBash:
			return "Shell(" + pattern + ")", true
		case permission.KindMCP:
			return "Mcp(" + server + ":" + pattern + ")", true
		default:
			return "", false
		}
	},
}

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
	"doom_loop":          {},
}

const openCodeBash = "bash"

type openCodeCodec struct{}

func (openCodeCodec) read(block map[string]any) kind.Items {
	rules := permission.Rules{}

	for key, value := range block {
		if key == openCodeBash {
			bash, _ := value.(map[string]any)

			for pattern, raw := range bash {
				if effect, ok := raw.(string); ok && pattern != "*" && validEffect(effect) {
					rules.Set(permission.BashKey(pattern), effect)
				}
			}

			continue
		}

		effect, ok := value.(string)
		if !ok || !validEffect(effect) {
			continue
		}

		if canonical, ok := openCodeRuleKey(key); ok {
			rules.Set(canonical, effect)
		}
	}

	return rulesToItems(rules)
}

func (c openCodeCodec) ops(pointer string, block map[string]any, desired kind.Items) []patchOp {
	current := c.read(block)

	_, bashIsObject := block[openCodeBash].(map[string]any)

	var ops []patchOp

	for _, key := range unionItemKeys(current, desired) {
		want, keep := desired[key]
		have, exists := current[key]

		if keep && exists && string(want) == string(have) {
			continue
		}

		path, ok := openCodePath(pointer, key)
		if !ok {
			continue
		}

		if !keep {
			if exists {
				ops = append(ops, removeOp(path))
			}

			continue
		}

		if strings.HasPrefix(key, permission.KindBash+":") && !bashIsObject {
			ops = append(ops, openCodeBashObject(pointer, block[openCodeBash]))
			bashIsObject = true
		}

		ops = append(ops, addOp(path, string(want)))
	}

	return ops
}

func (openCodeCodec) project(key, effect string) (string, bool) {
	if !validEffect(effect) {
		return "", false
	}

	if _, ok := openCodePath("", key); !ok {
		return "", false
	}

	return key, true
}

func openCodeBashObject(pointer string, current any) patchOp {
	patterns := map[string]any{}

	if effect, ok := current.(string); ok {
		patterns["*"] = effect
	}

	return addOp(pointerJoin(pointer, openCodeBash), patterns)
}

func openCodeRuleKey(key string) (string, bool) {
	if _, tool := openCodeTools[key]; tool {
		return permission.ToolKey(key), true
	}

	if server, tool, ok := splitOpenCodeMCPKey(key); ok {
		return permission.MCPKey(server, tool), true
	}

	return "", false
}

func openCodePath(pointer, key string) (string, bool) {
	kind, server, pattern, ok := permission.Split(key)
	if !ok {
		return "", false
	}

	switch kind {
	case permission.KindTool:
		if _, tool := openCodeTools[pattern]; tool {
			return pointerJoin(pointer, pattern), true
		}
	case permission.KindBash:
		if pattern != "*" {
			return pointerJoin(pointerJoin(pointer, openCodeBash), pattern), true
		}
	case permission.KindMCP:
		name := server + "_" + pattern
		if _, _, ok := splitOpenCodeMCPKey(name); ok {
			return pointerJoin(pointer, name), true
		}
	}

	return "", false
}

func splitOpenCodeMCPKey(key string) (string, string, bool) {
	if _, skip := openCodeNonMCPKeys[key]; skip {
		return "", "", false
	}

	server, tool, ok := strings.Cut(key, "_")
	if !ok || server == "" || tool == "" {
		return "", "", false
	}

	for _, r := range server {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return "", "", false
		}
	}

	return server, tool, true
}
