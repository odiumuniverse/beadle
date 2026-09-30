package cli

import (
	"encoding/json"
	"fmt"
	"io"
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
