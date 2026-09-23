package agent

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
)

// tomlKeySpan is one top-level assignment in a TOML document.
type tomlKeySpan struct {
	key        string
	lineStart  int // offset of the first byte of the assignment line
	valueStart int
	valueEnd   int
	end        int // offset just after the value's last line
}

// tomlSpanScan records the top-level assignments of a TOML document and the
// offset where the first table header starts.
type tomlSpanScan struct {
	spans      []tomlKeySpan
	tableStart int
}

// tomlBOM is the UTF-8 byte order mark some editors prepend: toml.LoadBytes
// accepts it, so the span scanner must skip it too.
var tomlBOM = []byte{0xEF, 0xBB, 0xBF}

// tomlStart returns the offset of the first content byte, skipping a UTF-8
// BOM.
func tomlStart(data []byte) int {
	if bytes.HasPrefix(data, tomlBOM) {
		return len(tomlBOM)
	}

	return 0
}

// scanTOMLSpans walks a TOML document once and records the value span of
// every top-level key. Scanning stops at the first table header: keys after
// it belong to tables. Multi-line strings, arrays, inline tables and
// comments are respected.
func scanTOMLSpans(data []byte) tomlSpanScan {
	scan := tomlSpanScan{tableStart: len(data)}

	for i := tomlStart(data); i < len(data); {
		lineStart := i

		lineEnd := bytes.IndexByte(data[i:], '\n')
		if lineEnd < 0 {
			lineEnd = len(data)
		} else {
			lineEnd = i + lineEnd
		}

		next := lineEnd
		if next < len(data) {
			next++
		}

		line := data[lineStart:lineEnd]
		trimmed := bytes.TrimLeft(line, " \t")

		switch {
		case len(trimmed) == 0 || trimmed[0] == '#':
			i = next

			continue
		case trimmed[0] == '[':
			scan.tableStart = lineStart

			return scan
		}

		key, afterKey, ok := scanTOMLKey(trimmed)
		if !ok {
			i = next

			continue
		}

		eq := bytes.IndexByte(afterKey, '=')
		if eq < 0 {
			i = next

			continue
		}

		valueStart := lineEnd - len(afterKey) + eq + 1
		valueStart += leadingSpace(data[valueStart:])

		valueEnd, end := scanTOMLValue(data, valueStart)

		scan.spans = append(scan.spans, tomlKeySpan{
			key:        key,
			lineStart:  lineStart,
			valueStart: valueStart,
			valueEnd:   valueEnd,
			end:        end,
		})

		i = end
		if i <= lineStart {
			i = next
		}
	}

	return scan
}

// scanTOMLKey reads a bare or quoted key at the start of a stripped line.
func scanTOMLKey(line []byte) (string, []byte, bool) {
	if len(line) == 0 {
		return "", nil, false
	}

	switch line[0] {
	case '"', '\'':
		quote := line[0]

		end := bytes.IndexByte(line[1:], quote)
		if end < 0 {
			return "", nil, false
		}

		return unquoteTOML(string(line[1 : 1+end])), line[2+end:], true
	default:
		end := bytes.IndexAny(line, " \t=")
		if end <= 0 {
			return "", nil, false
		}

		return string(line[:end]), line[end:], true
	}
}

// scanTOMLValue returns the end of the value that starts at offset and the
// offset just after the value's last line.
func scanTOMLValue(data []byte, offset int) (valueEnd, end int) {
	if offset >= len(data) {
		return len(data), len(data)
	}

	switch {
	case bytes.HasPrefix(data[offset:], []byte(`"""`)):
		return scanTOMLMultiline(data, offset, []byte(`"""`), true)
	case bytes.HasPrefix(data[offset:], []byte(`'''`)):
		return scanTOMLMultiline(data, offset, []byte(`'''`), false)
	case data[offset] == '[' || data[offset] == '{':
		return scanTOMLNested(data, offset)
	default:
		return scanTOMLScalar(data, offset)
	}
}

// scanTOMLMultiline finds the closing delimiter of a multi-line string.
func scanTOMLMultiline(data []byte, offset int, delimiter []byte, escaped bool) (valueEnd, end int) {
	pos := offset + len(delimiter)

	for pos < len(data) {
		if escaped && data[pos] == '\\' && pos+1 < len(data) {
			pos += 2

			continue
		}

		if bytes.HasPrefix(data[pos:], delimiter) {
			pos += len(delimiter)

			return pos, afterLine(data, pos)
		}

		pos++
	}

	return len(data), len(data)
}

// scanTOMLNested finds the end of an array or inline table, respecting
// nested brackets, strings and comments.
func scanTOMLNested(data []byte, offset int) (valueEnd, end int) {
	depth := 0
	pos := offset

	for pos < len(data) {
		switch data[pos] {
		case '"', '\'':
			pos = skipTOMLString(data, pos)

			continue
		case '#':
			if line := bytes.IndexByte(data[pos:], '\n'); line >= 0 {
				pos += line

				continue
			}

			return len(data), len(data)
		case '[', '{':
			depth++
		case ']', '}':
			depth--

			if depth == 0 {
				return pos + 1, afterLine(data, pos+1)
			}
		}

		pos++
	}

	return len(data), len(data)
}

// scanTOMLScalar finds the end of a single-line value, dropping trailing
// whitespace and comments.
func scanTOMLScalar(data []byte, offset int) (valueEnd, end int) {
	pos := offset

	if pos < len(data) && (data[pos] == '"' || data[pos] == '\'') {
		pos = skipTOMLString(data, pos)
	}

	for pos < len(data) && data[pos] != '\n' {
		if tomlCommentStart(data, pos, offset) {
			break
		}

		pos++
	}

	valueEnd = trimScalarEnd(data, offset, pos)

	return valueEnd, afterLine(data, valueEnd)
}

// tomlCommentStart reports whether the byte at pos starts a trailing comment
// of the value that starts at offset.
func tomlCommentStart(data []byte, pos, offset int) bool {
	if data[pos] != '#' {
		return false
	}

	return pos == offset || data[pos-1] == ' ' || data[pos-1] == '\t'
}

// trimScalarEnd drops trailing spaces, tabs and carriage returns.
func trimScalarEnd(data []byte, offset, pos int) int {
	for pos > offset && (data[pos-1] == ' ' || data[pos-1] == '\t' || data[pos-1] == '\r') {
		pos--
	}

	return pos
}

// skipTOMLString returns the offset just after a TOML string that starts at
// pos.
func skipTOMLString(data []byte, pos int) int {
	quote := data[pos]

	if bytes.HasPrefix(data[pos:], []byte(`"""`)) || bytes.HasPrefix(data[pos:], []byte(`'''`)) {
		delimiter := []byte{quote, quote, quote}

		end, _ := scanTOMLMultiline(data, pos, delimiter, quote == '"')

		return end
	}

	for i := pos + 1; i < len(data); i++ {
		if quote == '"' && data[i] == '\\' {
			i++

			continue
		}

		if data[i] == quote {
			return i + 1
		}

		if data[i] == '\n' {
			return i
		}
	}

	return len(data)
}

// afterLine returns the offset just after the line that contains offset.
func afterLine(data []byte, offset int) int {
	line := bytes.IndexByte(data[offset:], '\n')
	if line < 0 {
		return len(data)
	}

	return offset + line + 1
}

// leadingSpace returns the number of leading spaces and tabs in data.
func leadingSpace(data []byte) int {
	pos := 0

	for pos < len(data) && (data[pos] == ' ' || data[pos] == '\t') {
		pos++
	}

	return pos
}

// unquoteTOML strips the escapes of a quoted TOML key.
func unquoteTOML(key string) string {
	key = strings.ReplaceAll(key, `\"`, `"`)
	key = strings.ReplaceAll(key, `\'`, `'`)

	return strings.ReplaceAll(key, `\\`, `\`)
}

// editTOMLKeys rewrites the top-level keys of a TOML document: values maps a
// managed key to its rendered TOML value, an empty value removes the key,
// and a managed key absent from values is removed too unless it is listed in
// keep. Unknown keys, the key spelling, comments and tables are preserved.
func editTOMLKeys(data []byte, managed, keep []string, values map[string]string) ([]byte, error) {
	scan := scanTOMLSpans(data)

	cuts, present := editTOMLSpans(scan, managed, keep, values)

	offset, block := tomlInsertBlock(data, scan, managed, present, values)

	if len(block) > 0 {
		if offset > 0 && data[offset-1] != '\n' {
			block = append([]byte{'\n'}, block...)
		}

		cuts = append(cuts, tomlCut{start: offset, end: offset, data: block})
	}

	if len(cuts) == 0 {
		return data, nil
	}

	return applyTOMLCuts(data, cuts), nil
}

// editTOMLSpans rewrites the values of the managed keys that are present and
// removes the ones the caller does not want; it reports the keys it wrote.
func editTOMLSpans(scan tomlSpanScan, managed, keep []string, values map[string]string) ([]tomlCut, map[string]bool) {
	var cuts []tomlCut

	present := map[string]bool{}

	for _, span := range scan.spans {
		if !slices.Contains(managed, span.key) {
			continue
		}

		value, want := values[span.key]
		if !want || value == "" {
			if slices.Contains(keep, span.key) {
				continue
			}

			cuts = append(cuts, tomlCut{start: span.lineStart, end: span.end})

			continue
		}

		present[span.key] = true

		cuts = append(cuts, tomlCut{start: span.valueStart, end: span.valueEnd, data: []byte(value)})
	}

	return cuts, present
}

// tomlInsertBlock renders the managed keys the file does not hold yet and
// returns the offset they belong to.
func tomlInsertBlock(data []byte, scan tomlSpanScan, managed []string, present map[string]bool, values map[string]string) (int, []byte) {
	var offset int

	if len(scan.spans) > 0 {
		offset = scan.spans[len(scan.spans)-1].end
	} else {
		offset = topLevelStart(data, scan.tableStart)
	}

	var block bytes.Buffer

	for _, key := range managed {
		if present[key] {
			continue
		}

		value, want := values[key]
		if !want || value == "" {
			continue
		}

		block.WriteString(key)
		block.WriteString(" = ")
		block.WriteString(value)
		block.WriteByte('\n')
	}

	return offset, block.Bytes()
}

// topLevelStart returns the offset of the first meaningful line before the
// first table header, so inserted keys land after leading comments.
func topLevelStart(data []byte, tableStart int) int {
	pos := tomlStart(data)

	for pos < tableStart {
		lineEnd := bytes.IndexByte(data[pos:], '\n')
		if lineEnd < 0 {
			lineEnd = len(data)
		} else {
			lineEnd += pos
		}

		trimmed := bytes.TrimSpace(data[pos:lineEnd])
		if len(trimmed) > 0 && trimmed[0] != '#' {
			return pos
		}

		pos = lineEnd
		if pos < len(data) {
			pos++
		}
	}

	return tableStart
}

// tomlMultilineValue renders a canonical body as a TOML multi-line basic
// string. The value always ends with exactly one newline.
func tomlMultilineValue(body string) string {
	trimmed := strings.TrimRight(body, "\r\n")

	var b strings.Builder

	b.WriteString(`"""` + "\n")
	b.WriteString(tomlBasicBody(trimmed))

	if trimmed != "" {
		b.WriteByte('\n')
	}

	b.WriteString(`"""`)

	return b.String()
}

// tomlBasicBody escapes text for a multi-line basic string; newlines and
// tabs stay literal there.
func tomlBasicBody(value string) string {
	var b strings.Builder

	for _, r := range value {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteByte('\n')
		case '\t':
			b.WriteByte('\t')
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}

	return b.String()
}
