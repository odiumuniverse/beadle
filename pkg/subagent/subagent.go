// Package subagent defines the canonical form of a custom subagent: a
// markdown document whose YAML frontmatter carries the portable fields and
// whose body becomes the host's system prompt.
package subagent

import (
	"fmt"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/frontmatter"
)

// Document is the canonical form of a custom subagent.
type Document struct {
	Name            string
	Description     string
	Mode            string
	Model           string
	Tools           []string
	DisallowedTools []string
	PermissionMode  string
	MaxTurns        int
	Skills          []string
	MCPServers      []any
	Background      *bool
	Isolation       string
	Color           string
	Hidden          *bool
	Body            string
}

// Field is one frontmatter key rendered by a host codec. A nil Value removes
// the key from the file.
type Field = frontmatter.Field

// canonicalOrder lists the canonical frontmatter keys in render order.
var canonicalOrder = []string{
	"name", "description", "mode", "model", "tools", "disallowedTools",
	"permissionMode", "maxTurns", "skills", "mcpServers", "background",
	"isolation", "color", "hidden",
}

// Keys returns the canonical frontmatter keys in render order.
func Keys() []string {
	return slices.Clone(canonicalOrder)
}

// Parse decodes a markdown document with optional YAML frontmatter. A missing
// frontmatter block yields a document with the body only.
func Parse(data []byte) (Document, error) {
	var raw struct {
		Name            string                 `yaml:"name"`
		Description     string                 `yaml:"description"`
		Mode            string                 `yaml:"mode"`
		Model           string                 `yaml:"model"`
		Tools           frontmatter.StringList `yaml:"tools"`
		DisallowedTools frontmatter.StringList `yaml:"disallowedTools"`
		PermissionMode  string                 `yaml:"permissionMode"`
		MaxTurns        int                    `yaml:"maxTurns"`
		Skills          frontmatter.StringList `yaml:"skills"`
		MCPServers      []any                  `yaml:"mcpServers"`
		Background      *bool                  `yaml:"background"`
		Isolation       string                 `yaml:"isolation"`
		Color           string                 `yaml:"color"`
		Hidden          *bool                  `yaml:"hidden"`
	}

	body, ok, err := frontmatter.Unmarshal(data, &raw)
	if err != nil {
		return Document{}, fmt.Errorf("parse frontmatter: %w", err)
	}

	doc := Document{Body: frontmatter.NormalizeBody(body)}
	if !ok {
		return doc, nil
	}

	doc.Name = strings.TrimSpace(raw.Name)
	doc.Description = raw.Description
	doc.Mode = raw.Mode
	doc.Model = raw.Model
	doc.Tools = raw.Tools
	doc.DisallowedTools = raw.DisallowedTools
	doc.PermissionMode = raw.PermissionMode
	doc.MaxTurns = raw.MaxTurns
	doc.Skills = raw.Skills
	doc.MCPServers = raw.MCPServers
	doc.Background = raw.Background
	doc.Isolation = raw.Isolation
	doc.Color = raw.Color
	doc.Hidden = raw.Hidden

	return doc, nil
}

// Render encodes a document as canonical markdown: frontmatter with the
// non-empty fields in canonical order, then the body.
func Render(doc Document) []byte {
	return frontmatter.Compose(Fields(doc), doc.Body)
}

// Fields renders the non-empty canonical frontmatter fields of doc in order.
func Fields(doc Document) []Field {
	candidates := []struct {
		key   string
		value any
		ok    bool
	}{
		{"name", doc.Name, doc.Name != ""},
		{"description", doc.Description, doc.Description != ""},
		{"mode", doc.Mode, doc.Mode != ""},
		{"model", doc.Model, doc.Model != ""},
		{"tools", doc.Tools, len(doc.Tools) > 0},
		{"disallowedTools", doc.DisallowedTools, len(doc.DisallowedTools) > 0},
		{"permissionMode", doc.PermissionMode, doc.PermissionMode != ""},
		{"maxTurns", doc.MaxTurns, doc.MaxTurns > 0},
		{"skills", doc.Skills, len(doc.Skills) > 0},
		{"mcpServers", doc.MCPServers, len(doc.MCPServers) > 0},
		{"background", boolValue(doc.Background), doc.Background != nil},
		{"isolation", doc.Isolation, doc.Isolation != ""},
		{"color", doc.Color, doc.Color != ""},
		{"hidden", boolValue(doc.Hidden), doc.Hidden != nil},
	}

	fields := make([]Field, 0, len(candidates))

	for _, candidate := range candidates {
		if !candidate.ok {
			continue
		}

		fields = append(fields, Field{Key: candidate.key, Value: candidate.value})
	}

	return fields
}

func boolValue(value *bool) any {
	if value == nil {
		return nil
	}

	return *value
}

// Split separates a markdown document into frontmatter content and the body.
func Split(data []byte) (front []byte, body string, ok bool) {
	return frontmatter.Split(data)
}

// Compose builds a markdown document from frontmatter fields and a body.
func Compose(fields []Field, body string) []byte {
	return frontmatter.Compose(fields, body)
}

// Patch rewrites the frontmatter of existing, dropping every managed key and
// appending the rendered fields; keys outside the managed set, their comments
// and the file's formatting are preserved.
func Patch(existing []byte, managed []string, fields []Field, body string) ([]byte, error) {
	return frontmatter.Patch(existing, managed, fields, body)
}

// Drop removes the given frontmatter keys, keeping the other keys, their
// comments and the body.
func Drop(data []byte, keys []string) ([]byte, error) {
	return frontmatter.Drop(data, keys)
}

// writeClass lists the canonical tools that modify files. A document that
// cannot reach any of them is read-only.
var writeClass = []string{"Write", "Edit", "NotebookEdit"}

// WriteClass returns the canonical file-modifying tool names.
func WriteClass() []string {
	return slices.Clone(writeClass)
}

// ReadOnly reports the derived read-only state: no write-class tool is
// available to the agent. The document names an allowlist without any
// write-class tool, or it denies every write-class tool.
func ReadOnly(doc Document) bool {
	if doc.Tools != nil {
		for _, tool := range doc.Tools {
			if slices.Contains(writeClass, tool) {
				return false
			}
		}

		return true
	}

	for _, tool := range writeClass {
		if !slices.Contains(doc.DisallowedTools, tool) {
			return false
		}
	}

	return len(doc.DisallowedTools) > 0
}

// Lift merges the agent's document into the vault document. Fields the
// surface cannot express (absent from projected) keep the vault value;
// fields the agent changed win; fields the agent dropped are removed.
func Lift(vault, projected, updated []byte) []byte {
	v, err := Parse(vault)
	if err != nil {
		return updated
	}

	p, err := Parse(projected)
	if err != nil {
		return updated
	}

	u, err := Parse(updated)
	if err != nil {
		return updated
	}

	out := v
	out.Name = frontmatter.MergeScalar(v.Name, p.Name, u.Name)
	out.Description = frontmatter.MergeScalar(v.Description, p.Description, u.Description)
	out.Mode = frontmatter.MergeScalar(v.Mode, p.Mode, u.Mode)
	out.Model = frontmatter.MergeScalar(v.Model, p.Model, u.Model)
	out.Tools = frontmatter.MergeList(v.Tools, p.Tools, u.Tools)
	out.DisallowedTools = frontmatter.MergeList(v.DisallowedTools, p.DisallowedTools, u.DisallowedTools)
	out.PermissionMode = frontmatter.MergeScalar(v.PermissionMode, p.PermissionMode, u.PermissionMode)
	out.MaxTurns = frontmatter.MergeScalar(v.MaxTurns, p.MaxTurns, u.MaxTurns)
	out.Skills = frontmatter.MergeList(v.Skills, p.Skills, u.Skills)
	out.MCPServers = frontmatter.MergeAnyList(v.MCPServers, p.MCPServers, u.MCPServers)
	out.Background = frontmatter.MergeBoolPtr(v.Background, p.Background, u.Background)
	out.Isolation = frontmatter.MergeScalar(v.Isolation, p.Isolation, u.Isolation)
	out.Color = frontmatter.MergeScalar(v.Color, p.Color, u.Color)
	out.Hidden = frontmatter.MergeBoolPtr(v.Hidden, p.Hidden, u.Hidden)
	out.Body = frontmatter.MergeScalar(v.Body, p.Body, u.Body)

	return Render(out)
}

// ValidName reports whether name is a canonical subagent slug: lowercase
// letters, digits, hyphens and underscores, starting with a letter or digit.
func ValidName(name string) bool {
	return frontmatter.ValidSlug(name)
}
