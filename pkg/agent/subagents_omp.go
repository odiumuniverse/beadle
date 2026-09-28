package agent

import (
	"errors"
	"fmt"
	"slices"

	"github.com/odiumuniverse/beadle/pkg/subagent"
)

// ompSubagentUnsupported lists the canonical subagent keys the omp task-agent
// schema does not define. They never enter the canon from an omp file, never
// reach an omp file, and the doctor reports them; a file that carries one
// keeps it as a host extra.
var ompSubagentUnsupported = []string{
	modeKey, hiddenKey, "disallowedTools", "permissionMode", "maxTurns",
	"isolation", colorKey, skillsKey, "mcpServers", "background",
}

// ompSubagentManaged lists the canonical keys the codec owns, in canonical
// order minus the keys omp does not define.
func ompSubagentManaged() []string {
	out := make([]string, 0, len(subagent.Keys()))

	for _, key := range subagent.Keys() {
		if !isOmpSubagentUnsupported(key) {
			out = append(out, key)
		}
	}

	return out
}

func isOmpSubagentUnsupported(key string) bool {
	return slices.Contains(ompSubagentUnsupported, key)
}

// ompToolVocab maps the canonical subagent tool names to the omp tools and
// back. The omp spellings come from the bundled agent frontmatter
// (`omp agents unpack --dir <dir>`): read, write, edit, notebook, bash, grep,
// glob, web_search, task, ask. The canonical names alone are not enough:
// omp lowercases a name it knows (Write and write both count as a write tool,
// live-verified through the task-agent READ-ONLY annotation), but it has no
// WebFetch, Skill or Agent tool, so an unmapped name would be written and
// ignored in silence.
var ompToolVocab = newToolVocab(map[string]string{
	toolRead:            toolNameRead,
	toolWrite:           "write",
	toolEdit:            toolNameEdit,
	toolNotebookEdit:    "notebook",
	toolBash:            "bash",
	toolGrep:            toolNameGrep,
	toolGlob:            toolNameGlob,
	toolWebSearch:       "web_search",
	toolTask:            toolNameTask,
	toolAgent:           "task",
	toolAskUserQuestion: "ask",
})

// ompTool maps a canonical tool name to the omp spelling.
func ompTool(tool string) (string, bool) {
	return ompToolVocab.host(tool)
}

// ompCanonicalTool maps an omp tool spelling back to the canonical name.
func ompCanonicalTool(name string) (string, bool) {
	return ompToolVocab.canonical(name)
}

// ompCanonicalTools maps the tool list of an omp agent file back into the
// canon: the entries omp has no canonical equivalent for are dropped from the
// canon and stay in the file as host extras.
func ompCanonicalTools(names []string) []string {
	var out []string

	for _, name := range names {
		if tool, ok := ompCanonicalTool(name); ok {
			out = append(out, tool)
		}
	}

	return sortToolList(out)
}

// ompSubagentTools renders the tool allowlist: the canonical tools in their
// omp spelling, followed by the host entries the codec cannot canonicalize,
// so a rewrite never drops an omp-only tool.
func ompSubagentTools(doc subagent.Document, existing []byte) []string {
	var out []string

	for _, tool := range doc.Tools {
		if name, ok := ompTool(tool); ok {
			out = append(out, name)
		}
	}

	extra := preserveToolExtras(existing, func(name string) bool {
		_, ok := ompCanonicalTool(name)

		return ok
	})

	return append(out, extra...)
}

// ompSubagentCodec reads and writes oh-my-pi task-agent files. omp requires a
// name and a description (a file missing either is skipped) and knows tools,
// spawns, model, thinking-level, output, blocking, autoloadSkills and the
// other omp-only keys; the canonical keys it does not define are dropped from
// the host file and stay in the vault, while omp-only keys stay in the file
// as host extras.
type ompSubagentCodec struct{}

func (ompSubagentCodec) parse(_ string, data []byte) (subagent.Document, bool, error) {
	doc, err := subagent.Parse(data)
	if err != nil {
		return subagent.Document{}, false, err
	}

	if doc.Name == "" || doc.Description == "" {
		// omp skips a file that is missing either required field.
		return subagent.Document{}, false, nil
	}

	// Keys omp does not define never enter the canon; the file keeps them as
	// host extras.
	doc.Mode = ""
	doc.DisallowedTools = nil
	doc.PermissionMode = ""
	doc.MaxTurns = 0
	doc.Isolation = ""
	doc.Color = ""
	doc.Hidden = nil
	doc.Tools = ompCanonicalTools(doc.Tools)

	return doc, true, nil
}

func (ompSubagentCodec) fields(doc subagent.Document, existing []byte) ([]subagent.Field, string, error) {
	if !subagent.ValidName(doc.Name) {
		return nil, "", errors.New("subagent name is required")
	}

	if doc.Description == "" {
		// omp skips a task agent without a description, so a file would be
		// written and silently ignored: the item is hidden from the host
		// instead (the doctor reports a stale copy of one already on disk).
		return nil, "", fmt.Errorf("subagent %s: omp requires a description: %w", doc.Name, errItemInexpressible)
	}

	fields := make([]subagent.Field, 0, len(ompSubagentManaged()))

	for _, field := range subagent.Fields(doc) {
		switch field.Key {
		case toolsKey:
			tools := ompSubagentTools(doc, existing)
			if len(tools) == 0 {
				continue
			}

			field.Value = tools
		case modelKey:
			field.Value = ompSubagentModel(doc.Model)
			if field.Value == "" {
				continue
			}
		}

		if !ompSubagentFieldValid(field.Key, doc) {
			continue
		}

		fields = append(fields, field)
	}

	return fields, doc.Body, nil
}

// ompSubagentFieldValid drops the canonical keys omp does not define, a
// Claude model alias it cannot resolve, and an empty description it would
// skip the file for (an existing value in the file stays).
func ompSubagentFieldValid(key string, doc subagent.Document) bool {
	switch key {
	case modelKey:
		return ompSubagentModel(doc.Model) != ""
	case descriptionKey:
		return doc.Description != ""
	default:
		return !isOmpSubagentUnsupported(key)
	}
}

// ompSubagentModel keeps a model omp can resolve: omp accepts selectors and
// role aliases, so only the Claude aliases (host spellings of another agent)
// are dropped.
func ompSubagentModel(model string) string {
	if claudeAlias(model) {
		return ""
	}

	return model
}

// managed lists the canonical keys the codec owns; the keys omp does not
// define and the omp-only keys a file may carry stay outside the set.
func (ompSubagentCodec) managed() []string { return ompSubagentManaged() }

// strayKeys is empty: omp preserves unknown frontmatter keys as metadata.
func (ompSubagentCodec) strayKeys([]byte) []string { return nil }

func (ompSubagentCodec) audit(doc subagent.Document) []string {
	var notes []string

	for _, tool := range doc.Tools {
		if _, ok := ompTool(tool); !ok {
			notes = append(notes, fmt.Sprintf("tool %q has no omp equivalent; it is not written", tool))
		}
	}

	if ompToolsLost(doc) {
		notes = append(notes, "no canonical tool has an omp equivalent; the host keeps all tools (fail-open)")
	}

	for _, key := range ompSubagentUnsupported {
		if ompSubagentFieldSet(key, doc) {
			notes = append(notes, key+" is not expressible for omp; the vault value stays")
		}
	}

	if claudeAlias(doc.Model) {
		notes = append(notes, fmt.Sprintf(
			"model %q is a Claude alias and does not map to omp; the file inherits the session model", doc.Model))
	}

	if doc.Description == "" {
		notes = append(notes, "omp requires a description; an existing value is kept and a new file stays incomplete")
	}

	return dedupStrings(notes)
}

// ompToolsLost reports an allowlist with no omp equivalent: the codec writes
// no tools key, and the host then keeps every tool.
func ompToolsLost(doc subagent.Document) bool {
	if doc.Tools == nil {
		return false
	}

	for _, tool := range doc.Tools {
		if _, ok := ompTool(tool); ok {
			return false
		}
	}

	return true
}

// ompSubagentFieldSet reports whether a canonical document carries the key.
func ompSubagentFieldSet(key string, doc subagent.Document) bool {
	switch key {
	case modeKey:
		return doc.Mode != ""
	case hiddenKey:
		return doc.Hidden != nil
	case "disallowedTools":
		return len(doc.DisallowedTools) > 0
	case "permissionMode":
		return doc.PermissionMode != ""
	case "maxTurns":
		return doc.MaxTurns > 0
	case "isolation":
		return doc.Isolation != ""
	case colorKey:
		return doc.Color != ""
	case skillsKey:
		return len(doc.Skills) > 0
	case "mcpServers":
		return len(doc.MCPServers) > 0
	case "background":
		return doc.Background != nil
	default:
		return false
	}
}
