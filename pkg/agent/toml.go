package agent

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml"

	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

const tomlMCPServersKey = "mcp_servers"

type tomlMCPSurface struct {
	file   func() string
	codec  mcpCodec
	traits Traits
}

func (s *tomlMCPSurface) Kind() kind.ID { return kind.MCP }

func (s *tomlMCPSurface) Path() string { return s.file() }

func (s *tomlMCPSurface) WatchPaths() []string { return []string{s.file()} }

func (s *tomlMCPSurface) Traits() Traits { return s.traits }

func (s *tomlMCPSurface) Read(context.Context) (Snapshot, error) {
	path := s.file()

	data, present, err := readFile(path)
	if err != nil {
		return Snapshot{}, err
	}

	if !present {
		return Snapshot{Items: kind.Items{}}, nil
	}

	tree, err := toml.LoadBytes(data)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%s: parse toml: %w", path, err)
	}

	entries := tomlTableEntries(tomlMCPServers(tree))
	items := make(kind.Items, len(entries))

	for name, entry := range entries {
		if server, ok := s.codec.decode(entry); ok {
			items[name] = mcp.Encode(server)
		}
	}

	return Snapshot{Items: items, Present: true}, nil
}

func (s *tomlMCPSurface) Write(_ context.Context, desired kind.Items) error {
	path := s.file()

	return updateFile(path, 0o600, func(data []byte, _ bool) ([]byte, bool, error) {
		out, changed, err := s.rewrite(data, desired)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", path, err)
		}

		if !changed {
			return nil, false, nil
		}

		return out, true, nil
	})
}

func (s *tomlMCPSurface) rewrite(data []byte, desired kind.Items) ([]byte, bool, error) {
	tree, err := toml.LoadBytes(data)
	if err != nil {
		return nil, false, fmt.Errorf("parse toml: %w", err)
	}

	table := tomlMCPServers(tree)
	entries := tomlTableEntries(table)
	eol := tomlEOL(data)

	renders, order, err := s.plannedServers(entries, desired, eol)
	if err != nil {
		return nil, false, err
	}

	if table == nil {
		if len(order) == 0 {
			return nil, false, nil
		}

		return appendTOMLBlocks(data, joinBlocks(renders, order, eol), eol), true, nil
	}

	ix := newTOMLIndex(data)

	children, err := tomlChildren(ix, table)
	if err != nil {
		return nil, false, err
	}

	tables := tomlTables(tree, nil)

	out, inserts, changed, err := s.spliceChildren(ix, data, tables, children, entries, desired, renders)
	if err != nil {
		return nil, false, err
	}

	if len(inserts) > 0 {
		out = appendTOMLBlocks(out, joinBlocks(renders, inserts, eol), eol)
	}

	if !changed {
		return nil, false, nil
	}

	return out, true, nil
}

func (s *tomlMCPSurface) plannedServers(entries map[string]map[string]any, desired kind.Items, eol string) (map[string][]byte, []string, error) {
	renders := map[string][]byte{}

	var order []string

	for _, name := range slices.Sorted(maps.Keys(desired)) {
		if current, managed := s.codec.decode(entries[name]); managed && bytes.Equal(mcp.Encode(current), desired[name]) {
			continue
		}

		server, err := mcp.Decode(desired[name])
		if err != nil {
			return nil, nil, fmt.Errorf("server %s: %w", name, err)
		}

		fields := s.codec.encode(server)

		for key, value := range entries[name] {
			if !slices.Contains(s.codec.owned, key) {
				fields[key] = value
			}
		}

		block, err := renderServerBlock(name, fields, eol)
		if err != nil {
			return nil, nil, err
		}

		renders[name] = block
		order = append(order, name)
	}

	return renders, order, nil
}

func (s *tomlMCPSurface) spliceChildren(
	ix tomlLineIndex, data []byte, tables []tomlTable, children []tomlChild,
	entries map[string]map[string]any, desired kind.Items, renders map[string][]byte,
) ([]byte, []string, bool, error) {
	var cuts []tomlCut

	childSet := make(map[string]struct{}, len(children))
	changed := false

	for i, child := range children {
		childSet[child.name] = struct{}{}

		childStart := ix.offset(child.line)
		mainEnd := max(ix.trimTrailingComments(ix.offsetEnd(tomlChildEndLine(tables, children, i))), childStart)

		block, has := renders[child.name]
		drop := !has && !keepDesired(desired, child.name) && s.managed(entries[child.name])

		if !has && !drop {
			continue
		}

		if err := tomlValidateOrder(ix, tables, child); err != nil {
			return nil, nil, false, err
		}

		changed = true

		var replacement []byte
		if has {
			replacement = block
		}

		cuts = append(cuts, tomlCut{start: childStart, end: mainEnd, data: replacement})
		cuts = append(cuts, tomlStrayDescendants(ix, tables, child, childStart, mainEnd)...)
	}

	out := applyTOMLCuts(data, cuts)

	var inserts []string

	for _, name := range slices.Sorted(maps.Keys(renders)) {
		if _, exists := childSet[name]; !exists {
			inserts = append(inserts, name)
		}
	}

	return out, inserts, changed || len(inserts) > 0, nil
}

type tomlCut struct {
	start int
	end   int
	data  []byte
}

func tomlStrayDescendants(ix tomlLineIndex, tables []tomlTable, child tomlChild, start, end int) []tomlCut {
	var out []tomlCut

	for _, table := range tables {
		if !tomlDescendant(table.path, child.name) {
			continue
		}

		descendant := ix.offset(table.line)
		if descendant >= start && descendant < end {
			continue
		}

		descendantEnd := ix.trimTrailingComments(ix.offsetEnd(tomlNextTableLine(tables, table.line)))
		out = append(out, tomlCut{start: descendant, end: max(descendantEnd, descendant)})
	}

	return out
}

func tomlNextTableLine(tables []tomlTable, line int) int {
	next := 0

	for _, table := range tables {
		if table.line <= line {
			continue
		}

		if next == 0 || table.line < next {
			next = table.line
		}
	}

	return next
}

func applyTOMLCuts(data []byte, cuts []tomlCut) []byte {
	slices.SortFunc(cuts, func(a, b tomlCut) int { return cmp.Compare(a.start, b.start) })

	out := make([]byte, 0, len(data))
	cursor := 0

	for _, cut := range cuts {
		if cut.start < cursor {
			continue
		}

		out = append(out, data[cursor:cut.start]...)
		out = append(out, cut.data...)

		cursor = cut.end
	}

	return append(out, data[cursor:]...)
}

func (s *tomlMCPSurface) managed(entry map[string]any) bool {
	_, ok := s.codec.decode(entry)

	return ok
}

func keepDesired(desired kind.Items, name string) bool {
	_, ok := desired[name]

	return ok
}

func tomlMCPServers(tree *toml.Tree) *toml.Tree {
	table, _ := tree.GetPath([]string{tomlMCPServersKey}).(*toml.Tree)

	return table
}

func tomlTableEntries(table *toml.Tree) map[string]map[string]any {
	if table == nil {
		return nil
	}

	out := make(map[string]map[string]any, len(table.Keys()))

	for _, name := range table.Keys() {
		child, ok := table.GetPath([]string{name}).(*toml.Tree)
		if !ok {
			continue
		}

		out[name] = child.ToMap()
	}

	return out
}

type tomlChild struct {
	name string
	line int
}

func tomlChildren(ix tomlLineIndex, table *toml.Tree) ([]tomlChild, error) {
	children := make([]tomlChild, 0, len(table.Keys()))

	for _, name := range table.Keys() {
		line := table.GetPositionPath([]string{name}).Line
		if line == 0 {
			found, err := findTOMLAssignment(ix, name, 1, 0)
			if err != nil {
				return nil, err
			}

			line = found
		}

		children = append(children, tomlChild{name: name, line: line})
	}

	slices.SortFunc(children, func(a, b tomlChild) int { return cmp.Compare(a.line, b.line) })

	return children, nil
}

func tomlChildEndLine(tables []tomlTable, children []tomlChild, i int) int {
	child := children[i]
	end := 0

	for _, table := range tables {
		if table.line <= child.line || tomlDescendant(table.path, child.name) {
			continue
		}

		if end == 0 || table.line < end {
			end = table.line
		}
	}

	if i+1 < len(children) && children[i+1].line > child.line && (end == 0 || children[i+1].line < end) {
		end = children[i+1].line
	}

	return end
}

func tomlDescendant(path []string, name string) bool {
	return len(path) > 2 && path[0] == tomlMCPServersKey && path[1] == name
}

func tomlValidateOrder(ix tomlLineIndex, tables []tomlTable, child tomlChild) error {
	text := strings.TrimSpace(ix.line(child.line))

	if strings.HasPrefix(text, "[") && !tomlHeaderFor(text, child.name) {
		return fmt.Errorf("%s.%s: table header %q does not match the server", tomlMCPServersKey, child.name, text)
	}

	for _, table := range tables {
		if tomlDescendant(table.path, child.name) && table.line < child.line {
			return fmt.Errorf("%s.%s: subtable %s is declared before its table", tomlMCPServersKey, child.name, strings.Join(table.path, "."))
		}
	}

	return nil
}

func tomlHeaderFor(line, name string) bool {
	header, ok := strings.CutPrefix(line, "[")
	if !ok {
		return false
	}

	header, _, ok = strings.Cut(header, "]")
	if !ok {
		return false
	}

	header = strings.TrimSpace(header)

	for _, form := range tomlKeyForms(name) {
		if header == tomlMCPServersKey+"."+form {
			return true
		}
	}

	return false
}

type tomlTable struct {
	path []string
	line int
}

func tomlTables(tree *toml.Tree, prefix []string) []tomlTable {
	var out []tomlTable

	for _, key := range tree.Keys() {
		path := append(append([]string{}, prefix...), key)

		switch node := tree.GetPath([]string{key}).(type) {
		case *toml.Tree:
			if line := tree.GetPositionPath([]string{key}).Line; line > 0 {
				out = append(out, tomlTable{path: path, line: line})
			}

			out = append(out, tomlTables(node, path)...)
		case []*toml.Tree:
			for _, item := range node {
				if line := item.Position().Line; line > 0 {
					out = append(out, tomlTable{path: path, line: line})
				}

				out = append(out, tomlTables(item, path)...)
			}
		}
	}

	return out
}

func findTOMLAssignment(ix tomlLineIndex, name string, fromLine, toLine int) (int, error) {
	last := len(ix.starts)
	if toLine > 0 && toLine-1 < last {
		last = toLine - 1
	}

	forms := tomlKeyForms(name)
	inTable := false

	for line := fromLine; line <= last; line++ {
		trimmed := strings.TrimLeft(ix.line(line), " \t")

		if strings.HasPrefix(trimmed, "[") {
			inTable = isMCPServersHeader(trimmed)

			continue
		}

		for _, form := range forms {
			if (inTable && tomlAssignment(trimmed, form)) ||
				(!inTable && tomlAssignment(trimmed, tomlMCPServersKey+"."+form)) {
				return line, nil
			}
		}
	}

	return 0, fmt.Errorf("cannot locate inline server %q in the %s table", name, tomlMCPServersKey)
}

func isMCPServersHeader(line string) bool {
	header, ok := strings.CutPrefix(line, "[")
	if !ok {
		return false
	}

	header, _, ok = strings.Cut(header, "]")
	if !ok {
		return false
	}

	return strings.TrimSpace(header) == tomlMCPServersKey
}

func tomlAssignment(line, key string) bool {
	rest, ok := strings.CutPrefix(line, key)
	if !ok {
		return false
	}

	return strings.HasPrefix(strings.TrimLeft(rest, " \t"), "=")
}

func tomlKeyForms(name string) []string {
	if isBareTOMLKey(name) {
		return []string{name}
	}

	return []string{tomlBasicString(name), "'" + name + "'"}
}

type tomlLineIndex struct {
	data   []byte
	starts []int
}

func newTOMLIndex(data []byte) tomlLineIndex {
	starts := []int{0}

	for i, b := range data {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}

	return tomlLineIndex{data: data, starts: starts}
}

func (ix tomlLineIndex) offset(line int) int {
	if line <= 1 {
		return 0
	}

	if line-1 >= len(ix.starts) {
		return len(ix.data)
	}

	return ix.starts[line-1]
}

func (ix tomlLineIndex) offsetEnd(line int) int {
	if line == 0 {
		return len(ix.data)
	}

	return ix.offset(line)
}

func (ix tomlLineIndex) trimTrailingComments(end int) int {
	for end > 0 {
		start := ix.previousLineStart(end)

		text := strings.TrimSpace(string(ix.data[start:end]))
		if text != "" && !strings.HasPrefix(text, "#") {
			break
		}

		end = start
	}

	return end
}

func (ix tomlLineIndex) previousLineStart(end int) int {
	index := sort.SearchInts(ix.starts, end) - 1
	if index <= 0 {
		return 0
	}

	return ix.starts[index]
}

func (ix tomlLineIndex) line(n int) string {
	start := ix.offset(n)
	end := len(ix.data)

	if n < len(ix.starts) {
		end = ix.starts[n]
	}

	return string(ix.data[start:end])
}

// renderServerBlock renders one [mcp_servers.<name>] table in the canonical
// form: keys at the top level of the table, sorted, no indentation. The block
// carries no trailing blank line — the splice keeps whatever separated the
// table before, and the append path inserts the separator, so a rewrite
// touches only the table's own lines.
func renderServerBlock(name string, entry map[string]any, eol string) ([]byte, error) {
	var b strings.Builder

	b.WriteString("[")
	b.WriteString(tomlMCPServersKey)
	b.WriteString(".")
	b.WriteString(tomlKey(name))
	b.WriteString("]")
	b.WriteString(eol)

	for _, key := range slices.Sorted(maps.Keys(entry)) {
		value, err := tomlValue(entry[key])
		if err != nil {
			return nil, fmt.Errorf("server %s: key %s: %w", name, key, err)
		}

		b.WriteString(tomlKey(key))
		b.WriteString(" = ")
		b.WriteString(value)
		b.WriteString(eol)
	}

	return []byte(b.String()), nil
}

// joinBlocks renders the named blocks in order, one blank line apart.
func joinBlocks(renders map[string][]byte, names []string, eol string) []byte {
	var out []byte

	for i, name := range names {
		if i > 0 {
			out = append(out, eol...)
		}

		out = append(out, renders[name]...)
	}

	return out
}

// appendTOMLBlocks appends rendered tables to the file: the last existing line
// is terminated and exactly one blank line separates it from the new table, so
// a grown config reads like a hand-written one. An empty file gets no leading
// blank line.
func appendTOMLBlocks(data, blocks []byte, eol string) []byte {
	if len(blocks) == 0 {
		return data
	}

	out := append([]byte{}, data...)

	if len(out) == 0 {
		return append(out, blocks...)
	}

	if out[len(out)-1] != '\n' {
		out = append(out, eol...)
	}

	if !endsWithBlankLine(out, eol) {
		out = append(out, eol...)
	}

	return append(out, blocks...)
}

// endsWithBlankLine reports whether the buffer ends with an empty line, so the
// separator is not doubled.
func endsWithBlankLine(data []byte, eol string) bool {
	for _, blank := range []string{"\n\n", "\n\r\n", "\r\n\n", eol + eol} {
		if bytes.HasSuffix(data, []byte(blank)) {
			return true
		}
	}

	return false
}

func tomlEOL(data []byte) string {
	if bytes.Contains(data, []byte("\r\n")) {
		return "\r\n"
	}

	return "\n"
}

func tomlKey(key string) string {
	if isBareTOMLKey(key) {
		return key
	}

	return tomlBasicString(key)
}

func isBareTOMLKey(key string) bool {
	if key == "" {
		return false
	}

	for _, r := range key {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}

		return false
	}

	return true
}

func tomlBasicString(value string) string {
	var b strings.Builder

	b.WriteByte('"')

	for _, r := range value {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}

	b.WriteByte('"')

	return b.String()
}

func tomlValue(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return tomlBasicString(typed), nil
	case bool:
		return strconv.FormatBool(typed), nil
	case int:
		return strconv.Itoa(typed), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case uint64:
		return strconv.FormatUint(typed, 10), nil
	case float64:
		return tomlFloat(typed), nil
	case time.Time, toml.LocalDate, toml.LocalTime, toml.LocalDateTime:
		return tomlTemporal(typed), nil
	case []string:
		return tomlArray(typed), nil
	case []any:
		return tomlArrayAny(typed)
	case map[string]string:
		return tomlInlineStringTable(typed), nil
	case map[string]any:
		return tomlInlineTable(typed)
	default:
		return "", fmt.Errorf("unsupported toml value of type %T", value)
	}
}

func tomlTemporal(value any) string {
	switch typed := value.(type) {
	case time.Time:
		return typed.Format(time.RFC3339)
	case toml.LocalDate:
		return typed.String()
	case toml.LocalTime:
		return typed.String()
	case toml.LocalDateTime:
		return typed.String()
	default:
		return ""
	}
}

func tomlFloat(value float64) string {
	out := strconv.FormatFloat(value, 'g', -1, 64)
	if !strings.ContainsAny(out, ".eEnN") {
		out += ".0"
	}

	return out
}

func tomlArray(items []string) string {
	values := make([]string, 0, len(items))

	for _, item := range items {
		values = append(values, tomlBasicString(item))
	}

	return "[" + strings.Join(values, ", ") + "]"
}

func tomlArrayAny(items []any) (string, error) {
	values := make([]string, 0, len(items))

	for _, item := range items {
		value, err := tomlValue(item)
		if err != nil {
			return "", err
		}

		values = append(values, value)
	}

	return "[" + strings.Join(values, ", ") + "]", nil
}

func tomlInlineStringTable(entry map[string]string) string {
	if len(entry) == 0 {
		return "{}"
	}

	values := make([]string, 0, len(entry))

	for _, key := range slices.Sorted(maps.Keys(entry)) {
		values = append(values, tomlKey(key)+" = "+tomlBasicString(entry[key]))
	}

	return "{ " + strings.Join(values, ", ") + " }"
}

func tomlInlineTable(entry map[string]any) (string, error) {
	if len(entry) == 0 {
		return "{}", nil
	}

	values := make([]string, 0, len(entry))

	for _, key := range slices.Sorted(maps.Keys(entry)) {
		value, err := tomlValue(entry[key])
		if err != nil {
			return "", err
		}

		values = append(values, tomlKey(key)+" = "+value)
	}

	return "{ " + strings.Join(values, ", ") + " }", nil
}
