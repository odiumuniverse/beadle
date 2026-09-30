package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/verger/pkg/verger"
)

// Every beadle document that a machine reads carries the same envelope, and it
// carries it first: the document's name and its version, so a consumer can
// tell what it got without guessing from the fields that follow.
//
// The vocabulary inside the document is the user's (§1 of the UX spec): a state
// is a word a user can act on, not an internal type name. The envelope is the
// only place beadle names itself, and it does so once.
const (
	// SchemaVersion is the major version of every beadle document. Within it a
	// field may be added; a removal, a rename or a retyping needs a bump.
	SchemaVersion = 1
)

// schemaField is the envelope itself, spelled exactly as verger spells it, so
// one consumer reads both tools' documents.
type schemaField struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

// withSchema is the envelope for a document beadle defines itself, embedded so
// it marshals first.
type withSchema struct {
	Schema schemaField `json:"schema"`
}

func newEnvelope(name string) withSchema {
	return withSchema{Schema: schemaField{Name: name, Version: SchemaVersion}}
}

// envelope wraps a payload beadle does not own - a library document, a report -
// in the shared envelope. The payload keeps its own field names, inline and
// beside the schema: spliced, not nested, because a consumer must not have to
// know which tool wrote the document to find the data.
type envelope struct {
	name    string
	payload any
}

// MarshalJSON renders `{"schema":{…}, …payload}`, schema first, always.
func (e envelope) MarshalJSON() ([]byte, error) {
	head, err := json.Marshal(newEnvelope(e.name))
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(e.payload)
	if err != nil {
		return nil, err
	}

	// The object payload loses its braces, so the envelope's comma joins the
	// halves; a payload that is not an object keeps its own and sits beside.
	// writeJSON does the indenting, so this renders compact.
	if len(body) > 0 && body[0] == '{' && len(body) > 2 {
		inner := string(body[1 : len(body)-1])

		return append(append(append(head[:len(head)-1], ','), inner...), '}'), nil
	}

	if len(body) > 0 && body[0] == '{' {
		return append(head[:len(head)-1], '}'), nil
	}

	return append(append(head[:len(head)-1], ','), body...), nil
}

// writeJSON prints one document to the command's stdout and nothing else: the
// document is the whole contract of that channel, so a log line in front of it
// would break every consumer that pipes it.
func writeJSON(out io.Writer, doc any) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot render the document: %w", err)
	}

	_, err = fmt.Fprintf(out, "%s\n", data)

	return err
}

// jsonFormAnnotation is the cobra annotation a command carries when it has a
// machine-readable form, and its value is the document's `schema.name`.
//
// It is the only place that name is written. The command reads it back when it
// prints, so the annotation the refusal consults, the name on stdout, and the
// table in `guide/ai-agents.md` are three views of one string rather than three
// places to keep in step — and a command that invents a name in its own body
// cannot be written by accident.
const jsonFormAnnotation = "beadle.json"

// jsonForm marks a command as having a machine-readable form and names the
// document it prints. Every command that answers --json calls this exactly once,
// and the walk test requires that every command either calls it or is refused.
func jsonForm(cmd *cobra.Command, name string) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}

	cmd.Annotations[jsonFormAnnotation] = name

	return cmd
}

// jsonSchemaName returns the document a command prints under --json, or "" when
// it has none. A subcommand inherits nothing: `beadle plugins --json` is not
// `beadle plugins list --json`, and answering with the parent's document would be
// a lie about which one ran.
func jsonSchemaName(cmd *cobra.Command) string {
	if cmd == nil || cmd.Annotations == nil {
		return ""
	}

	return cmd.Annotations[jsonFormAnnotation]
}

// noJSONForm is the answer for a command that has no machine-readable form. It
// is a usage error, so it exits 2 like any other bad invocation: the caller
// named a capability the command does not have, which is a mistake in the
// invocation rather than a fault in the vault or a question for the user.
//
// The alternative — printing the human table and saying nothing — is the one
// thing a JSON consumer cannot defend against: a script that pipes beadle into
// `jq` gets a parse error with no hint that the flag was the mistake, and the
// flag's own help text promises a document.
func noJSONForm(cmd, root *cobra.Command) error {
	have := jsonFormCommands(root)
	if len(have) == 0 {
		return &verger.UsageError{Cause: fmt.Errorf("`%s` has no machine-readable form; drop --json", cmd.CommandPath())}
	}

	return &verger.UsageError{Cause: fmt.Errorf(
		"`%s` has no machine-readable form; drop --json, or use one of: %s",
		cmd.CommandPath(), strings.Join(have, ", "),
	)}
}

// jsonFormCommands lists every command in the tree that does have a document, by
// the path a user would type. It is what the refusal offers instead of a bare
// "no", because the useful answer to "this command cannot do that" is the one
// that can.
func jsonFormCommands(root *cobra.Command) []string {
	var out []string

	var walk func(*cobra.Command)

	walk = func(cmd *cobra.Command) {
		if name := jsonSchemaName(cmd); name != "" {
			out = append(out, cmd.CommandPath())
		}

		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}

	walk(root)

	sort.Strings(out)

	return out
}
