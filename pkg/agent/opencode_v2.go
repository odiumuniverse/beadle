package agent

import (
	"fmt"

	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/permission"
)

const (
	openCodeMCPPointer         = "/mcp"
	openCodeServersPointer     = "/mcp/servers"
	openCodePermissionsPointer = "/permissions"
	openCodePermissionPointer  = "/permission"

	openCodeV2Shell    = "shell"
	openCodeV2Local    = "local"
	openCodeV2Remote   = "remote"
	openCodeV2Task     = "task"
	openCodeV2Subagent = "subagent"
)

var openCodeV2Actions = map[string]struct{}{
	"read":               {},
	"edit":               {},
	"glob":               {},
	"grep":               {},
	"skill":              {},
	"question":           {},
	"webfetch":           {},
	"websearch":          {},
	"external_directory": {},
	"execute":            {},
}

var (
	openCodeV2Aliases   = map[string]string{openCodeV2Task: openCodeV2Subagent}
	openCodeV2Canonical = map[string]string{openCodeV2Subagent: openCodeV2Task}
)

func openCodeMCPTarget(data []byte) (mcpPlacement, error) {
	var raw any

	found, err := decodePointer(data, openCodeMCPPointer, &raw)
	if err != nil {
		return mcpPlacement{}, err
	}

	if !found {
		return mcpPlacement{primary: openCodeServersPointer, shadow: openCodeMCPPointer}, nil
	}

	block, ok := raw.(map[string]any)
	if !ok {
		return mcpPlacement{}, fmt.Errorf("%s is not an object, refusing to rewrite it", openCodeMCPPointer)
	}

	if servers, hasServers := block["servers"]; hasServers {
		container, ok := servers.(map[string]any)
		if !ok {
			return mcpPlacement{}, fmt.Errorf("%s is not an object, refusing to rewrite it", openCodeServersPointer)
		}

		if !openCodeV1Server(container) {
			return mcpPlacement{primary: openCodeServersPointer, shadow: openCodeMCPPointer}, nil
		}
	}

	return mcpPlacement{primary: openCodeMCPPointer}, nil
}

func openCodeV1Server(entry map[string]any) bool {
	switch stringField(entry, keyType) {
	case openCodeV2Local, openCodeV2Remote:
		return true
	default:
		return false
	}
}

func openCodePermissionTarget(data []byte) (permPlacement, error) {
	var raw any

	found, err := decodePointer(data, openCodePermissionsPointer, &raw)
	if err != nil {
		return permPlacement{}, err
	}

	if found {
		if _, ok := raw.([]any); !ok {
			return permPlacement{}, fmt.Errorf("%s is not an array, refusing to rewrite it", openCodePermissionsPointer)
		}

		if err := ensureLegacyPermissionObject(data); err != nil {
			return permPlacement{}, err
		}

		return permPlacement{v2: true, primary: openCodePermissionsPointer, shadow: openCodePermissionPointer}, nil
	}

	var legacy any

	legacyFound, err := decodePointer(data, openCodePermissionPointer, &legacy)
	if err != nil {
		return permPlacement{}, err
	}

	if legacyFound {
		if _, ok := legacy.(map[string]any); !ok {
			return permPlacement{}, fmt.Errorf("%s is not an object, refusing to rewrite it", openCodePermissionPointer)
		}

		return permPlacement{primary: openCodePermissionPointer}, nil
	}

	// Neither permission key exists: follow the file's MCP dialect, so a V1
	// config does not grow the V2 array next to its V1 servers. A file beadle
	// cannot classify (or an unreadable /mcp) keeps the V2 default.
	if placement, err := openCodeMCPTarget(data); err == nil && placement.primary == openCodeMCPPointer {
		return permPlacement{primary: openCodePermissionPointer}, nil
	}

	return permPlacement{v2: true, primary: openCodePermissionsPointer, shadow: openCodePermissionPointer}, nil
}

func ensureLegacyPermissionObject(data []byte) error {
	var legacy any

	found, err := decodePointer(data, openCodePermissionPointer, &legacy)
	if err != nil {
		return err
	}

	if found {
		if _, ok := legacy.(map[string]any); !ok {
			return fmt.Errorf("%s is not an object, refusing to rewrite it", openCodePermissionPointer)
		}
	}

	return nil
}

type openCodeV2Codec struct{}

func (openCodeV2Codec) read(raw any) kind.Items {
	rules, ok := raw.([]any)
	if !ok {
		return kind.Items{}
	}

	items := kind.Items{}

	for _, rule := range rules {
		if key, effect, ok := parseOpenCodeV2Rule(rule); ok {
			items[key] = []byte(effect)
		}
	}

	return items
}

func (openCodeV2Codec) ops(pointer string, raw any, found bool, desired kind.Items) ([]patchOp, error) {
	rules, err := openCodeV2Rules(raw, found, pointer)
	if err != nil {
		return nil, err
	}

	next := rebuildOpenCodeV2Rules(rules, desired)

	if len(next) == 0 {
		if !found || len(rules) == 0 {
			return nil, nil
		}

		next = []any{}
	}

	if jsonEqual(rules, next) {
		return nil, nil
	}

	return []patchOp{addOp(pointer, next)}, nil
}

func openCodeV2Rules(raw any, found bool, pointer string) ([]any, error) {
	if !found {
		return nil, nil
	}

	rules, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s is not an array, refusing to rewrite it", pointer)
	}

	return rules, nil
}

func rebuildOpenCodeV2Rules(rules []any, desired kind.Items) []any {
	last := map[string]string{}

	for _, rule := range rules {
		if key, effect, ok := parseOpenCodeV2Rule(rule); ok {
			last[key] = effect
		}
	}

	var next []any

	for _, rule := range rules {
		key, _, managed := parseOpenCodeV2Rule(rule)
		if !managed {
			next = append(next, rule)

			continue
		}

		if want, keep := desired[key]; keep && last[key] == string(want) {
			next = append(next, rule)
		}
	}

	for _, key := range desired.Keys() {
		if effect, exists := last[key]; exists && effect == string(desired[key]) {
			continue
		}

		if rule, ok := renderOpenCodeV2Rule(key, string(desired[key])); ok {
			next = append(next, rule)
		}
	}

	return next
}

func (openCodeV2Codec) project(key, effect string) (string, bool) {
	if !validEffect(effect) {
		return "", false
	}

	if _, ok := renderOpenCodeV2Rule(key, effect); !ok {
		return "", false
	}

	return key, true
}

func parseOpenCodeV2Rule(raw any) (string, string, bool) {
	rule, ok := raw.(map[string]any)
	if !ok {
		return "", "", false
	}

	effect := stringField(rule, "effect")
	if !validEffect(effect) {
		return "", "", false
	}

	action := stringField(rule, "action")

	resource := stringField(rule, "resource")
	if action == "" || resource == "" {
		return "", "", false
	}

	if action == openCodeV2Shell {
		if resource == "*" {
			return "", "", false
		}

		return permission.BashKey(resource), effect, true
	}

	if resource != "*" {
		return "", "", false
	}

	if canonical, ok := openCodeV2Canonical[action]; ok {
		return permission.ToolKey(canonical), effect, true
	}

	if _, ok := openCodeV2Actions[action]; ok {
		return permission.ToolKey(action), effect, true
	}

	if server, tool, ok := splitOpenCodeMCPKey(action); ok {
		return permission.MCPKey(server, tool), effect, true
	}

	return "", "", false
}

func renderOpenCodeV2Rule(key, effect string) (map[string]any, bool) {
	if !validEffect(effect) {
		return nil, false
	}

	kind, server, pattern, ok := permission.Split(key)
	if !ok {
		return nil, false
	}

	switch kind {
	case permission.KindBash:
		if pattern == "*" {
			return nil, false
		}

		return openCodeV2Rule(openCodeV2Shell, pattern, effect), true
	case permission.KindTool:
		action, ok := openCodeV2Aliases[pattern]
		if !ok {
			if _, known := openCodeV2Actions[pattern]; !known {
				return nil, false
			}

			action = pattern
		}

		return openCodeV2Rule(action, "*", effect), true
	case permission.KindMCP:
		name := server + "_" + pattern
		if _, _, ok := splitOpenCodeMCPKey(name); !ok {
			return nil, false
		}

		return openCodeV2Rule(name, "*", effect), true
	default:
		return nil, false
	}
}

func openCodeV2Rule(action, resource, effect string) map[string]any {
	return map[string]any{
		"action":   action,
		"resource": resource,
		"effect":   effect,
	}
}
