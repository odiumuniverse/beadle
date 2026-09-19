package agent

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

const (
	pluginRootVar      = "CLAUDE_PLUGIN_ROOT"
	pluginMCPServers   = "mcpServers"
	maxPlaceholderNote = 32
)

var placeholderName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*`)

func PluginMCPServers(data []byte, pluginRoot string) (kind.Items, []string, error) {
	var doc struct {
		MCPServers map[string]any `json:"mcpServers"`
	}

	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("parse plugin mcp config: %w", err)
	}

	items := kind.Items{}

	var warnings []string

	for _, name := range slices.Sorted(maps.Keys(doc.MCPServers)) {
		entry, ok := doc.MCPServers[name].(map[string]any)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("server %q is not an object", name))

			continue
		}

		if bad := expandPluginEntry(entry, pluginRoot); bad != "" {
			warnings = append(warnings, fmt.Sprintf("server %q references an unsupported variable %q", name, bad))

			continue
		}

		server, ok := claudeMCP.decode(entry)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("server %q is not a supported MCP entry", name))

			continue
		}

		items[name] = mcp.Encode(server)
	}

	return items, warnings, nil
}

func expandPluginEntry(entry map[string]any, pluginRoot string) string {
	for _, key := range slices.Sorted(maps.Keys(entry)) {
		expanded, bad := expandPluginValue(entry[key], pluginRoot)
		if bad != "" {
			return bad
		}

		entry[key] = expanded
	}

	return ""
}

func expandPluginValue(value any, pluginRoot string) (any, string) {
	switch typed := value.(type) {
	case string:
		return expandPluginString(typed, pluginRoot)
	case []any:
		for i, item := range typed {
			expanded, bad := expandPluginValue(item, pluginRoot)
			if bad != "" {
				return nil, bad
			}

			typed[i] = expanded
		}

		return typed, ""
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(typed)) {
			expanded, bad := expandPluginValue(typed[key], pluginRoot)
			if bad != "" {
				return nil, bad
			}

			typed[key] = expanded
		}

		return typed, ""
	default:
		return value, ""
	}
}

func expandPluginString(value, pluginRoot string) (any, string) {
	var out strings.Builder

	rest := value

	for {
		start := strings.Index(rest, "${")
		if start < 0 {
			out.WriteString(rest)

			break
		}

		end := strings.Index(rest[start:], "}")
		if end < 0 {
			return nil, unsupportedPlaceholder(rest[start:])
		}

		name := rest[start+2 : start+end]

		if name != pluginRootVar {
			return nil, name
		}

		out.WriteString(rest[:start])
		out.WriteString(pluginRoot)

		rest = rest[start+end+1:]
	}

	expanded := out.String()

	if name := unsupportedSecretRef(expanded); name != "" {
		return nil, name
	}

	return expanded, ""
}

func unsupportedPlaceholder(remainder string) string {
	name := placeholderName.FindString(strings.TrimPrefix(remainder, "${"))
	if name == "" {
		return "unterminated ${"
	}

	if len(name) > maxPlaceholderNote {
		name = name[:maxPlaceholderNote]
	}

	return name
}

func unsupportedSecretRef(value string) string {
	idx := strings.Index(value, "{secret:")
	if idx < 0 {
		return ""
	}

	ref := value[idx+1:]
	if end := strings.Index(ref, "}"); end >= 0 {
		ref = ref[:end]
	}

	return ref
}
