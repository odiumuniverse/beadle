package agent

import (
	"errors"
	"slices"

	"github.com/odiumuniverse/beadle/pkg/subagent"
)

// claudeColors lists the color names Claude Code accepts.
var claudeColors = []string{"red", "blue", "green", "yellow", "purple", "orange", "pink", "cyan"}

// claudePermissionModes lists the permission modes Claude Code accepts.
var claudePermissionModes = []string{
	"default", "acceptEdits", "auto", "dontAsk", "bypassPermissions", "plan", "manual",
}

// claudeSubagentCodec reads and writes Claude Code subagent files. Claude
// keeps the canonical field set minus the OpenCode-only mode and hidden.
type claudeSubagentCodec struct{}

func (claudeSubagentCodec) parse(_ string, data []byte) (subagent.Document, bool, error) {
	doc, err := subagent.Parse(data)
	if err != nil {
		return subagent.Document{}, false, err
	}

	if doc.Name == "" {
		// Claude treats a file without a name as documentation and skips it.
		return subagent.Document{}, false, nil
	}

	// mode and hidden are not Claude fields: a file that carries them keeps
	// them as host extras, they never enter the canon.
	doc.Mode = ""
	doc.Hidden = nil

	return doc, true, nil
}

func (claudeSubagentCodec) fields(doc subagent.Document, _ []byte) ([]subagent.Field, string, error) {
	if doc.Name == "" {
		return nil, "", errors.New("subagent name is required")
	}

	fields := make([]subagent.Field, 0, len(subagent.Keys()))

	for _, field := range subagent.Fields(doc) {
		if claudeFieldValid(field.Key, doc) {
			fields = append(fields, field)
		}
	}

	return fields, doc.Body, nil
}

// claudeFieldValid drops the canonical fields Claude cannot express or would
// reject: mode and hidden do not exist there, color takes a palette name,
// permissionMode and isolation are enums.
func claudeFieldValid(key string, doc subagent.Document) bool {
	switch key {
	case "mode", "hidden":
		return false
	case "color":
		return slices.Contains(claudeColors, doc.Color)
	case "permissionMode":
		return slices.Contains(claudePermissionModes, doc.PermissionMode)
	case "isolation":
		return doc.Isolation == "worktree"
	default:
		return true
	}
}

// managed lists the canonical keys Claude owns. mode and hidden are not
// Claude fields, so a file that carries them keeps them as host extras.
func (claudeSubagentCodec) managed() []string {
	out := make([]string, 0, len(subagent.Keys()))

	for _, key := range subagent.Keys() {
		if key == "mode" || key == "hidden" {
			continue
		}

		out = append(out, key)
	}

	return out
}

// strayKeys is empty: Claude ignores unknown frontmatter keys, so they are
// not a schema hazard.
func (claudeSubagentCodec) strayKeys([]byte) []string { return nil }

// audit is empty: Claude expresses the canonical field set, and mode/hidden
// are host extras that stay in the file.
func (claudeSubagentCodec) audit(subagent.Document) []string { return nil }
