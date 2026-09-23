package agent

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/odiumuniverse/beadle/pkg/subagent"
)

// cursorSubagentCodec reads and writes Cursor subagent files. Cursor keeps
// name, description, model, readonly and is_background; every other
// canonical field stays in the vault.
type cursorSubagentCodec struct{}

func (cursorSubagentCodec) parse(path string, data []byte) (subagent.Document, bool, error) {
	front, body, ok := subagent.Split(data)
	if !ok {
		return subagent.Document{}, false, errors.New("no frontmatter; Cursor subagents need a description")
	}

	var raw struct {
		Name         string `yaml:"name"`
		Description  string `yaml:"description"`
		Model        string `yaml:"model"`
		ReadOnly     *bool  `yaml:"readonly"`
		IsBackground *bool  `yaml:"is_background"`
	}

	if err := yaml.Unmarshal(front, &raw); err != nil {
		return subagent.Document{}, false, fmt.Errorf("parse frontmatter: %w", err)
	}

	doc := subagent.Document{
		Name:        raw.Name,
		Description: raw.Description,
		Model:       raw.Model,
		Background:  raw.IsBackground,
		Body:        body,
	}

	if doc.Name == "" {
		// Cursor defaults the agent name to the file name.
		doc.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}

	if raw.ReadOnly != nil && *raw.ReadOnly {
		// Cursor expresses read-only without a tool list; the canonical
		// minimal honest form is the write-class deny.
		doc.DisallowedTools = subagent.WriteClass()
	}

	return doc, true, nil
}

func (cursorSubagentCodec) fields(doc subagent.Document, _ []byte) ([]subagent.Field, string, error) {
	if !subagent.ValidName(doc.Name) {
		return nil, "", errors.New("subagent name is required")
	}

	fields := []subagent.Field{{Key: nameKey, Value: doc.Name}}

	if doc.Description != "" {
		fields = append(fields, subagent.Field{Key: descriptionKey, Value: doc.Description})
	}

	if model := cursorModel(doc.Model); model != "" {
		fields = append(fields, subagent.Field{Key: modelKey, Value: model})
	}

	if doc.Background != nil {
		fields = append(fields, subagent.Field{Key: "is_background", Value: *doc.Background})
	}

	if subagent.ReadOnly(doc) {
		fields = append(fields, subagent.Field{Key: "readonly", Value: true})
	}

	return fields, doc.Body, nil
}

// managed lists the frontmatter keys the Cursor codec owns.
func (cursorSubagentCodec) managed() []string {
	return []string{nameKey, descriptionKey, modelKey, "readonly", "is_background"}
}

// strayKeys is empty: Cursor ignores unknown frontmatter keys.
func (cursorSubagentCodec) strayKeys([]byte) []string { return nil }

// cursorModel keeps a model value Cursor can read and drops the Claude
// aliases: Cursor defaults to inherit, so an alias is not guessed over.
func cursorModel(model string) string {
	if model == "" || claudeAlias(model) {
		return ""
	}

	return model
}

func (cursorSubagentCodec) audit(doc subagent.Document) []string {
	var notes []string

	if doc.Tools != nil {
		notes = append(notes, "the tool allowlist is not expressible for cursor; only readonly is")
	}

	if len(doc.DisallowedTools) > 0 && !subagent.ReadOnly(doc) {
		notes = append(notes, "a partial tool deny is not expressible for cursor; only readonly is")
	}

	if claudeAlias(doc.Model) {
		notes = append(notes, fmt.Sprintf("model %q is a Claude alias and does not map to cursor; the host keeps inherit", doc.Model))
	}

	return notes
}
