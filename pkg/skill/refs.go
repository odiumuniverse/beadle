package skill

import (
	"regexp"
	"slices"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Reference is one skill a skill body points at.
type Reference struct {
	// Name is the referenced skill name, normalized (NFC, lowercase).
	Name string
	// Plugin is the plugin namespace of a `/plugin:skill` or
	// `Skill(plugin:skill)` reference, if any.
	Plugin string
	// Quoted reports that the phrase quoted the name.
	Quoted bool
	// Explicit reports that the phrase names the skill unambiguously
	// (`Skill(X)`, `skill tool with "X"`, `/plugin:skill`, `skill "X"`, a
	// quoted call). Only the unquoted `call/load/use/invoke skill X` form is
	// prose-prone, so the caller validates just that one.
	Explicit bool
}

// skillRefPatterns lists the documented phrases that point at another skill.
// The extractor never guesses from bare words: only these forms match.
var skillRefPatterns = []*regexp.Regexp{
	// Skill tool with "name"
	regexp.MustCompile(`(?i)skill tool with\s+["'“”]([a-z0-9][a-z0-9_.:-]*)["'“”]`),
	// Skill(name) and Skill("name")
	regexp.MustCompile(`(?i)\bSkill\(\s*["'“”]?([a-z0-9][a-z0-9_.:-]*)["'“”]?\s*\)`),
	// call the skill "name" / load skill "name" / use skill "name" /
	// invoke skill "name"
	regexp.MustCompile(`(?i)\b(?:call|load|use|invoke)\s+(?:the\s+)?skill\s+["'“”]([a-z0-9][a-z0-9_.:-]*)["'“”]`),
	// call the skill name. / load skill name, ... / call skill name at the
	// end of a line
	regexp.MustCompile(`(?im)\b(?:call|load|use|invoke)\s+(?:the\s+)?skill\s+([a-z0-9][a-z0-9_-]*)\s*(?:[.,;:!?)]|$)`),
	// /plugin:skill. The leading slash must start a word: `//go:build`,
	// `//nolint:errcheck` and URLs are not references.
	regexp.MustCompile(`(?i)(?:^|[^\w/:-])/([a-z0-9][a-z0-9_-]*):([a-z0-9][a-z0-9_-]*)`),
	// skill "name"
	regexp.MustCompile(`(?i)\bskill\s+["'“”]([a-z0-9][a-z0-9_.:-]*)["'“”]`),
}

// isRefLetter reports whether a rune makes a reference name more than a
// number (a port or a timestamp is not a skill).
func isRefLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || r == '-' || r == '_'
}

// refNamePattern accepts the skill names a reference may carry: the canonical
// slug shape plus the underscores plugin skills sometimes use.
var refNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// explicitPatterns marks the patterns whose form names a skill
// unambiguously; pattern 3 (unquoted call/load/use/invoke) is prose-prone.
var explicitPatterns = map[int]bool{0: true, 1: true, 2: true, 3: false, 4: true, 5: true}

// References extracts the skill references of a skill body: the documented
// phrases only, normalized (NFC, lowercase) and deduplicated by
// (plugin, name).
func References(body []byte) []Reference {
	text := string(body)

	var refs []Reference

	for i, pattern := range skillRefPatterns {
		for _, match := range pattern.FindAllStringSubmatch(text, -1) {
			name, plugin := splitReference(match, i)
			if name == "" {
				continue
			}

			refs = append(refs, Reference{
				Name:     name,
				Plugin:   plugin,
				Quoted:   strings.ContainsAny(match[0], "\"'“”"),
				Explicit: explicitPatterns[i],
			})
		}
	}

	// One entry per (plugin, name): an explicit phrase wins over a
	// prose-prone one, otherwise the first phrase that names it wins.
	index := map[string]int{}
	out := make([]Reference, 0, len(refs))

	for _, ref := range refs {
		key := ref.Plugin + "\x00" + ref.Name

		if at, taken := index[key]; taken {
			if ref.Explicit && !out[at].Explicit {
				out[at] = ref
			}

			continue
		}

		index[key] = len(out)

		out = append(out, ref)
	}

	slices.SortFunc(out, func(a, b Reference) int {
		if order := strings.Compare(a.Plugin, b.Plugin); order != 0 {
			return order
		}

		return strings.Compare(a.Name, b.Name)
	})

	return out
}

// splitReference returns the normalized skill name and the plugin namespace
// of one match. A `/plugin:skill` match carries the skill in group two.
func splitReference(match []string, pattern int) (string, string) {
	name, plugin := match[1], ""

	if pattern == 4 && len(match) > 2 {
		plugin, name = match[1], match[2]
	}

	name = norm.NFC.String(strings.ToLower(name))

	if before, after, ok := strings.Cut(name, ":"); ok {
		plugin, name = before, after
	}

	if !refNamePattern.MatchString(name) || !strings.ContainsFunc(name, isRefLetter) {
		return "", ""
	}

	return name, plugin
}
