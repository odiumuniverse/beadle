package adapter

import (
	"regexp"

	"github.com/odiumuniverse/agents-sync/pkg/permission"
)

var geminiShellRe = regexp.MustCompile(`^run_shell_command\((.+)\)$`)

func geminiExportPermissions(tools map[string]any) (permission.Rules, permission.Override) {
	rules := permission.Rules{}
	override := permission.Override{}

	collect := func(key, effect string) {
		for _, entry := range toStringSlice(tools[key]) {
			if match := geminiShellRe.FindStringSubmatch(entry); match != nil {
				rules.Set(permission.BashKey(match[1]), effect)

				continue
			}

			override.Append(effect, entry)
		}
	}

	collect("allowed", permission.EffectAllow)
	collect("confirmationRequired", permission.EffectAsk)
	collect("exclude", permission.EffectDeny)

	return rules, override
}

func geminiRenderPermissions(rules permission.Rules, override permission.Override) map[string]any {
	allowed := append([]string{}, override.Allow...)
	confirmation := append([]string{}, override.Ask...)
	exclude := append([]string{}, override.Deny...)

	for _, key := range rules.Keys() {
		kind, _, pattern, ok := permission.Split(key)
		if !ok || kind != permission.KindBash {
			continue
		}

		entry := "run_shell_command(" + pattern + ")"

		switch rules[key] {
		case permission.EffectAsk:
			confirmation = append(confirmation, entry)
		case permission.EffectDeny:
			exclude = append(exclude, entry)
		default:
			allowed = append(allowed, entry)
		}
	}

	return map[string]any{
		"allowed":              allowed,
		"confirmationRequired": confirmation,
		"exclude":              exclude,
	}
}
