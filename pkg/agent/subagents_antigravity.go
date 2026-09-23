package agent

import (
	"errors"
	"fmt"
	"slices"

	yaml "go.yaml.in/yaml/v3"

	"github.com/odiumuniverse/beadle/pkg/subagent"
)

// antigravityToolVocab maps canonical tool names to the Antigravity tool
// names of the subagent docs. Unknown tool names hang the host subagent, so
// only documented names are written; everything else is reported.
var antigravityToolVocab = toolVocab{
	forward: map[string]string{
		toolRead:  "view_file",
		toolWrite: "replace_file_content",
		toolEdit:  "replace_file_content",
		toolBash:  "run_command",
		toolGrep:  "grep_search",
	},
	reverse: map[string]string{
		"view_file":            toolRead,
		"replace_file_content": toolEdit,
		"run_command":          toolBash,
		"grep_search":          toolGrep,
	},
}

// antigravityModes lists the canonical mode values Antigravity can express.
var antigravityModes = []string{"", subagentLabel, primaryMode, allMode}

// antigravitySubagentCodec reads and writes Antigravity subagent files.
type antigravitySubagentCodec struct{}

func (antigravitySubagentCodec) parse(_ string, data []byte) (subagent.Document, bool, error) {
	front, body, ok := subagent.Split(data)
	if !ok {
		return subagent.Document{}, false, errors.New("no frontmatter; Antigravity subagents need a name and a description")
	}

	var raw struct {
		Name        string   `yaml:"name"`
		Description string   `yaml:"description"`
		Tools       []string `yaml:"tools"`
		MainAgent   *bool    `yaml:"mainAgent"`
		Subagent    *bool    `yaml:"subagent"`
		Model       string   `yaml:"model"`
		Skills      []string `yaml:"skills"`
	}

	if err := yaml.Unmarshal(front, &raw); err != nil {
		return subagent.Document{}, false, fmt.Errorf("parse frontmatter: %w", err)
	}

	if raw.Name == "" {
		return subagent.Document{}, false, errors.New("the name field is required")
	}

	doc := subagent.Document{
		Name:        raw.Name,
		Description: raw.Description,
		Model:       raw.Model,
		Skills:      raw.Skills,
		Body:        body,
	}

	for _, name := range raw.Tools {
		if tool, ok := antigravityToolVocab.canonical(name); ok {
			doc.Tools = append(doc.Tools, tool)
		}
	}

	doc.Tools = sortToolList(doc.Tools)
	if len(doc.Tools) == 0 {
		doc.Tools = nil
	}

	switch {
	case raw.MainAgent != nil && !*raw.MainAgent:
		doc.Mode = subagentLabel
	case raw.Subagent != nil && !*raw.Subagent:
		doc.Mode = "primary"
	}

	return doc, true, nil
}

func (antigravitySubagentCodec) fields(doc subagent.Document, existing []byte) ([]subagent.Field, string, error) {
	if !subagent.ValidName(doc.Name) {
		return nil, "", errors.New("subagent name is required")
	}

	fields := []subagent.Field{{Key: nameKey, Value: doc.Name}}

	if doc.Description != "" {
		fields = append(fields, subagent.Field{Key: descriptionKey, Value: doc.Description})
	}

	if tools := antigravityTools(doc, existing); len(tools) > 0 {
		fields = append(fields, subagent.Field{Key: toolsKey, Value: tools})
	}

	mainAgent, subagentRole := antigravityRoles(doc)
	fields = append(fields,
		subagent.Field{Key: "mainAgent", Value: mainAgent},
		subagent.Field{Key: "subagent", Value: subagentRole},
	)

	if doc.Model != "" {
		fields = append(fields, subagent.Field{Key: modelKey, Value: antigravityModel(doc.Model)})
	}

	if len(doc.Skills) > 0 {
		fields = append(fields, subagent.Field{Key: "skills", Value: doc.Skills})
	}

	return fields, doc.Body, nil
}

// antigravityTools renders the allowlist: the mappable canonical tools
// followed by the host entries the codec cannot canonicalize.
func antigravityTools(doc subagent.Document, existing []byte) []string {
	var out []string

	for _, tool := range doc.Tools {
		if name, ok := antigravityToolVocab.host(tool); ok {
			out = append(out, name)
		}
	}

	extra := preserveToolExtras(existing, func(name string) bool {
		_, ok := antigravityToolVocab.canonical(name)

		return ok
	})

	return append(out, extra...)
}

// antigravityRoles maps the canonical mode to the two role flags. An unset
// mode means "a subagent, not a main agent".
func antigravityRoles(doc subagent.Document) (mainAgent, subagentRole bool) {
	switch doc.Mode {
	case primaryMode:
		return true, false
	case "all":
		return true, true
	default:
		return false, true
	}
}

// antigravityModel keeps a model Antigravity accepts; everything else falls
// back to inherit, the documented default.
func antigravityModel(model string) string {
	switch model {
	case inheritModel, "flash", "pro":
		return model
	default:
		return "inherit"
	}
}

// antigravityToolsLost reports an allowlist with no Antigravity equivalent:
// the codec writes no tools key, and the host then keeps every tool.
func antigravityToolsLost(doc subagent.Document) bool {
	if doc.Tools == nil {
		return false
	}

	for _, tool := range doc.Tools {
		if _, ok := antigravityToolVocab.host(tool); ok {
			return false
		}
	}

	return true
}

// managed lists the frontmatter keys the Antigravity codec owns.
func (antigravitySubagentCodec) managed() []string {
	return []string{nameKey, descriptionKey, toolsKey, "mainAgent", "subagent", modelKey, "skills"}
}

// strayKeys is empty: Antigravity ignores unknown frontmatter keys.
func (antigravitySubagentCodec) strayKeys([]byte) []string { return nil }

func (antigravitySubagentCodec) audit(doc subagent.Document) []string {
	var notes []string

	if antigravityToolsLost(doc) {
		notes = append(notes, "no canonical tool has an antigravity equivalent; the host keeps all tools (fail-open)")
	}

	for _, tool := range doc.Tools {
		if _, ok := antigravityToolVocab.host(tool); !ok {
			notes = append(notes, fmt.Sprintf("unmappable tool %q for antigravity; skipped", tool))
		}
	}

	if len(doc.DisallowedTools) > 0 && !subagent.ReadOnly(doc) {
		notes = append(notes, "a partial tool deny is not expressible for antigravity; write the allowlist instead")
	}

	if subagent.ReadOnly(doc) && doc.Tools == nil {
		notes = append(notes, "readonly without a tool allowlist is not expressible for antigravity; the host keeps all tools")
	}

	if !slices.Contains(antigravityModes, doc.Mode) {
		notes = append(notes, fmt.Sprintf("mode %q does not map to antigravity; the host gets a subagent", doc.Mode))
	}

	if doc.Model != "" && antigravityModel(doc.Model) != doc.Model {
		notes = append(notes, fmt.Sprintf("model %q does not map to antigravity; the host gets inherit", doc.Model))
	}

	if doc.MCPServers != nil {
		notes = append(notes, "antigravity takes inline mcpServers definitions; the canonical server list stays in the vault")
	}

	if doc.PermissionMode != "" {
		notes = append(notes, "permissionMode has no antigravity equivalent (commandExecutionPolicy differs)")
	}

	return dedupStrings(notes)
}
