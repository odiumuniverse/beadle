package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
)

// Host is a host whose user-level hooks file beadle presents the approved
// canon into. Native bundle hosts (Claude, Gemini, Antigravity) render hooks
// through pkg/bundle instead.
type Host string

const (
	Cursor Host = "cursor"
	Codex  Host = "codex"
)

// hostEvents maps the canon event names to the host event names.
var hostEvents = map[Host]map[string]string{
	Cursor: {
		EventPreTool:      "preToolUse",
		EventPostTool:     "postToolUse",
		EventSessionStart: "sessionStart",
		EventStop:         EventStop,
	},
	Codex: {
		EventPreTool:      "PreToolUse",
		EventPostTool:     "PostToolUse",
		EventSessionStart: "SessionStart",
		EventStop:         "Stop",
	},
}

// HostFile returns the user-level hooks file of a host.
func HostFile(host Host, home string) (string, bool) {
	switch host {
	case Cursor:
		return filepath.Join(home, ".cursor", "hooks.json"), true
	case Codex:
		return filepath.Join(home, ".codex", "hooks.json"), true
	default:
		return "", false
	}
}

// Op is one JSON Patch operation relative to the host hooks document.
type Op struct {
	Op    string
	Path  string
	Value any
}

// FilePlan is the merge of the approved canon into one host hooks document.
type FilePlan struct {
	// Ops are the patch operations that bring the document to the desired
	// state; empty when the file already holds it.
	Ops []Op
	// Owned lists the unique commands rendered by this plan, so the next run
	// can replace or remove exactly beadle's entries. Two hooks may share a
	// command and still render two entries.
	Owned []string
	// Rendered counts the rendered entries: every canon hook that reached a
	// host event counts once, even when two hooks share a command.
	Rendered int
	// Warnings list the approved hooks no host event can express, dropped
	// matchers and file shapes beadle refuses to touch. They repeat on every
	// plan, like the bundle renderers' unmappable notes.
	Warnings []string
}

// PlanHostFile computes the patch operations that render the approved canon
// hooks into one host hooks document. data is the current document (empty when
// the file does not exist yet) and owned lists the commands rendered last
// time. The function is pure: the caller reads, patches and writes the file.
//
// data must be plain JSON: both hosts parse their hooks files with a strict
// JSON parser, so a JSONC document would not load in the host either.
//
// Foreign entries are preserved. An entry is beadle's only when every command
// it carries is in owned; a Codex matcher group that mixes beadle and foreign
// handlers keeps the foreign handlers and re-renders the canon ones. Events
// whose value is not an array are never rewritten: they carry a structure
// beadle cannot merge, so the plan warns and leaves them alone.
func PlanHostFile(host Host, data []byte, canon map[string]Hook, approved map[string]bool, owned []string) (FilePlan, error) {
	events, ok := hostEvents[host]
	if !ok {
		return FilePlan{}, fmt.Errorf("unknown hooks host %q", host)
	}

	doc, err := decodeHostDoc(host, data)
	if err != nil {
		return FilePlan{}, err
	}

	existing, present := doc["hooks"].(map[string]any)
	if !present {
		existing = map[string]any{}
	}

	current, plan := renderHostHooks(host, events, canon, approved)

	skipped := malformedEvents(host, existing, current, &plan)

	ownedSet := map[string]bool{}

	for _, command := range owned {
		ownedSet[command] = true
	}

	plan.Warnings = append(plan.Warnings, missingOwnedWarnings(host, existing, owned)...)

	affected := affectedEvents(host, existing, current, ownedSet, skipped)
	if len(affected) == 0 {
		return plan, nil
	}

	plan.Ops, plan.Warnings = planEventOps(host, doc, existing, current, affected, ownedSet, plan.Warnings)

	return plan, nil
}

// decodeHostDoc parses the host document and rejects shapes beadle cannot
// safely patch: broken JSON, a non-object hooks member, an unknown Cursor
// schema version. Null members count as missing.
func decodeHostDoc(host Host, data []byte) (map[string]any, error) {
	doc := map[string]any{}

	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("parse %s hooks: %w", host, err)
		}
	}

	if raw, ok := doc["hooks"]; ok && raw != nil {
		if _, ok := raw.(map[string]any); !ok {
			return nil, fmt.Errorf("%s hooks: the hooks member is not an object", host)
		}
	}

	if host == Cursor {
		if version, ok := doc["version"]; ok && version != nil && version != float64(1) {
			return nil, fmt.Errorf("%s hooks: unsupported version %v", host, version)
		}
	}

	return doc, nil
}

// renderHostHooks renders the approved canon into host events, collecting the
// commands beadle owns and the warnings for unmappable events and matchers.
func renderHostHooks(host Host, events map[string]string, canon map[string]Hook, approved map[string]bool) (map[string][]any, FilePlan) {
	current := map[string][]any{}
	plan := FilePlan{}

	ownedSeen := map[string]bool{}
	rendered := 0

	for _, name := range slices.Sorted(maps.Keys(canon)) {
		hook := canon[name]
		if !approved[name] {
			continue
		}

		event, ok := events[hook.Event]
		if !ok {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("hook %s (%s): no %s event; not rendered", name, hook.Event, host))

			continue
		}

		entry, droppedMatcher := hostEntry(host, event, hook)
		if droppedMatcher {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"hook %s (%s): matcher %q is not expressible on %s %s; dropped", name, hook.Event, hook.Matcher, host, event))
		}

		current[event] = append(current[event], entry)
		rendered++

		if !ownedSeen[hook.Command] {
			ownedSeen[hook.Command] = true

			plan.Owned = append(plan.Owned, hook.Command)
		}
	}

	plan.Rendered = rendered

	return current, plan
}

// malformedEvents warns about events whose value is not an array and marks
// them so the plan never rewrites them. An event no canon hook maps to is
// skipped silently: it is a host-only shape beadle has no business in.
func malformedEvents(host Host, existing map[string]any, current map[string][]any, plan *FilePlan) map[string]bool {
	skipped := map[string]bool{}

	for event, raw := range existing {
		if raw == nil {
			continue
		}

		if _, ok := raw.([]any); ok {
			continue
		}

		skipped[event] = true

		if n := len(current[event]); n > 0 {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"%s hooks: event %s is not an array; left untouched, %d canon hook(s) not rendered", host, event, n))
		}
	}

	return skipped
}

// missingOwnedWarnings reports commands beadle rendered before and no longer
// finds in the file, so a manual edit or deletion does not silently turn into
// a second copy of the canon hook. Duplicate commands warn once.
func missingOwnedWarnings(host Host, existing map[string]any, owned []string) []string {
	found := existingCommands(host, existing)

	var (
		warns []string
		seen  = map[string]bool{}
	)

	for _, command := range owned {
		if found[command] || seen[command] {
			continue
		}

		seen[command] = true

		warns = append(warns, fmt.Sprintf("%s hooks: the previously rendered command %q is no longer in the file", host, command))
	}

	return warns
}

// affectedEvents lists the host events the plan touches: those it renders,
// those holding an entry beadle rendered before, and those holding a mixed
// Codex group — a revoked hook must still be cut out of a mixed group, even
// when the canon renders nothing into that event.
func affectedEvents(host Host, existing map[string]any, current map[string][]any, owned, skipped map[string]bool) map[string]bool {
	affected := map[string]bool{}

	for event := range current {
		if !skipped[event] {
			affected[event] = true
		}
	}

	for event, raw := range existing {
		if skipped[event] {
			continue
		}

		list, ok := raw.([]any)
		if !ok {
			continue
		}

		for _, entry := range list {
			if ownedEntry(host, entry, owned) || mixedEntry(host, entry, owned) {
				affected[event] = true

				break
			}
		}
	}

	return affected
}

// planEventOps builds the patch operations for the affected events, keeping
// every foreign entry and every foreign handler in place.
func planEventOps(
	host Host, doc, existing map[string]any, current map[string][]any, affected, owned map[string]bool, warns []string,
) ([]Op, []string) {
	var ops []Op

	if raw, ok := doc["hooks"]; !ok || raw == nil {
		ops = append(ops, Op{Op: "add", Path: "/hooks", Value: map[string]any{}})
	}

	if host == Cursor {
		if version, ok := doc["version"]; !ok || version == nil {
			ops = append(ops, Op{Op: "add", Path: "/version", Value: 1})
		}
	}

	for _, event := range slices.Sorted(maps.Keys(affected)) {
		list, _ := existing[event].([]any)
		next, mixed := nextEventEntries(host, list, current[event], owned)

		if mixed {
			warns = append(warns, fmt.Sprintf(
				"%s hooks: a hook group on %s mixes beadle and foreign handlers; the foreign handlers stay and beadle's handlers are re-rendered or removed",
				host, event))
		}

		if sameJSON(list, next) {
			continue
		}

		switch {
		case len(next) == 0 && list != nil:
			ops = append(ops, Op{Op: "remove", Path: "/hooks/" + event})
		case len(next) > 0:
			ops = append(ops, Op{Op: "add", Path: "/hooks/" + event, Value: next})
		}
	}

	return ops, warns
}

// nextEventEntries builds one event's new array: foreign entries and foreign
// handlers stay, beadle's entries make room for the canon render. It reports
// that a Codex group mixed both kinds.
func nextEventEntries(host Host, list, current []any, owned map[string]bool) ([]any, bool) {
	next := make([]any, 0, len(list)+len(current))
	mixed := false

	for _, entry := range list {
		switch {
		case ownedEntry(host, entry, owned):
			continue
		case mixedEntry(host, entry, owned):
			next = append(next, foreignHandlers(host, entry, owned))

			mixed = true
		default:
			next = append(next, entry)
		}
	}

	return append(next, current...), mixed
}

// hostEntry renders one canon hook in the host document shape. It reports a
// dropped matcher: session-start and stop match other values (or ignore the
// matcher), so a canon tool matcher would widen the hook or never match.
func hostEntry(host Host, event string, hook Hook) (any, bool) {
	matcher := matcherField(hook)
	dropped := matcher != "" && !hostMatcherEvent(host, event)

	if dropped {
		matcher = ""
	}

	switch host {
	case Cursor:
		entry := map[string]any{"command": hook.Command}

		if hook.Timeout > 0 {
			entry["timeout"] = hook.Timeout
		}

		if matcher != "" {
			entry["matcher"] = matcher
		}

		return entry, dropped
	case Codex:
		handler := map[string]any{"type": "command", "command": hook.Command}

		if hook.Timeout > 0 {
			handler["timeout"] = hook.Timeout
		}

		group := map[string]any{"hooks": []any{handler}}

		if matcher != "" {
			group["matcher"] = matcher
		}

		return group, dropped
	default:
		return nil, false
	}
}

// hostMatcherEvent reports whether a non-wildcard matcher is meaningful on the
// host event: pre/post-tool match tool names, session-start and stop match
// other vocabularies or ignore the matcher.
func hostMatcherEvent(host Host, event string) bool {
	switch host {
	case Cursor:
		return event == "preToolUse" || event == "postToolUse"
	case Codex:
		return event == "PreToolUse" || event == "PostToolUse"
	default:
		return false
	}
}

// matcherField returns the host matcher value: the wildcard form is expressed
// by omitting the field, like both hosts document.
func matcherField(hook Hook) string {
	if hook.Matcher == "" || hook.Matcher == "*" {
		return ""
	}

	return hook.Matcher
}

// entryCommands lists the commands one host entry carries.
func entryCommands(host Host, entry any) ([]string, bool) {
	obj, ok := entry.(map[string]any)
	if !ok {
		return nil, false
	}

	switch host {
	case Cursor:
		command, ok := obj["command"].(string)
		if !ok {
			return nil, false
		}

		return []string{command}, true
	case Codex:
		handlers, ok := obj["hooks"].([]any)
		if !ok || len(handlers) == 0 {
			return nil, false
		}

		commands := make([]string, 0, len(handlers))

		for _, handler := range handlers {
			object, ok := handler.(map[string]any)
			if !ok {
				return nil, false
			}

			command, ok := object["command"].(string)
			if !ok {
				return nil, false
			}

			commands = append(commands, command)
		}

		return commands, true
	default:
		return nil, false
	}
}

// ownedEntry reports that every command of the entry is one beadle rendered.
func ownedEntry(host Host, entry any, owned map[string]bool) bool {
	commands, ok := entryCommands(host, entry)
	if !ok {
		return false
	}

	for _, command := range commands {
		if !owned[command] {
			return false
		}
	}

	return true
}

// mixedEntry reports a Codex matcher group that carries both beadle and
// foreign handlers.
func mixedEntry(host Host, entry any, owned map[string]bool) bool {
	if host != Codex {
		return false
	}

	commands, ok := entryCommands(host, entry)
	if !ok {
		return false
	}

	ours := 0

	for _, command := range commands {
		if owned[command] {
			ours++
		}
	}

	return ours > 0 && ours < len(commands)
}

// foreignHandlers returns the entry with only the handlers beadle does not own.
func foreignHandlers(host Host, entry any, owned map[string]bool) any {
	obj, _ := entry.(map[string]any)
	handlers, _ := obj["hooks"].([]any)
	kept := make([]any, 0, len(handlers))

	for _, handler := range handlers {
		commands, ok := entryCommands(host, map[string]any{"hooks": []any{handler}})
		if ok && len(commands) == 1 && owned[commands[0]] {
			continue
		}

		kept = append(kept, handler)
	}

	clone := maps.Clone(obj)
	clone["hooks"] = kept

	return clone
}

// existingCommands collects the commands the document already holds.
func existingCommands(host Host, existing map[string]any) map[string]bool {
	found := map[string]bool{}

	for _, raw := range existing {
		list, ok := raw.([]any)
		if !ok {
			continue
		}

		for _, entry := range list {
			commands, ok := entryCommands(host, entry)
			if !ok {
				continue
			}

			for _, command := range commands {
				found[command] = true
			}
		}
	}

	return found
}

// FileHasCommands reports whether any of the commands is present in the host
// hooks document; the caller uses it to keep host-side notes accurate.
func FileHasCommands(host Host, data []byte, commands []string) bool {
	if _, ok := hostEvents[host]; !ok {
		return false
	}

	doc := map[string]any{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return false
	}

	existing, ok := doc["hooks"].(map[string]any)
	if !ok {
		return false
	}

	found := existingCommands(host, existing)

	for _, command := range commands {
		if found[command] {
			return true
		}
	}

	return false
}

// sameJSON compares two decoded JSON values semantically.
func sameJSON(left, right any) bool {
	leftData, err := json.Marshal(left)
	if err != nil {
		return false
	}

	rightData, err := json.Marshal(right)
	if err != nil {
		return false
	}

	return bytes.Equal(leftData, rightData)
}
