package agent

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/command"
	"github.com/odiumuniverse/beadle/pkg/frontmatter"
)

// commandCodec reads and writes one markdown host's command files. The hosts
// share the frontmatter schema and differ in the template dialect and in the
// canonical fields they can express.
type commandCodec struct {
	// host names the host in diagnostics.
	host string
	// args is the host's template dialect.
	args commandArgs
	// model, hint, arguments and disable report whether the host supports the
	// matching canonical frontmatter key.
	model     bool
	hint      bool
	arguments bool
	disable   bool
	// pullOnly marks hosts whose files are pulled but never written (Codex).
	pullOnly bool
}

// commandModel adapts the canonical command package to the file surface: the
// item key carries the identity, the frontmatter never does.
type commandModel struct{}

func (commandModel) parse(name string, value []byte) (command.Document, error) {
	doc, err := command.Parse(value)
	if err != nil {
		return command.Document{}, err
	}

	doc.Name = name

	return doc, nil
}

func (commandModel) render(doc command.Document) []byte { return command.Render(doc) }

func (commandModel) name(doc command.Document) string { return doc.Name }

func (commandModel) validName(name string) bool { return command.ValidName(name) }

// commandNameFromPath derives the command identity from a host file name.
func commandNameFromPath(path string) string {
	base := filepath.Base(path)

	return strings.TrimSuffix(base, filepath.Ext(base))
}

// managed lists the frontmatter keys the codec owns.
func (c commandCodec) managed() []string {
	out := []string{descriptionKey}

	if c.hint {
		out = append(out, "argument-hint")
	}

	if c.arguments {
		out = append(out, "arguments")
	}

	if c.model {
		out = append(out, modelKey)
	}

	if c.disable {
		out = append(out, "disable-model-invocation")
	}

	return out
}

// strayKeys is empty: the markdown command hosts ignore unknown keys.
func (commandCodec) strayKeys([]byte) []string { return nil }

func (c commandCodec) parse(path string, data []byte) (command.Document, bool, error) {
	doc, err := command.Parse(data)
	if err != nil {
		return command.Document{}, false, err
	}

	doc.Name = commandNameFromPath(path)

	// Canonical keys the host does not define stay host extras in the file:
	// they never enter the canon.
	if !c.hint {
		doc.ArgumentHint = ""
	}

	if !c.arguments {
		doc.Arguments = nil
	}

	if !c.model {
		doc.Model = ""
	}

	if !c.disable {
		doc.DisableModelInvocation = nil
	}

	tpl, err := rewriteCommandBody(doc.Body, c.args, commandCanon)
	if err != nil {
		return command.Document{}, false, fmt.Errorf("the template cannot be lifted: %w", err)
	}

	doc.Body = tpl.Body

	return doc, true, nil
}

// pullNotes reports the template constructs the host does not expand.
func (c commandCodec) pullNotes(data []byte) []string {
	doc, err := command.Parse(data)
	if err != nil {
		return nil
	}

	return commandPullNotes(doc.Body, c.args, c.host)
}

func (c commandCodec) fields(doc command.Document, _ []byte) ([]frontmatter.Field, string, error) {
	if !command.ValidName(doc.Name) {
		return nil, "", errors.New("command name is required")
	}

	tpl, err := rewriteCommandBody(doc.Body, commandCanon, c.args)
	if err != nil {
		return nil, "", err
	}

	var fields []frontmatter.Field

	if doc.Description != "" {
		fields = append(fields, frontmatter.Field{Key: descriptionKey, Value: doc.Description})
	}

	if c.hint && doc.ArgumentHint != "" {
		fields = append(fields, frontmatter.Field{Key: "argument-hint", Value: doc.ArgumentHint})
	}

	if c.arguments && len(doc.Arguments) > 0 {
		fields = append(fields, frontmatter.Field{Key: "arguments", Value: doc.Arguments})
	}

	if c.model && doc.Model != "" {
		fields = append(fields, frontmatter.Field{Key: modelKey, Value: doc.Model})
	}

	if c.disable && doc.DisableModelInvocation != nil {
		fields = append(fields, frontmatter.Field{Key: "disable-model-invocation", Value: *doc.DisableModelInvocation})
	}

	return fields, tpl.Body, nil
}

func (c commandCodec) audit(doc command.Document) []string {
	if c.pullOnly {
		// Nothing is written to the host, so expressiveness does not matter.
		return nil
	}

	var notes []string

	if c.args.named && c.arguments {
		for _, name := range commandNamedPlaceholders(doc.Body) {
			if !slices.Contains(doc.Arguments, name) {
				notes = append(notes, fmt.Sprintf(
					"the template uses $%s but arguments does not declare it; claude keeps the placeholder literal", name))
			}
		}
	}

	for _, field := range []struct {
		key       string
		set       bool
		supported bool
	}{
		{"argument-hint", doc.ArgumentHint != "", c.hint},
		{"arguments", len(doc.Arguments) > 0, c.arguments},
		{modelKey, doc.Model != "", c.model},
		{"disable-model-invocation", doc.DisableModelInvocation != nil, c.disable},
	} {
		if field.set && !field.supported {
			notes = append(notes, fmt.Sprintf("%s is not expressible for %s; the vault value stays", field.key, c.host))
		}
	}

	tpl, err := rewriteCommandBody(doc.Body, commandCanon, c.args)
	if errors.Is(err, errCommandInexpressible) {
		notes = append(notes, fmt.Sprintf("the template uses a placeholder %s cannot express; the command is not synced to it", c.host))
	} else if err == nil {
		notes = append(notes, commandForeignNotes(tpl.Foreign, c.host)...)
		notes = append(notes, tpl.Notes...)
	}

	return dedupStrings(notes)
}
