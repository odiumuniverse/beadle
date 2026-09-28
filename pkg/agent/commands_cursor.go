package agent

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/command"
	"github.com/odiumuniverse/beadle/pkg/frontmatter"
)

// cursorCommandCodec reads and writes Cursor command files. cursor-agent
// 2026.06.15 (`src/commands/custom-commands.ts`, `parseMarkdownCommand`)
// parses them as plain markdown: the file stem is the id, the first line the
// title (a leading `#` is stripped), the whole text the prompt, and only
// `$ARGUMENTS` and 1-based `$1..$99` are expanded.
//
// The host has no frontmatter schema, so the codec writes none: a
// `---\ndescription: …` block would be sent to the model as prompt text and
// would show up as the slash-menu title `---`. A file that carries one anyway
// — a user's, or another tool's — is therefore NOT this host's file, and the
// codec refuses it (parse → not ours, below) rather than adopting it: a lookup
// that adopted it could hand a write to a document this host does not own.
type cursorCommandCodec struct{}

// cursorTitleRE is the host's own heading rule (`extractTitle`).
var cursorTitleRE = regexp.MustCompile(`^#+\s*(.+)$`)

func (cursorCommandCodec) parse(path string, data []byte) (command.Document, bool, error) {
	// A document this codec would not have written is not this host's file, so
	// it is not ours to take over. Cursor's command files are plain markdown: a
	// leading `---` block is another dialect's (Claude's, and the canon's own),
	// and cursor-agent would send it to the model as prompt text and show it as
	// the slash-menu title. Refusing it here is what keeps a lookup from ever
	// handing a write to another host's file: the file surface writes in place
	// where a name is found, so a file this codec cannot own must not be a hit.
	if _, _, ok := frontmatter.Split(data); ok {
		return command.Document{}, false, nil
	}

	doc, err := command.Parse(data)
	if err != nil {
		return command.Document{}, false, fmt.Errorf("parse command: %w", err)
	}

	doc.Name = commandNameFromPath(path)

	if doc.Description == "" {
		doc.Description = cursorTitle(data, doc.Name)
	}

	tpl, err := rewriteCommandBody(doc.Body, commandArgsCursor, commandCanon)
	if err != nil {
		return command.Document{}, false, fmt.Errorf("the template cannot be lifted: %w", err)
	}

	doc.Body = tpl.Body

	return doc, true, nil
}

// cursorTitle applies the host's title rule to a command file: the first line
// if it is a heading, else the first line itself, else the file stem.
func cursorTitle(data []byte, fallback string) string {
	line, _, _ := strings.Cut(string(data), "\n")
	line = strings.TrimRight(line, "\r")

	if match := cursorTitleRE.FindStringSubmatch(line); match != nil {
		return strings.TrimSpace(match[1])
	}

	if trimmed := strings.TrimSpace(line); trimmed != "" {
		return trimmed
	}

	return fallback
}

// pullNotes reports the template constructs the host does not expand.
func (cursorCommandCodec) pullNotes(data []byte) []string {
	return commandPullNotes(string(data), commandArgsCursor, "cursor")
}

// fields renders the file body and no frontmatter: Cursor has no schema for
// one, and the host runs whatever text the file carries.
func (cursorCommandCodec) fields(doc command.Document, _ []byte) ([]frontmatter.Field, string, error) {
	tpl, err := rewriteCommandBody(doc.Body, commandCanon, commandArgsCursor)
	if err != nil {
		return nil, "", err
	}

	return nil, tpl.Body, nil
}

// managed lists the canonical keys a write drops: the host has no frontmatter
// schema, so a file that carries one anyway is normalized to plain markdown
// instead of keeping a block the host would send to the model as prompt text.
func (cursorCommandCodec) managed() []string {
	return []string{descriptionKey, argumentHintKey, argumentsKey, modelKey, disableModelInvocationKey}
}

// strayKeys is empty: a markdown file has no schema to fall back from.
func (cursorCommandCodec) strayKeys([]byte) []string { return nil }

func (cursorCommandCodec) audit(doc command.Document) []string {
	var notes []string

	if doc.Description != "" {
		notes = append(notes, "cursor takes the command title from the file's first line; the canonical description is not written")
	}

	for _, field := range []struct {
		key string
		set bool
	}{
		{argumentHintKey, doc.ArgumentHint != ""},
		{argumentsKey, len(doc.Arguments) > 0},
		{modelKey, doc.Model != ""},
		{disableModelInvocationKey, doc.DisableModelInvocation != nil},
	} {
		if field.set {
			notes = append(notes, field.key+" is not expressible for cursor; the vault value stays")
		}
	}

	if strings.TrimSpace(doc.Body) == "" {
		notes = append(notes, "cursor runs the file as the prompt; a command without a body is empty")
	}

	tpl, err := rewriteCommandBody(doc.Body, commandCanon, commandArgsCursor)
	if errors.Is(err, errCommandInexpressible) {
		notes = append(notes, "the template uses a placeholder cursor cannot express; the command is not synced to it "+
			"(cursor also loads ~/.claude/commands, so another host's copy of this command still reaches it there)")
	} else if err == nil {
		notes = append(notes, tpl.Notes...)
	}

	return dedupStrings(notes)
}
