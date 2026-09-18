// Package inbox reads append-only agent inbox files into memory notes.
package inbox

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Line is one consumable inbox line with its content hash.
type Line struct {
	Text string
	Hash string
}

const (
	nameLimit = 60
	hookLimit = 120
)

var commentLine = regexp.MustCompile(`^<!--.*-->$`)

// Read returns the consumable lines of an inbox file. A missing file yields no
// lines; empty lines and pure HTML comments are skipped.
func Read(path string) ([]Line, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: inbox paths are resolved by the agent definitions
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read inbox %s: %w", path, err)
	}

	var lines []Line

	for raw := range strings.Lines(string(data)) {
		text := strings.TrimSpace(strings.ReplaceAll(raw, "\r", ""))
		if text == "" || commentLine.MatchString(text) || strings.HasPrefix(text, "#") {
			continue
		}

		sum := sha256.Sum256([]byte(text))

		lines = append(lines, Line{Text: text, Hash: hex.EncodeToString(sum[:])[:8]})
	}

	return lines, nil
}

// NoteName returns the canon note file name of a line.
func NoteName(line Line) string {
	return "inbox-" + line.Hash + ".md"
}

// Note renders the memory note of one inbox line for the given project slug.
// The returned key is the canon item key "<slug>/inbox-<hash8>.md".
func Note(slug, agent string, line Line) (string, []byte) {
	var out strings.Builder

	fmt.Fprintf(&out, "---\nname: %s\ndescription: %s\n", strconv.Quote(truncate(line.Text, nameLimit)), strconv.Quote(truncate(line.Text, hookLimit)))
	fmt.Fprintf(&out, "metadata:\n  type: user\n  origin: inbox\n  agent: %s\n  hash: %s\n", agent, line.Hash)
	fmt.Fprintf(&out, "---\n%s\n\n<!-- transcribed: %s -->\n", line.Text, agent)

	return slug + "/" + NoteName(line), []byte(out.String())
}

func truncate(text string, limit int) string {
	runes := []rune(sanitize(text))
	if len(runes) <= limit {
		return string(runes)
	}

	return string(runes[:limit])
}

func sanitize(text string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(text)
}
