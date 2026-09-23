package agent

import (
	"errors"
	"fmt"

	toml "github.com/pelletier/go-toml"

	"github.com/odiumuniverse/beadle/pkg/subagent"
)

// codexInstructionsKey is the Codex key that holds the system prompt.
const codexInstructionsKey = "developer_instructions"

// codexManagedKeys lists the top-level TOML keys the Codex codec owns.
var codexManagedKeys = []string{nameKey, descriptionKey, codexInstructionsKey, modelKey, "sandbox_mode"}

// codexSubagentCodec reads and writes Codex CLI subagent files: standalone
// TOML whose unknown keys, tables and comments must survive a write.
type codexSubagentCodec struct{}

func (codexSubagentCodec) parse(_ string, data []byte) (subagent.Document, bool, error) {
	tree, err := toml.LoadBytes(data)
	if err != nil {
		return subagent.Document{}, false, fmt.Errorf("parse toml: %w", err)
	}

	name, _ := tree.GetPath([]string{"name"}).(string)
	if name == "" {
		return subagent.Document{}, false, errors.New("the name key is required")
	}

	doc := subagent.Document{Name: name}

	if description, ok := tree.GetPath([]string{"description"}).(string); ok {
		doc.Description = description
	}

	if model, ok := tree.GetPath([]string{"model"}).(string); ok {
		doc.Model = model
	}

	if body, ok := tree.GetPath([]string{codexInstructionsKey}).(string); ok {
		doc.Body = body
	}

	if mode, ok := tree.GetPath([]string{"sandbox_mode"}).(string); ok && mode == "read-only" {
		doc.DisallowedTools = subagent.WriteClass()
	}

	return doc, true, nil
}

func (codexSubagentCodec) fields(doc subagent.Document, _ []byte) ([]subagent.Field, string, error) {
	if !subagent.ValidName(doc.Name) {
		return nil, "", errors.New("subagent name is required")
	}

	fields := []subagent.Field{{Key: nameKey, Value: doc.Name}}

	if doc.Description != "" {
		fields = append(fields, subagent.Field{Key: descriptionKey, Value: doc.Description})
	}

	if model := codexModel(doc.Model); model != "" {
		fields = append(fields, subagent.Field{Key: modelKey, Value: model})
	}

	if subagent.ReadOnly(doc) {
		fields = append(fields, subagent.Field{Key: "sandbox_mode", Value: "read-only"})
	}

	return fields, doc.Body, nil
}

// render writes the managed keys with a surgical span edit: comments, other
// keys and tables stay byte-identical. Invalid TOML is refused.
func (codexSubagentCodec) render(doc subagent.Document, existing []byte) ([]byte, error) {
	if len(existing) > 0 {
		if _, err := toml.LoadBytes(existing); err != nil {
			return nil, fmt.Errorf("parse toml: %w", err)
		}
	}

	fields, body, err := codexSubagentCodec{}.fields(doc, existing)
	if err != nil {
		return nil, err
	}

	values := make(map[string]string, len(fields)+1)
	keep := make([]string, 0, 2)

	for _, field := range fields {
		rendered, err := tomlValue(field.Value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field.Key, err)
		}

		values[field.Key] = rendered
	}

	if doc.Description == "" {
		// Codex requires the key, so the file keeps its current description.
		keep = append(keep, descriptionKey)
	}

	if body == "" {
		keep = append(keep, codexInstructionsKey)
	} else {
		values[codexInstructionsKey] = tomlMultilineValue(body)
	}

	if !subagent.ReadOnly(doc) {
		// A stale read-only sandbox contradicts the canon; every other
		// sandbox_mode is the user's own sandbox policy and stays.
		if mode, ok := existingTOMLString(existing, "sandbox_mode"); ok && mode == "read-only" {
			values["sandbox_mode"] = ""
		} else {
			keep = append(keep, "sandbox_mode")
		}
	}

	return editTOMLKeys(existing, codexManagedKeys, keep, values)
}

// managed lists the top-level TOML keys the Codex codec owns.
func (codexSubagentCodec) managed() []string { return codexManagedKeys }

// strayKeys is empty: Codex accepts arbitrary config-layer keys.
func (codexSubagentCodec) strayKeys([]byte) []string { return nil }

// codexModel keeps a model Codex can read. Codex has no inherit keyword:
// omitting the key inherits, so the Claude aliases are dropped.
func codexModel(model string) string {
	if model == "" || model == inheritModel || claudeAlias(model) {
		return ""
	}

	return model
}

func (codexSubagentCodec) audit(doc subagent.Document) []string {
	var notes []string

	if doc.Tools != nil {
		notes = append(notes, "the tool allowlist is not expressible for codex; the file keeps its tool configuration")
	}

	if len(doc.DisallowedTools) > 0 && !subagent.ReadOnly(doc) {
		notes = append(notes, "a partial tool deny is not expressible for codex; only sandbox_mode read-only is")
	}

	if doc.PermissionMode != "" {
		notes = append(notes, "permissionMode is not expressible for codex; kept in the vault")
	}

	if claudeAlias(doc.Model) {
		notes = append(notes, fmt.Sprintf("model %q is a Claude alias and does not map to codex; the host inherits its model", doc.Model))
	}

	if doc.Description == "" {
		notes = append(notes, "codex requires a description; an existing value is kept and a new file stays incomplete")
	}

	if doc.Body == "" {
		notes = append(notes, "codex requires developer_instructions; an existing value is kept and a new file stays incomplete")
	}

	if doc.MCPServers != nil || doc.Skills != nil {
		notes = append(notes, "codex configures mcp_servers and skills.config itself; the canonical list stays in the vault")
	}

	return dedupStrings(notes)
}

// existingTOMLString reads a top-level string key of a TOML document.
func existingTOMLString(data []byte, key string) (string, bool) {
	if len(data) == 0 {
		return "", false
	}

	tree, err := toml.LoadBytes(data)
	if err != nil {
		return "", false
	}

	value, ok := tree.GetPath([]string{key}).(string)

	return value, ok
}
