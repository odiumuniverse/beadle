package agent

import (
	"bytes"
	"fmt"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// This file is the YAML plumbing of the DSH MCP writer: the patch document
// model, node lookups and the node builders the writer splices into the user's
// file. The writer edits the parsed node tree and re-encodes the document
// once, so comments, entry order and scalar styles survive a write.

// dshPatchHeader seeds the patch file beadle creates. DSH never creates the
// home layer itself, so the first write explains the file.
const dshPatchHeader = `MCP servers for every DSH profile: the home patch layer DSH applies after each profile's own layer.
beadle manages only the entries whose id starts with "beadle:"; other entries and comments stay untouched.`

// dshPatchDocument is a parsed DSH home patch layer: a top-level YAML array of
// loader patch entries, the shape @deepseek-ai/dsh-app-boot requires.
type dshPatchDocument struct {
	doc  *yaml.Node
	root *yaml.Node
}

// newDSHPatch builds an empty patch document: a block-style top-level array
// carrying the writer's header comment.
func newDSHPatch() *dshPatchDocument {
	root := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", HeadComment: dshPatchHeader}

	return &dshPatchDocument{doc: &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}, root: root}
}

// loadDSHPatch parses a patch file. An empty file and a non-array document are
// errors: DSH fails loud on both (an empty file loads as null, not as an
// empty array), so beadle refuses to guess.
func loadDSHPatch(path string, data []byte) (*dshPatchDocument, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("%s: empty patch file; DSH needs a top-level array (write [] or remove the file)", path)
	}

	var doc yaml.Node

	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s: DSH needs a top-level YAML array of patch entries", path)
	}

	return &dshPatchDocument{doc: &doc, root: doc.Content[0]}, nil
}

// encodeDSHPatch renders the document with the given indent width.
func encodeDSHPatch(doc *yaml.Node, indent int) ([]byte, error) {
	var buf bytes.Buffer

	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(indent)

	if err := encoder.Encode(doc); err != nil {
		return nil, fmt.Errorf("encode patch: %w", err)
	}

	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("encode patch: %w", err)
	}

	return buf.Bytes(), nil
}

// dshPatchIndent picks the indent width that re-encodes the parsed document
// byte-for-byte, so a write keeps the file's layout. A file whose formatting
// the node round-trip normalizes anywhere (a relocated standalone comment, a
// re-spaced inline comment) matches no width and falls back to two spaces, the
// width DSH's own config dump uses.
func dshPatchIndent(doc *dshPatchDocument, data []byte) int {
	for indent := 2; indent <= 9; indent++ {
		out, err := encodeDSHPatch(doc.doc, indent)
		if err == nil && bytes.Equal(out, data) {
			return indent
		}
	}

	return 2
}

// dshMapIndex returns the index of a mapping key's value node, or -1.
func dshMapIndex(mapping *yaml.Node, key string) int {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return -1
	}

	index := -1

	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Kind == yaml.ScalarNode && mapping.Content[i].Value == key {
			index = i + 1
		}
	}

	return index
}

// dshMapValue returns the value node of a mapping key. YAML resolves a
// duplicated key to its last occurrence, and so does this lookup.
func dshMapValue(mapping *yaml.Node, key string) *yaml.Node {
	if index := dshMapIndex(mapping, key); index >= 0 {
		return mapping.Content[index]
	}

	return nil
}

// dshScalar reads a scalar node as a string. Numbers, booleans, nulls and
// tagged scalars (such as `!!js` expressions) are not strings, and DSH's
// schema rejects them wherever beadle expects one, so they do not count.
func dshScalar(node *yaml.Node) (string, bool) {
	if node == nil || node.Kind != yaml.ScalarNode {
		return "", false
	}

	switch node.Tag {
	case "", "!!str":
		return node.Value, true
	default:
		return "", false
	}
}

// dshBool reads a boolean scalar node.
func dshBool(node *yaml.Node) (bool, bool) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!bool" {
		return false, false
	}

	return strings.EqualFold(node.Value, "true"), true
}

// dshString builds a string scalar node. The resolved tag keeps values such as
// "true" or "123" strings on the way back in.
func dshString(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

// dshMapping builds a block mapping node from alternating key/value nodes.
func dshMapping(pairs ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: pairs}
}

// dshSequence builds a block sequence node.
func dshSequence(items ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: items}
}

// dshSetConfig replaces a record's config value, or appends the key when the
// record has none. Every other record key and comment stays as it is.
func dshSetConfig(record, config *yaml.Node) {
	if index := dshMapIndex(record, "config"); index >= 0 {
		record.Content[index] = config

		return
	}

	record.Content = append(record.Content, dshString("config"), config)
}

// dshDropRecords removes the marked record mappings from the insert lists of a
// patch document and prunes the insert-only entries a removal left empty.
// Records outside insert lists are never touched.
func dshDropRecords(root *yaml.Node, drop map[*yaml.Node]bool) {
	kept := make([]*yaml.Node, 0, len(root.Content))

	for _, entry := range root.Content {
		if entry.Kind == yaml.MappingNode {
			if insert := dshMapValue(entry, "insert"); insert != nil && insert.Kind == yaml.SequenceNode {
				dshDropBelow(insert, drop)

				if len(insert.Content) == 0 && len(entry.Content) == 2 {
					continue
				}
			}
		}

		kept = append(kept, entry)
	}

	root.Content = kept
}

// dshDropBelow removes the marked record mappings anywhere under an insert
// list, including the config lists of inserted groups.
func dshDropBelow(node *yaml.Node, drop map[*yaml.Node]bool) {
	switch node.Kind {
	case yaml.SequenceNode:
		kept := make([]*yaml.Node, 0, len(node.Content))

		for _, item := range node.Content {
			if drop[item] {
				continue
			}

			dshDropBelow(item, drop)
			kept = append(kept, item)
		}

		node.Content = kept
	case yaml.MappingNode:
		for i := 1; i < len(node.Content); i += 2 {
			dshDropBelow(node.Content[i], drop)
		}
	default:
		// Scalars, aliases and documents hold no records.
	}
}
