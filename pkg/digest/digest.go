// Package digest renders, splices and verifies the agent-sync memory digest
// fence embedded into project rules files.
package digest

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	// BeginPrefix starts the digest fence line; the full line carries the receipt.
	BeginPrefix = "<!-- agent-sync:memory:begin"
	// EndMarker closes the digest fence.
	EndMarker = "<!-- agent-sync:memory:end -->"
	// DefaultBudget caps the digest content region in bytes.
	DefaultBudget = 8192
	// Version is the current receipt format version.
	Version = "v1"
)

const (
	hookLimit    = 120
	markerWord   = "agent-sync"
	markerSafe   = "agent&#45;sync"
	projectsMark = "/.claude/projects/"
	reasonBudget = "budget"
	hashHexLen   = 64
	redactedText = "[redacted]"
)

var refRedactRE = regexp.MustCompile(`\{(?:secret|env):[A-Za-z_][A-Za-z0-9_]*\}`)

// ErrFence reports a malformed or ambiguous digest fence.
var ErrFence = errors.New("malformed memory fence")

// Receipt is the parsed header of a digest fence.
type Receipt struct {
	Version string
	Inputs  string
	Render  string
	Notes   int
	Omitted int
}

type entry struct {
	name     string
	path     string
	hook     string
	kind     string
	modified string
	line     string
}

// Render builds the digest fence for the notes of one project. The result is a
// pure function of the inputs: no clock, no file metadata, no map order.
func Render(dir string, notes map[string][]byte, budget int) ([]byte, Receipt) {
	entries := make([]entry, 0, len(notes))

	for key, data := range notes {
		entries = append(entries, newEntry(dir, key, data))
	}

	sortEntries(entries)

	inputs := inputsHash(notes)
	included, dropped := splitByBudget(entries, budget)

	receipt := Receipt{Version: Version, Inputs: inputs, Notes: len(entries), Omitted: len(dropped)}
	content := contentLines(included, dropped, budget)
	receipt.Render = renderHash(receipt, content)

	return assemble(receipt, content), receipt
}

// Strip splits a document into the body without the digest fence and the raw
// fence bytes. It never repairs an ambiguous document: multiple or nested
// markers yield ErrFence.
func Strip(doc []byte) (body, fence []byte, found bool, err error) {
	start, end, fence, found, err := stripPos(doc)
	if err != nil {
		return nil, nil, false, err
	}

	if !found {
		return doc, nil, false, nil
	}

	body = make([]byte, 0, len(doc)-(end-start))
	body = append(body, doc[:start]...)
	body = append(body, doc[end:]...)

	return body, fence, true, nil
}

// Splice replaces the fence of doc with block, inserts block at the start when
// no fence exists, or removes the fence when block is nil. Bytes outside the
// fence are preserved verbatim; block is expected to be canonical (ending with
// a newline), which is what Render returns.
func Splice(doc, block []byte) ([]byte, error) {
	start, end, _, found, err := stripPos(doc)
	if err != nil {
		return nil, err
	}

	if !found {
		if len(block) == 0 {
			return doc, nil
		}

		out := make([]byte, 0, len(block)+len(doc))
		out = append(out, block...)
		out = append(out, doc...)

		return out, nil
	}

	out := make([]byte, 0, len(doc)-(end-start)+len(block))
	out = append(out, doc[:start]...)
	out = append(out, block...)
	out = append(out, doc[end:]...)

	return out, nil
}

// Neutralize makes note-derived text unable to form a raw fence marker: every
// occurrence of the marker word becomes an inert entity.
func Neutralize(text []byte) []byte {
	return bytes.ReplaceAll(text, []byte(markerWord), []byte(markerSafe))
}

// Redact replaces secret and environment references with a fixed placeholder.
func Redact(text []byte) []byte {
	return refRedactRE.ReplaceAll(text, []byte(redactedText))
}

// Verify parses a fence receipt and recomputes the render hash of the block
// body, reporting whether the bytes are intact.
func Verify(fence []byte) (Receipt, bool) {
	line, rest, ok := bytes.Cut(fence, []byte("\n"))
	if !ok {
		return Receipt{}, false
	}

	receipt, ok := parseReceipt(line)
	if !ok {
		return Receipt{}, false
	}

	content, _, ok := bytes.Cut(rest, []byte(EndMarker))
	if !ok {
		return Receipt{}, false
	}

	if receipt.Render != renderHash(receipt, content) {
		return Receipt{}, false
	}

	return receipt, true
}

func stripPos(doc []byte) (int, int, []byte, bool, error) {
	begin := bytes.Index(doc, []byte(BeginPrefix))
	end := bytes.Index(doc, []byte(EndMarker))

	switch {
	case begin < 0 && end < 0:
		return 0, 0, nil, false, nil
	case begin < 0, end < 0, end < begin:
		return 0, 0, nil, false, ErrFence
	}

	if bytes.Contains(doc[begin+len(BeginPrefix):], []byte(BeginPrefix)) ||
		bytes.Contains(doc[end+len(EndMarker):], []byte(EndMarker)) {
		return 0, 0, nil, false, ErrFence
	}

	fenceEnd := end + len(EndMarker)

	switch {
	case bytes.HasPrefix(doc[fenceEnd:], []byte("\r\n")):
		fenceEnd += 2
	case bytes.HasPrefix(doc[fenceEnd:], []byte("\n")):
		fenceEnd++
	}

	return begin, fenceEnd, doc[begin:fenceEnd], true, nil
}

func newEntry(dir, key string, data []byte) entry {
	name := sanitizeName(noteName(key))
	meta, body := parseFrontmatter(data)

	entryName := meta.name
	if entryName == "" {
		entryName = strings.TrimSuffix(name, ".md")
	}

	hook := meta.description
	if hook == "" {
		hook = firstLine(body)
	}

	item := entry{
		name:     entryName,
		path:     tildeDir(dir) + "/" + name,
		hook:     truncate(hook, hookLimit),
		kind:     meta.kind,
		modified: meta.modified,
	}

	item.line = renderLine(item)

	return item
}

func renderLine(item entry) string {
	line := "- [" + sanitize(item.name) + "](" + sanitize(item.path) + ")"

	if item.hook != "" {
		line += " — " + sanitize(item.hook)
	}

	return line + suffix(item)
}

func suffix(item entry) string {
	parts := make([]string, 0, 2)

	if item.kind != "" {
		parts = append(parts, item.kind)
	}

	if item.modified != "" {
		parts = append(parts, item.modified)
	}

	if len(parts) == 0 {
		return ""
	}

	return " [" + sanitize(strings.Join(parts, ", ")) + "]"
}

func sortEntries(entries []entry) {
	rank := func(item entry) int {
		if item.modified == "" {
			return 1
		}

		return 0
	}

	slices.SortFunc(entries, func(a, b entry) int {
		return cmp.Or(
			cmp.Compare(rank(a), rank(b)),
			cmp.Compare(b.modified, a.modified),
			cmp.Compare(a.name, b.name),
			cmp.Compare(a.path, b.path),
		)
	})
}

func splitByBudget(entries []entry, budget int) (included, dropped []entry) {
	included = slices.Clone(entries)

	for len(included) > 0 && len(contentLines(included, dropped, budget)) > budget {
		dropped = append([]entry{included[len(included)-1]}, dropped...)
		included = included[:len(included)-1]
	}

	return included, dropped
}

func contentLines(included, dropped []entry, budget int) []byte {
	var out bytes.Buffer

	for _, item := range included {
		out.WriteString(item.line)
		out.WriteByte('\n')
	}

	for _, line := range manifestLines(dropped, budget) {
		out.WriteString(line)
		out.WriteByte('\n')
	}

	return out.Bytes()
}

func manifestLines(dropped []entry, budget int) []string {
	limit := budget / 4
	lines := make([]string, 0, len(dropped))
	used := 0

	for _, item := range dropped {
		line := "- " + sanitize(item.path) + " — omitted: " + reasonBudget
		if used+len(line)+1 > limit {
			break
		}

		lines = append(lines, line)
		used += len(line) + 1
	}

	if fits := len(lines); fits < len(dropped) {
		summary := fmt.Sprintf("- … and %d more", len(dropped)-fits)
		if used+len(summary)+1 <= limit {
			lines = append(lines, summary)
		}
	}

	return lines
}

func assemble(receipt Receipt, content []byte) []byte {
	var out bytes.Buffer

	fmt.Fprintf(&out, "%s %s inputs=sha256:%s render=sha256:%s notes=%d omitted=%d -->\n",
		BeginPrefix, receipt.Version, receipt.Inputs, receipt.Render, receipt.Notes, receipt.Omitted)
	out.Write(content)
	out.WriteString(EndMarker)
	out.WriteByte('\n')

	return out.Bytes()
}

func parseReceipt(line []byte) (Receipt, bool) {
	text := strings.TrimSuffix(string(line), " -->")
	if !strings.HasPrefix(text, BeginPrefix) {
		return Receipt{}, false
	}

	fields := strings.Fields(text)
	if len(fields) != 7 {
		return Receipt{}, false
	}

	receipt := Receipt{Version: fields[2]}
	if receipt.Version != Version {
		return Receipt{}, false
	}

	var ok bool

	if receipt.Inputs, ok = cutHash(fields[3], "inputs="); !ok {
		return Receipt{}, false
	}

	if receipt.Render, ok = cutHash(fields[4], "render="); !ok {
		return Receipt{}, false
	}

	notes, ok := strings.CutPrefix(fields[5], "notes=")
	if !ok {
		return Receipt{}, false
	}

	omitted, ok := strings.CutPrefix(fields[6], "omitted=")
	if !ok {
		return Receipt{}, false
	}

	if receipt.Notes, ok = parseCount(notes); !ok {
		return Receipt{}, false
	}

	if receipt.Omitted, ok = parseCount(omitted); !ok {
		return Receipt{}, false
	}

	return receipt, true
}

func cutHash(field, prefix string) (string, bool) {
	value, ok := strings.CutPrefix(field, prefix+"sha256:")
	if !ok {
		return "", false
	}

	if _, err := hex.DecodeString(value); err != nil || len(value) != hashHexLen {
		return "", false
	}

	return value, true
}

func parseCount(field string) (int, bool) {
	count, err := strconv.Atoi(field)
	if err != nil || count < 0 {
		return 0, false
	}

	return count, true
}

func renderHash(receipt Receipt, content []byte) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\ninputs=%s\nnotes=%d\nomitted=%d\n", receipt.Version, receipt.Inputs, receipt.Notes, receipt.Omitted)
	hash.Write(content)

	return hex.EncodeToString(hash.Sum(nil))
}

func inputsHash(notes map[string][]byte) string {
	pairs := make([]string, 0, len(notes))

	for key, data := range notes {
		sum := sha256.Sum256(data)
		pairs = append(pairs, key+"|"+hex.EncodeToString(sum[:]))
	}

	slices.Sort(pairs)

	sum := sha256.Sum256([]byte(strings.Join(pairs, "\n")))

	return hex.EncodeToString(sum[:])
}

type frontmatter struct {
	name        string
	description string
	kind        string
	modified    string
}

func parseFrontmatter(data []byte) (frontmatter, []byte) {
	line, rest, ok := cutLine(data)
	if !ok || strings.TrimRight(string(line), "\r") != "---" {
		return frontmatter{}, data
	}

	meta := frontmatter{}
	inMetadata := false

	for {
		line, next, ok := cutLine(rest)
		if !ok {
			return frontmatter{}, data
		}

		text := strings.TrimRight(string(line), "\r")
		rest = next

		if text == "---" {
			return meta, rest
		}

		parseFrontmatterLine(&meta, text, &inMetadata)
	}
}

func parseFrontmatterLine(meta *frontmatter, line string, inMetadata *bool) {
	if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
		if !*inMetadata {
			return
		}

		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			return
		}

		switch strings.TrimSpace(key) {
		case "modified":
			meta.modified = unquote(strings.TrimSpace(value))
		case "type":
			meta.kind = unquote(strings.TrimSpace(value))
		}

		return
	}

	key, value, ok := strings.Cut(line, ":")
	if !ok {
		*inMetadata = false

		return
	}

	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)

	if key == "metadata" && value == "" {
		*inMetadata = true

		return
	}

	*inMetadata = false

	switch key {
	case "name":
		meta.name = unquote(value)
	case "description":
		meta.description = unquote(value)
	}
}

func cutLine(data []byte) ([]byte, []byte, bool) {
	line, rest, ok := bytes.Cut(data, []byte("\n"))

	return line, rest, ok
}

func unquote(value string) string {
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		return value[1 : len(value)-1]
	}

	return value
}

func firstLine(body []byte) string {
	for line := range strings.Lines(string(body)) {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}

	return ""
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}

	return string(runes[:limit])
}

func noteName(key string) string {
	return key[strings.LastIndex(key, "/")+1:]
}

func sanitizeName(name string) string {
	if !strings.ContainsFunc(name, isControl) {
		return name
	}

	var out strings.Builder

	for _, r := range name {
		if isControl(r) {
			fmt.Fprintf(&out, "\\x%02x", r)

			continue
		}

		out.WriteRune(r)
	}

	return out.String()
}

func isControl(r rune) bool {
	return r < ' ' || r == 0x7f
}

func tildeDir(dir string) string {
	clean := filepath.Clean(dir)

	if idx := strings.Index(clean, projectsMark); idx >= 0 {
		return "~" + clean[idx:]
	}

	return clean
}

func sanitize(text string) string {
	return string(Redact(Neutralize([]byte(text))))
}
