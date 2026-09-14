package adapter

import (
	"regexp"

	"github.com/odiumuniverse/agents-sync/pkg/permission"
)

var (
	cursorShellRe = regexp.MustCompile(`^Shell\((.+)\)$`)
	cursorMcpRe   = regexp.MustCompile(`^Mcp\(([^:]+):(.+)\)$`)
)

func cursorExportPermissions(raw map[string]any) (permission.Rules, permission.Override) {
	rules := permission.Rules{}
	override := permission.Override{}

	for _, effect := range []string{permission.EffectAllow, permission.EffectDeny} {
		for _, entry := range toStringSlice(raw[effect]) {
			if match := cursorShellRe.FindStringSubmatch(entry); match != nil {
				rules.Set(permission.BashKey(match[1]), effect)

				continue
			}

			if match := cursorMcpRe.FindStringSubmatch(entry); match != nil {
				rules.Set(permission.MCPKey(match[1], match[2]), effect)

				continue
			}

			override.Append(effect, entry)
		}
	}

	return rules, override
}

func cursorRenderPermissions(rules permission.Rules, override permission.Override) map[string]any {
	allow := append([]string{}, override.Allow...)
	deny := append([]string{}, override.Deny...)

	for _, key := range rules.Keys() {
		kind, server, pattern, ok := permission.Split(key)
		if !ok {
			continue
		}

		var entry string

		switch kind {
		case permission.KindBash:
			entry = "Shell(" + pattern + ")"
		case permission.KindMCP:
			entry = "Mcp(" + server + ":" + pattern + ")"
		default:
			continue
		}

		switch rules[key] {
		case permission.EffectDeny:
			deny = append(deny, entry)
		case permission.EffectAllow:
			allow = append(allow, entry)
		}
	}

	return map[string]any{
		permission.EffectAllow: allow,
		permission.EffectDeny:  deny,
	}
}
