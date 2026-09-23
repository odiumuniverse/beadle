package agent

import (
	"errors"
	"fmt"

	toml "github.com/pelletier/go-toml"

	"github.com/odiumuniverse/beadle/pkg/command"
	"github.com/odiumuniverse/beadle/pkg/frontmatter"
)

// geminiCommandKeys lists the TOML keys the Gemini command codec owns.
var geminiCommandKeys = []string{descriptionKey, promptKey}

// geminiCommandCodec reads and writes Gemini CLI command files: TOML with a
// required prompt and an optional description.
type geminiCommandCodec struct{}

func (geminiCommandCodec) parse(path string, data []byte) (command.Document, bool, error) {
	tree, err := toml.LoadBytes(data)
	if err != nil {
		return command.Document{}, false, fmt.Errorf("parse toml: %w", err)
	}

	prompt, _ := tree.GetPath([]string{promptKey}).(string)
	if prompt == "" {
		return command.Document{}, false, errors.New("the " + promptKey + " key is required")
	}

	// Only description and prompt exist in the Gemini schema; every other
	// canonical key stays a host extra in the file.
	doc := command.Document{Name: commandNameFromPath(path), Body: prompt}

	if description, ok := tree.GetPath([]string{descriptionKey}).(string); ok {
		doc.Description = description
	}

	tpl, err := rewriteCommandBody(doc.Body, commandArgsGemini, commandCanon)
	if err != nil {
		return command.Document{}, false, fmt.Errorf("the template cannot be lifted: %w", err)
	}

	doc.Body = tpl.Body

	return doc, true, nil
}

// pullNotes reports the template constructs the host does not expand.
func (geminiCommandCodec) pullNotes(data []byte) []string {
	tree, err := toml.LoadBytes(data)
	if err != nil {
		return nil
	}

	prompt, _ := tree.GetPath([]string{promptKey}).(string)

	return commandPullNotes(prompt, commandArgsGemini, "gemini")
}

// fields is unused: the codec renders whole TOML files.
func (geminiCommandCodec) fields(command.Document, []byte) ([]frontmatter.Field, string, error) {
	return nil, "", errors.New("gemini commands are rendered as TOML")
}

// managed lists the TOML keys the codec owns.
func (geminiCommandCodec) managed() []string { return geminiCommandKeys }

// strayKeys is empty: Gemini accepts arbitrary TOML keys.
func (geminiCommandCodec) strayKeys([]byte) []string { return nil }

// render writes the managed TOML keys with a surgical span edit: comments,
// foreign keys and tables stay byte-identical.
func (geminiCommandCodec) render(doc command.Document, existing []byte) ([]byte, error) {
	if len(existing) > 0 {
		if _, err := toml.LoadBytes(existing); err != nil {
			return nil, fmt.Errorf("parse toml: %w", err)
		}
	}

	if !command.ValidName(doc.Name) {
		return nil, errors.New("command name is required")
	}

	tpl, err := rewriteCommandBody(doc.Body, commandCanon, commandArgsGemini)
	if err != nil {
		return nil, err
	}

	values := map[string]string{}
	keep := make([]string, 0, 1)

	// description is optional: an empty canon value removes the key, so a
	// cleared description reaches the host.
	if doc.Description != "" {
		values[descriptionKey] = tomlBasicString(doc.Description)
	}

	switch {
	case tpl.Body != "":
		values[promptKey] = tomlMultilineValue(tpl.Body)
	case hasExistingPrompt(existing):
		// prompt is required: keep the file's value when the canon body is
		// empty instead of writing an invalid TOML.
		keep = append(keep, promptKey)
	default:
		return nil, errCommandInexpressible
	}

	return editTOMLKeys(existing, geminiCommandKeys, keep, values)
}

// hasExistingPrompt reports whether an existing file already carries a
// prompt value.
func hasExistingPrompt(existing []byte) bool {
	prompt, ok := existingTOMLString(existing, promptKey)

	return ok && prompt != ""
}

func (geminiCommandCodec) audit(doc command.Document) []string {
	var notes []string

	for _, field := range []struct {
		key string
		set bool
	}{
		{"argument-hint", doc.ArgumentHint != ""},
		{"arguments", len(doc.Arguments) > 0},
		{modelKey, doc.Model != ""},
		{"disable-model-invocation", doc.DisableModelInvocation != nil},
	} {
		if field.set {
			notes = append(notes, field.key+" is not expressible for gemini; the vault value stays")
		}
	}

	if doc.Body == "" {
		notes = append(notes, "gemini requires a prompt; a command without a body is not synced")
	}

	tpl, err := rewriteCommandBody(doc.Body, commandCanon, commandArgsGemini)
	if errors.Is(err, errCommandInexpressible) {
		notes = append(notes, "the template uses a placeholder gemini cannot express; the command is not synced to it")
	} else if err == nil {
		notes = append(notes, tpl.Notes...)
	}

	return dedupStrings(notes)
}
