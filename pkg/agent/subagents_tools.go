package agent

import (
	"cmp"
	"slices"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/odiumuniverse/beadle/pkg/subagent"
)

// Host schema literals shared by the subagent codecs.
const (
	nameKey        = "name"
	descriptionKey = "description"
	toolsKey       = "tools"
	modelKey       = "model"
	modeKey        = "mode"
	primaryMode    = "primary"
	allMode        = "all"
	promptKey      = "prompt"
	subagentLabel  = "subagent"
	inheritModel   = "inherit"
	localKind      = "local"
	remoteKind     = "remote"
	globTool       = "glob"
)

// toolVocab maps canonical tool names to one host's spelling and back.
type toolVocab struct {
	forward map[string]string
	reverse map[string]string
}

// newToolVocab builds a vocabulary from a canonical-to-host map; the first
// canonical name that claims a host spelling owns the reverse direction.
func newToolVocab(forward map[string]string) toolVocab {
	reverse := make(map[string]string, len(forward))

	for _, tool := range canonicalToolOrder {
		name, ok := forward[tool]
		if !ok {
			continue
		}

		if _, taken := reverse[name]; !taken {
			reverse[name] = tool
		}
	}

	return toolVocab{forward: forward, reverse: reverse}
}

// host returns the host spelling of a canonical tool name.
func (v toolVocab) host(tool string) (string, bool) {
	name, ok := v.forward[tool]

	return name, ok
}

// canonical returns the canonical name of a host tool spelling.
func (v toolVocab) canonical(name string) (string, bool) {
	tool, ok := v.reverse[name]

	return tool, ok
}

// canonicalToolOrder lists canonical tool names in render order.
var canonicalToolOrder = []string{
	toolRead, toolWrite, toolEdit, toolNotebookEdit, toolBash, toolGrep,
	toolGlob, toolWebFetch, toolWebSearch, toolTask, toolAgent, toolSkill,
	toolAskUserQuestion,
}

// canonicalToolRank ranks canonical tools in render order.
var canonicalToolRank = func() map[string]int {
	rank := make(map[string]int, len(canonicalToolOrder))

	for i, tool := range canonicalToolOrder {
		rank[tool] = i
	}

	return rank
}()

// sortToolList dedups canonical tool names and sorts them in render order,
// with names outside the table (MCP tools) last in lexical order.
func sortToolList(tools []string) []string {
	out := slices.Clone(tools)
	slices.Sort(out)
	out = slices.Compact(out)

	slices.SortStableFunc(out, func(a, b string) int {
		ra, oka := canonicalToolRank[a]
		rb, okb := canonicalToolRank[b]

		switch {
		case oka && okb:
			return cmp.Compare(ra, rb)
		case oka:
			return -1
		case okb:
			return 1
		default:
			return 0
		}
	})

	return out
}

// claudeModelAliases lists the Claude Code model aliases: they are host
// spellings and must not leak into other hosts' model fields.
var claudeModelAliases = []string{"haiku", "sonnet", "opus"}

// claudeAlias reports whether a canonical model value is a Claude alias.
func claudeAlias(model string) bool {
	return slices.Contains(claudeModelAliases, model)
}

// splitCanonicalMCP splits a canonical mcp__<server>__<tool> name.
func splitCanonicalMCP(tool string) (string, string, bool) {
	rest, ok := strings.CutPrefix(tool, "mcp__")
	if !ok {
		return "", "", false
	}

	server, name, ok := strings.Cut(rest, "__")
	if !ok || server == "" || name == "" {
		return "", "", false
	}

	return server, name, true
}

// geminiMCPName converts a canonical MCP tool name to the Gemini FQN
// mcp_<server>_<tool>. Server names containing "_" have no unambiguous FQN.
func geminiMCPName(tool string) (string, bool) {
	server, name, ok := splitCanonicalMCP(tool)
	if !ok || strings.Contains(server, "_") {
		return "", false
	}

	return "mcp_" + server + "_" + name, true
}

// geminiCanonicalMCP converts a Gemini MCP tool FQN back to canonical form.
func geminiCanonicalMCP(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, "mcp_")
	if !ok {
		return "", false
	}

	server, tool, ok := strings.Cut(rest, "_")
	if !ok || server == "" || tool == "" {
		return "", false
	}

	for _, r := range server {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return "", false
		}
	}

	return "mcp__" + server + "__" + tool, true
}

// preserveToolExtras keeps the host tool entries of an existing file that
// the codec cannot canonicalize, so a rewrite never drops them.
func preserveToolExtras(existing []byte, mappable func(string) bool) []string {
	if len(existing) == 0 {
		return nil
	}

	front, _, ok := subagent.Split(existing)
	if !ok {
		return nil
	}

	var raw struct {
		Tools []string `yaml:"tools"`
	}

	if err := yaml.Unmarshal(front, &raw); err != nil {
		return nil
	}

	var out []string

	for _, name := range raw.Tools {
		if name != "" && !mappable(name) {
			out = append(out, name)
		}
	}

	return out
}

// dedupStrings removes empty and repeated notes, keeping the first order.
func dedupStrings(notes []string) []string {
	seen := make(map[string]bool, len(notes))

	out := make([]string, 0, len(notes))

	for _, note := range notes {
		if note == "" || seen[note] {
			continue
		}

		seen[note] = true

		out = append(out, note)
	}

	return out
}
