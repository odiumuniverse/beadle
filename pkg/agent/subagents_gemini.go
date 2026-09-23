package agent

import (
	"errors"
	"fmt"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/odiumuniverse/beadle/pkg/subagent"
)

// geminiToolVocab maps canonical tool names to the Gemini CLI tool names of
// the subagent docs.
var geminiToolVocab = newToolVocab(map[string]string{
	toolRead:      "read_file",
	toolWrite:     "write_file",
	toolEdit:      "replace",
	toolBash:      "run_shell_command",
	toolGrep:      "search_file_content",
	toolGlob:      globTool,
	toolWebFetch:  "web_fetch",
	toolWebSearch: "google_web_search",
	toolSkill:     "activate_skill",
})

// geminiTool maps a canonical tool name to the Gemini spelling, including
// the mcp_<server>_<tool> MCP form.
func geminiTool(tool string) (string, bool) {
	if name, ok := geminiToolVocab.host(tool); ok {
		return name, true
	}

	return geminiMCPName(tool)
}

// geminiCanonical maps a Gemini tool entry back to the canonical form.
func geminiCanonical(name string) (string, bool) {
	if tool, ok := geminiToolVocab.canonical(name); ok {
		return tool, true
	}

	return geminiCanonicalMCP(name)
}

// geminiSubagentCodec reads and writes Gemini CLI subagent files.
type geminiSubagentCodec struct{}

func (geminiSubagentCodec) parse(_ string, data []byte) (subagent.Document, bool, error) {
	front, body, ok := subagent.Split(data)
	if !ok {
		return subagent.Document{}, false, errors.New("no frontmatter; Gemini CLI subagents need a name and a description")
	}

	var raw struct {
		Name        string   `yaml:"name"`
		Description string   `yaml:"description"`
		Kind        string   `yaml:"kind"`
		Tools       []string `yaml:"tools"`
		Model       string   `yaml:"model"`
		MaxTurns    int      `yaml:"max_turns"`
	}

	if err := yaml.Unmarshal(front, &raw); err != nil {
		return subagent.Document{}, false, fmt.Errorf("parse frontmatter: %w", err)
	}

	switch raw.Kind {
	case "", localKind:
	case remoteKind:
		return subagent.Document{}, false, errors.New("kind: remote subagents are not synced")
	default:
		return subagent.Document{}, false, fmt.Errorf("unknown kind %q", raw.Kind)
	}

	if raw.Name == "" {
		return subagent.Document{}, false, errors.New("the name field is required")
	}

	doc := subagent.Document{
		Name:        raw.Name,
		Description: raw.Description,
		Model:       raw.Model,
		MaxTurns:    raw.MaxTurns,
		Body:        body,
	}

	for _, name := range raw.Tools {
		if tool, ok := geminiCanonical(name); ok {
			doc.Tools = append(doc.Tools, tool)
		}
	}

	doc.Tools = sortToolList(doc.Tools)
	if len(doc.Tools) == 0 {
		// Only unmappable entries (wildcards, plugin tools) were listed; the
		// canonical form cannot express them and they stay in the file.
		doc.Tools = nil
	}

	return doc, true, nil
}

func (geminiSubagentCodec) fields(doc subagent.Document, existing []byte) ([]subagent.Field, string, error) {
	if !subagent.ValidName(doc.Name) {
		return nil, "", errors.New("subagent name is required")
	}

	fields := []subagent.Field{{Key: nameKey, Value: doc.Name}}

	if doc.Description != "" {
		fields = append(fields, subagent.Field{Key: descriptionKey, Value: doc.Description})
	}

	if model := geminiModel(doc.Model); model != "" {
		fields = append(fields, subagent.Field{Key: modelKey, Value: model})
	}

	if tools := geminiTools(doc, existing); len(tools) > 0 {
		fields = append(fields, subagent.Field{Key: toolsKey, Value: tools})
	}

	if doc.MaxTurns > 0 {
		fields = append(fields, subagent.Field{Key: "max_turns", Value: doc.MaxTurns})
	}

	return fields, doc.Body, nil
}

// geminiTools renders the allowlist: the mappable canonical tools followed
// by the host entries the codec cannot canonicalize.
func geminiTools(doc subagent.Document, existing []byte) []string {
	var out []string

	for _, tool := range doc.Tools {
		if name, ok := geminiTool(tool); ok {
			out = append(out, name)
		}
	}

	extra := preserveToolExtras(existing, func(name string) bool {
		_, ok := geminiCanonical(name)

		return ok
	})

	return append(out, extra...)
}

// geminiModel keeps a model Gemini understands; everything else falls back
// to inherit, the documented default.
func geminiModel(model string) string {
	if model == "" {
		return ""
	}

	if model == inheritModel || strings.HasPrefix(model, "gemini") {
		return model
	}

	return "inherit"
}

// geminiToolsLost reports an allowlist with no Gemini equivalent: the codec
// writes no tools key, and the host then keeps every tool.
func geminiToolsLost(doc subagent.Document) bool {
	if doc.Tools == nil {
		return false
	}

	for _, tool := range doc.Tools {
		if _, ok := geminiTool(tool); ok {
			return false
		}
	}

	return true
}

// managed lists the frontmatter keys the Gemini codec owns.
func (geminiSubagentCodec) managed() []string {
	return []string{nameKey, descriptionKey, toolsKey, modelKey, "max_turns"}
}

// strayKeys is empty: Gemini ignores unknown frontmatter keys.
func (geminiSubagentCodec) strayKeys([]byte) []string { return nil }

func (geminiSubagentCodec) audit(doc subagent.Document) []string {
	var notes []string

	if geminiToolsLost(doc) {
		notes = append(notes, "no canonical tool has a gemini equivalent; the host keeps all tools (fail-open)")
	}

	for _, tool := range doc.Tools {
		if _, _, mcp := splitCanonicalMCP(tool); mcp {
			if _, ok := geminiTool(tool); !ok {
				notes = append(notes, fmt.Sprintf(
					"MCP tool %q has no gemini form: server names cannot contain underscores", tool))
			}

			continue
		}

		if _, ok := geminiTool(tool); !ok {
			notes = append(notes, fmt.Sprintf("unmappable tool %q for gemini; skipped", tool))
		}
	}

	if len(doc.DisallowedTools) > 0 && !subagent.ReadOnly(doc) {
		notes = append(notes, "a partial tool deny is not expressible for gemini; write the allowlist instead")
	}

	if subagent.ReadOnly(doc) && doc.Tools == nil {
		notes = append(notes, "readonly without a tool allowlist is not expressible for gemini; the host keeps all tools")
	}

	if doc.Model != "" && geminiModel(doc.Model) != doc.Model {
		notes = append(notes, fmt.Sprintf("model %q does not map to gemini; the host gets inherit", doc.Model))
	}

	if doc.MCPServers != nil {
		notes = append(notes, "gemini takes inline mcpServers definitions; the canonical server list stays in the vault")
	}

	return dedupStrings(notes)
}
