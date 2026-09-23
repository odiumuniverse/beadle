// Package command defines the canonical form of a custom command: a markdown
// document whose YAML frontmatter carries the portable fields and whose body
// is the prompt template. The command name is the file name and is never
// rendered into the frontmatter.
package command

import (
	"fmt"
	"slices"

	"github.com/odiumuniverse/beadle/pkg/frontmatter"
)

// Document is the canonical form of a custom command.
type Document struct {
	Name                   string
	Description            string
	ArgumentHint           string
	Arguments              []string
	Model                  string
	DisableModelInvocation *bool
	Body                   string
}

// Field is one frontmatter key rendered by a host codec. A nil Value removes
// the key from the file.
type Field = frontmatter.Field

// canonicalOrder lists the canonical frontmatter keys in render order.
var canonicalOrder = []string{
	"description", "argument-hint", "arguments", "model", "disable-model-invocation",
}

// Keys returns the canonical frontmatter keys in render order.
func Keys() []string {
	return slices.Clone(canonicalOrder)
}

// Parse decodes a markdown document with optional YAML frontmatter. A missing
// frontmatter block yields a document with the body only. The name is the
// file name and is set by the caller.
func Parse(data []byte) (Document, error) {
	var raw struct {
		Description            string                 `yaml:"description"`
		ArgumentHint           string                 `yaml:"argument-hint"`
		Arguments              frontmatter.StringList `yaml:"arguments"`
		Model                  string                 `yaml:"model"`
		DisableModelInvocation *bool                  `yaml:"disable-model-invocation"`
	}

	body, ok, err := frontmatter.Unmarshal(data, &raw)
	if err != nil {
		return Document{}, fmt.Errorf("parse frontmatter: %w", err)
	}

	doc := Document{Body: frontmatter.NormalizeBody(body)}
	if !ok {
		return doc, nil
	}

	doc.Description = raw.Description
	doc.ArgumentHint = raw.ArgumentHint
	doc.Arguments = raw.Arguments
	doc.Model = raw.Model
	doc.DisableModelInvocation = raw.DisableModelInvocation

	return doc, nil
}

// Render encodes a document as canonical markdown: frontmatter with the
// non-empty fields in canonical order, then the template.
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
		{"description", doc.Description, doc.Description != ""},
		{"argument-hint", doc.ArgumentHint, doc.ArgumentHint != ""},
		{"arguments", doc.Arguments, len(doc.Arguments) > 0},
		{"model", doc.Model, doc.Model != ""},
		{"disable-model-invocation", boolValue(doc.DisableModelInvocation), doc.DisableModelInvocation != nil},
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
	out.Description = frontmatter.MergeScalar(v.Description, p.Description, u.Description)
	out.ArgumentHint = frontmatter.MergeScalar(v.ArgumentHint, p.ArgumentHint, u.ArgumentHint)
	out.Arguments = frontmatter.MergeList(v.Arguments, p.Arguments, u.Arguments)
	out.Model = frontmatter.MergeScalar(v.Model, p.Model, u.Model)
	out.DisableModelInvocation = frontmatter.MergeBoolPtr(v.DisableModelInvocation, p.DisableModelInvocation, u.DisableModelInvocation)
	out.Body = frontmatter.MergeScalar(v.Body, p.Body, u.Body)

	out.Name = v.Name

	return Render(out)
}

// ValidName reports whether name is a canonical command slug: lowercase
// letters, digits, hyphens and underscores, starting with a letter or digit.
func ValidName(name string) bool {
	return frontmatter.ValidSlug(name)
}
