// Package frontmatter is the markdown-frontmatter engine shared by the
// canonical file kinds: it splits and encodes a YAML frontmatter block with a
// markdown body, patches managed keys while preserving comments and foreign
// keys, drops keys and merges vault/projection/update values.
package frontmatter

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// Field is one frontmatter key rendered by a host codec. A nil Value removes
// the key from the file.
type Field struct {
	Key   string
	Value any
}

// Split separates a markdown document into frontmatter content (without the
// fences) and the body. It accepts LF and CRLF fences; the body keeps its
// original line endings. A closing fence must end the line.
func Split(data []byte) (front []byte, body string, ok bool) {
	s := string(data)

	start := 4

	switch {
	case strings.HasPrefix(s, "---\n"):
	case strings.HasPrefix(s, "---\r\n"):
		start = 5
	default:
		return nil, s, false
	}

	rest := s[start:]

	before, after, found := cutFence(rest)
	if !found {
		// An empty block: the closing fence is the first line.
		if tail, empty := leadingFence(rest); empty {
			return []byte{}, tail, true
		}

		return nil, s, false
	}

	front = []byte(strings.TrimSuffix(strings.ReplaceAll(before, "\r\n", "\n"), "\r"))

	switch {
	case strings.HasPrefix(after, "\r\n"):
		body = after[2:]
	case strings.HasPrefix(after, "\n"):
		body = after[1:]
	default:
		body = after
	}

	return front, body, true
}

// Unmarshal splits a markdown document and decodes its frontmatter into dst.
// ok reports whether a frontmatter block was present; the body is returned
// either way.
func Unmarshal(data []byte, dst any) (body string, ok bool, err error) {
	front, body, ok := Split(data)
	if !ok {
		return body, false, nil
	}

	if err := yaml.Unmarshal(front, dst); err != nil {
		return "", false, fmt.Errorf("parse frontmatter: %w", err)
	}

	return body, true, nil
}

// leadingFence matches a closing fence at offset zero, the empty frontmatter
// block, and returns the body after it.
func leadingFence(rest string) (string, bool) {
	if !strings.HasPrefix(rest, "---") {
		return "", false
	}

	tail := strings.TrimLeft(rest[3:], " \t")

	switch {
	case strings.HasPrefix(tail, "\r\n"):
		return tail[2:], true
	case strings.HasPrefix(tail, "\n"):
		return tail[1:], true
	case tail == "":
		return "", true
	default:
		return "", false
	}
}

// cutFence finds a closing "---" that ends its line, ignoring trailing
// spaces or tabs, so they never leak into the body.
func cutFence(rest string) (before, after string, ok bool) {
	offset := 0

	for {
		idx := strings.Index(rest[offset:], "\n---")
		if idx < 0 {
			return "", "", false
		}

		start := offset + idx
		tail := strings.TrimLeft(rest[start+4:], " \t")

		if tail == "" || tail[0] == '\n' || strings.HasPrefix(tail, "\r\n") {
			return rest[:start], tail, true
		}

		offset = start + 1
	}
}

// Compose builds a markdown document from frontmatter fields and a body.
func Compose(fields []Field, body string) []byte {
	root := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}

	mapping := root.Content[0]

	for _, field := range fields {
		if field.Value == nil {
			continue
		}

		node, err := NodeFor(field.Value)
		if err != nil {
			continue
		}

		mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: field.Key}, node)
	}

	out, err := yaml.Marshal(root)
	if err != nil {
		return nil
	}

	return encode(out, body)
}

// Patch rewrites the frontmatter of existing: it drops every managed key,
// appends the rendered fields in order and replaces the body. Keys outside
// the managed set, their comments and the file's formatting are preserved.
func Patch(existing []byte, managed []string, fields []Field, body string) ([]byte, error) {
	front, _, ok := Split(existing)

	root := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}

	if ok {
		if err := yaml.Unmarshal(front, root); err != nil {
			return nil, fmt.Errorf("parse frontmatter: %w", err)
		}

		if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
			return nil, errors.New("frontmatter is not a mapping")
		}
	}

	mapping := root.Content[0]

	RemoveKeys(mapping, managed)

	for _, field := range fields {
		if field.Value == nil {
			continue
		}

		node, err := NodeFor(field.Value)
		if err != nil {
			continue
		}

		mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: field.Key}, node)
	}

	out, err := yaml.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("encode frontmatter: %w", err)
	}

	return encode(out, body), nil
}

// Drop removes the given frontmatter keys, keeping the other keys, their
// comments and the body.
func Drop(data []byte, keys []string) ([]byte, error) {
	front, body, ok := Split(data)
	if !ok {
		return data, nil
	}

	root := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}

	if err := yaml.Unmarshal(front, root); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}

	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return data, nil
	}

	RemoveKeys(root.Content[0], keys)

	out, err := yaml.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("encode frontmatter: %w", err)
	}

	return encode(out, body), nil
}

// encode wraps marshalled frontmatter and a body into a markdown document. An
// empty mapping is not written: a document without fields is just a body.
func encode(front []byte, body string) []byte {
	normalized := NormalizeBody(body)

	switch strings.TrimSpace(string(front)) {
	case "", "{}":
		return []byte(normalized)
	}

	var b strings.Builder

	b.WriteString("---\n")
	b.Write(front)
	b.WriteString("---\n")
	b.WriteString(normalized)

	return []byte(b.String())
}

// RemoveKeys drops the pairs whose key is in keys from a mapping node. A
// comment above the first key is carried to the next kept key, so a
// document-level comment survives the removal.
func RemoveKeys(mapping *yaml.Node, keys []string) {
	kept := make([]*yaml.Node, 0, len(mapping.Content))

	var documentComment string

	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key := mapping.Content[i]

		if slices.Contains(keys, key.Value) {
			if len(kept) == 0 {
				documentComment = key.HeadComment
			}

			continue
		}

		if documentComment != "" && key.HeadComment == "" {
			key.HeadComment = documentComment

			documentComment = ""
		}

		kept = append(kept, key, mapping.Content[i+1])
	}

	mapping.Content = kept
}

// NormalizeBody trims the trailing newlines of a body and keeps exactly one.
func NormalizeBody(body string) string {
	trimmed := strings.TrimRight(body, "\r\n")

	if trimmed == "" {
		return ""
	}

	return trimmed + "\n"
}

// MergeScalar merges one scalar field: an unchanged projection keeps the
// vault value, an empty update clears it, otherwise the update wins.
func MergeScalar[T comparable](v, p, u T) T {
	var zero T

	if u == p {
		return v
	}

	if u == zero {
		return zero
	}

	return u
}

// MergeList merges one string list field.
func MergeList(v, p, u []string) []string {
	if slices.Equal(p, u) {
		return v
	}

	if len(u) == 0 {
		return nil
	}

	return u
}

// MergeAnyList merges one untyped list field.
func MergeAnyList(v, p, u []any) []any {
	if reflect.DeepEqual(p, u) {
		return v
	}

	if len(u) == 0 {
		return nil
	}

	return u
}

// MergeBoolPtr merges one optional boolean field.
func MergeBoolPtr(v, p, u *bool) *bool {
	if boolPtrEqual(p, u) {
		return v
	}

	if u == nil {
		return nil
	}

	value := *u

	return &value
}

func boolPtrEqual(a, b *bool) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

// ValidSlug reports whether name is a canonical file-item slug: lowercase
// letters, digits, hyphens and underscores, starting with a letter or digit.
func ValidSlug(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}

	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}

		if i == 0 && (r == '-' || r == '_') {
			return false
		}
	}

	return true
}

// StringList decodes a YAML scalar (comma or space separated) or a sequence.
type StringList []string

// UnmarshalYAML decodes a scalar or a sequence into a list of strings.
func (l *StringList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.SequenceNode:
		out := make([]string, 0, len(node.Content))

		for _, item := range node.Content {
			out = append(out, strings.TrimSpace(item.Value))
		}

		*l = out

		return nil
	case yaml.ScalarNode:
		fields := strings.FieldsFunc(node.Value, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t'
		})
		out := make([]string, 0, len(fields))

		for _, field := range fields {
			if field = strings.TrimSpace(field); field != "" {
				out = append(out, field)
			}
		}

		*l = out

		return nil
	default:
		return fmt.Errorf("expected a string or a list, got %s", node.Tag)
	}
}

// NodeFor encodes a Go value as a YAML node; scalar sequences render inline.
func NodeFor(value any) (*yaml.Node, error) {
	data, err := yaml.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode value: %w", err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode value: %w", err)
	}

	if len(doc.Content) == 0 {
		return nil, errors.New("empty value")
	}

	node := doc.Content[0]

	if node.Kind == yaml.SequenceNode && scalarsOnly(node) {
		node.Style = yaml.FlowStyle
	}

	return node, nil
}

func scalarsOnly(node *yaml.Node) bool {
	for _, item := range node.Content {
		if item.Kind != yaml.ScalarNode {
			return false
		}
	}

	return true
}
