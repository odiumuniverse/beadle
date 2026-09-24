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

// PluginMCPServers decodes a Claude plugin MCP document.
func PluginMCPServers(data []byte, pluginRoot string) (kind.Items, []string, error) {
	return PluginMCPServersFor(ClaudeCodeID, data, pluginRoot)
}

// PluginMCPServersFor decodes a plugin MCP document in the dialect of one
// host: the event names and the transport fields the host itself reads. The
// plugin root placeholder of the host expands to pluginRoot; any other
// `${…}` variable refuses the server, because the canon cannot resolve it.
func PluginMCPServersFor(source string, data []byte, pluginRoot string) (kind.Items, []string, error) {
	codec, roots := pluginMCPDialect(source)

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

		if bad := expandPluginEntry(entry, pluginRoot, roots); bad != "" {
			warnings = append(warnings, fmt.Sprintf("server %q references an unsupported variable %q", name, bad))

			continue
		}

		server, ok := codec.decode(entry)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("server %q is not a supported MCP entry", name))

			continue
		}

		items[name] = mcp.Encode(server)
	}

	return items, warnings, nil
}

// pluginMCPDialect returns the codec and the accepted plugin root placeholders
// of one host.
func pluginMCPDialect(source string) (mcpCodec, []string) {
	switch source {
	case CodexID:
		return claudeMCP, []string{"PLUGIN_ROOT", pluginRootVar}
	case CursorID:
		return cursorMCP, []string{"CURSOR_PLUGIN_ROOT", pluginRootVar}
	case GeminiCLIID:
		return geminiMCP, []string{"extensionPath"}
	case AntigravityCLIID:
		return antigravityMCP, []string{"PLUGIN_ROOT", pluginRootVar}
	default:
		return claudeMCP, []string{pluginRootVar}
	}
}

func expandPluginEntry(entry map[string]any, pluginRoot string, roots []string) string {
	for _, key := range slices.Sorted(maps.Keys(entry)) {
		expanded, bad := expandPluginValue(entry[key], pluginRoot, roots)
		if bad != "" {
			return bad
		}

		entry[key] = expanded
	}

	return ""
}

func expandPluginValue(value any, pluginRoot string, roots []string) (any, string) {
	switch typed := value.(type) {
	case string:
		return expandPluginString(typed, pluginRoot, roots)
	case []any:
		for i, item := range typed {
			expanded, bad := expandPluginValue(item, pluginRoot, roots)
			if bad != "" {
				return nil, bad
			}

			typed[i] = expanded
		}

		return typed, ""
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(typed)) {
			expanded, bad := expandPluginValue(typed[key], pluginRoot, roots)
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

func expandPluginString(value, pluginRoot string, roots []string) (any, string) {
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

		if !slices.Contains(roots, name) {
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
