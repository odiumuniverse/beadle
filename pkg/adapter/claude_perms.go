package adapter

import (
	"strings"

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

func claudeExportPermissions(raw map[string]any) (permission.Rules, permission.Override) {
	rules := permission.Rules{}
	override := permission.Override{}

	for _, effect := range []string{permission.EffectAllow, permission.EffectAsk, permission.EffectDeny} {
		for _, rule := range toStringSlice(raw[effect]) {
			key, ok := parseClaudeRule(rule)
			if !ok {
				override.Append(effect, rule)

				continue
			}

			rules.Set(key, effect)
		}
	}

	return rules, override
}

func claudeRenderPermissions(rules permission.Rules, override permission.Override) map[string]any {
	allow := append([]string{}, override.Allow...)
	ask := append([]string{}, override.Ask...)
	deny := append([]string{}, override.Deny...)

	for _, key := range rules.Keys() {
		rule, ok := renderClaudeRule(key)
		if !ok {
			continue
		}

		switch rules[key] {
		case permission.EffectAsk:
			ask = append(ask, rule)
		case permission.EffectDeny:
			deny = append(deny, rule)
		default:
			allow = append(allow, rule)
		}
	}

	return map[string]any{
		permission.EffectAllow: allow,
		permission.EffectAsk:   ask,
		permission.EffectDeny:  deny,
	}
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
		tool, ok := claudeToolKey(rule)
		if !ok {
			return "", false
		}

		return permission.ToolKey(tool), true
	}

	if name != "Bash" {
		return "", false
	}

	return permission.BashKey(parseClaudeBash(strings.TrimSuffix(spec, ")"))), true
}

func parseClaudeBash(spec string) string {
	if prefix, ok := strings.CutSuffix(spec, ":*"); ok {
		return prefix + "*"
	}

	return spec
}

func renderClaudeRule(key string) (string, bool) {
	kind, server, pattern, ok := permission.Split(key)
	if !ok {
		return "", false
	}

	switch kind {
	case permission.KindTool:
		name, ok := claudeTools[pattern]
		if !ok {
			return "", false
		}

		return name, true
	case permission.KindBash:
		return renderClaudeBash(pattern), true
	case permission.KindMCP:
		return "mcp__" + server + "__" + pattern, true
	default:
		return "", false
	}
}

func renderClaudeBash(pattern string) string {
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		if prefix == "" {
			return "Bash"
		}

		return "Bash(" + strings.TrimSuffix(prefix, " ") + ":*)"
	}

	return "Bash(" + pattern + ")"
}

func claudeToolKey(display string) (string, bool) {
	for canonical, name := range claudeTools {
		if name == display {
			return canonical, true
		}
	}

	return "", false
}
