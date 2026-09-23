package agent

// PatchOp is one RFC 6902 operation for PatchJSON.
type PatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

// PatchJSON applies RFC 6902 operations to a JSON/JSONC document the way the
// agent config surfaces patch their files: the formatting and comments of
// untouched members stay, an added member is rendered with the document's own
// indentation, and a replaced member is re-rendered from the tree only when
// the same patch also adds its children — otherwise the replacement keeps the
// patch payload's compact form (still valid JSON and stable across runs).
// data must be a valid JSON document; an empty operation list returns data
// untouched.
func PatchJSON(data []byte, ops []PatchOp) ([]byte, error) {
	if len(ops) == 0 {
		return data, nil
	}

	converted := make([]patchOp, len(ops))

	for i, op := range ops {
		converted[i] = patchOp(op)
	}

	return applyPatch(data, converted)
}
