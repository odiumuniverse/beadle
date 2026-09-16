package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/tailscale/hujson"
)

type Reference struct {
	Pointer string
	Path    string
}

const tildePluginsRoot = "~/.claude/plugins"

var pointerEscaper = strings.NewReplacer("~", "~0", "/", "~1")

func References(data []byte, home string) ([]Reference, error) {
	if home == "" {
		return nil, errors.New("empty home directory")
	}

	root := filepath.Join(home, claudeDir, pluginsDir)

	value, err := hujson.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}

	var refs []Reference

	collectRefs(&value, "", root, home, &refs)

	return refs, nil
}

func collectRefs(value *hujson.Value, pointer, root, home string, refs *[]Reference) {
	switch typed := value.Value.(type) {
	case hujson.Literal:
		if s, ok := literalString(typed); ok {
			*refs = append(*refs, extractRefs(s, pointer, root, home)...)
		}
	case *hujson.Object:
		for i := range typed.Members {
			member := &typed.Members[i]

			name, ok := literalString(member.Name.Value)
			if !ok {
				continue
			}

			collectRefs(&member.Value, pointer+"/"+pointerEscaper.Replace(name), root, home, refs)
		}
	case *hujson.Array:
		for i := range typed.Elements {
			collectRefs(&typed.Elements[i], pointer+"/"+strconv.Itoa(i), root, home, refs)
		}
	}
}

func literalString(value hujson.ValueTrimmed) (string, bool) {
	literal, ok := value.(hujson.Literal)
	if !ok {
		return "", false
	}

	var s string

	if err := json.Unmarshal(literal, &s); err != nil {
		return "", false
	}

	return s, true
}

func extractRefs(s, pointer, root, home string) []Reference {
	var refs []Reference

	seen := map[string]bool{}

	for offset := 0; offset < len(s); {
		start, length := findRoot(s, offset, root)
		if start < 0 {
			break
		}

		end := tokenEnd(s, start+length)
		token := s[start:end]

		offset = end

		if strings.Contains(token, "${") {
			continue
		}

		path, ok := tokenPath(token, root, home)
		if !ok {
			continue
		}

		key := pointer + "\x00" + path
		if seen[key] {
			continue
		}

		seen[key] = true

		refs = append(refs, Reference{Pointer: pointer, Path: path})
	}

	return refs
}

func findRoot(s string, offset int, root string) (int, int) {
	best, length := -1, 0

	for _, prefix := range []string{root, tildePluginsRoot} {
		idx := strings.Index(s[offset:], prefix)
		if idx < 0 {
			continue
		}

		if at := offset + idx; best < 0 || at < best {
			best, length = at, len(prefix)
		}
	}

	return best, length
}

func tokenEnd(s string, start int) int {
	for i, r := range s[start:] {
		if refStop(r) {
			return start + i
		}
	}

	return len(s)
}

func refStop(r rune) bool {
	switch r {
	case '"', '\'', '`', ',', ';', '|', '>', ')', '}':
		return true
	default:
		return unicode.IsSpace(r)
	}
}

func tokenPath(token, root, home string) (string, bool) {
	switch {
	case prefixBoundary(token, root):
		return token, true
	case prefixBoundary(token, tildePluginsRoot):
		return filepath.Join(home, token[2:]), true
	default:
		return "", false
	}
}

func prefixBoundary(token, prefix string) bool {
	rest, ok := strings.CutPrefix(token, prefix)
	if !ok {
		return false
	}

	return rest == "" || strings.HasPrefix(rest, "/")
}
