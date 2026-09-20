package rulings

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

const sigHashLen = 12

type Signature struct {
	Kind       kind.ID `json:"kind"`
	Target     string  `json:"target"`
	Divergence string  `json:"divergence"`
	Scope      string  `json:"scope"`
}

const (
	DivDeleted        = "deleted"
	DivAdded          = "added"
	DivModified       = "modified"
	DivJSONKeyAdded   = "json:key-added"
	DivJSONKeyRemoved = "json:key-removed"
	DivJSONValue      = "json:value-changed"
	DivJSONRiskyValue = "json:value-changed:command|url"
	DivTextContent    = "text:content"
	DivTextFormatting = "text:formatting-only"

	ScopeGlobal = "global"
)

func ComputeSignature(k kind.ID, reason, key, vaultKey, scope string, base, vault, local []byte) (Signature, error) {
	target, err := normalizeTarget(k, key, vaultKey)
	if err != nil {
		return Signature{}, err
	}

	divergence, err := computeDivergence(k, reason, base, vault, local)
	if err != nil {
		return Signature{}, err
	}

	return Signature{
		Kind:       k,
		Target:     target,
		Divergence: divergence,
		Scope:      normalizeScope(scope),
	}, nil
}

func (s Signature) Hash() string {
	data, err := json.Marshal(s.canonical())
	if err != nil {
		return ""
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])[:sigHashLen]
}

type canonicalField struct {
	K string `json:"k"`
	V string `json:"v"`
}

func (s Signature) canonical() []canonicalField {
	return []canonicalField{
		{"divergence", s.Divergence},
		{"kind", string(s.Kind)},
		{"scope", s.Scope},
		{"target", s.Target},
	}
}

func (s Signature) BlastRadius() bool {
	if s.Kind == kind.Permissions {
		return true
	}

	return s.Divergence == DivJSONRiskyValue
}

func normalizeTarget(k kind.ID, key, vaultKey string) (string, error) {
	switch k {
	case kind.Rules, kind.Permissions, kind.Projects:
		return "main", nil
	case kind.Skills, kind.Memory:
		first, _, _ := strings.Cut(key, "/")
		if first == "" {
			return key, nil
		}

		return "skills/" + first, nil
	case kind.MCP:
		server := key
		if vaultKey != "" {
			server = vaultKey
		}

		if field := mcpField(key); field != "" {
			return server + "." + field, nil
		}

		return server, nil
	default:
		return key, nil
	}
}

func mcpField(key string) string {
	_, rest, ok := strings.Cut(key, ".")
	if !ok {
		return ""
	}

	return rest
}

func computeDivergence(k kind.ID, reason string, base, vault, local []byte) (string, error) {
	if k == kind.MCP {
		return jsonDivergence(reason, base, vault, local)
	}

	switch reason {
	case DivDeleted:
		return DivDeleted, nil
	case DivAdded:
		return DivAdded, nil
	}

	switch k {
	case kind.Rules, kind.Skills, kind.Memory, kind.Projects:
		return textDivergence(vault, local), nil
	default:
		return DivModified, nil
	}
}

func jsonDivergence(reason string, base, vault, local []byte) (string, error) {
	switch reason {
	case DivAdded:
		return DivJSONKeyAdded, nil
	case DivDeleted:
		return DivJSONKeyRemoved, nil
	}

	if base == nil || vault == nil || local == nil {
		return DivJSONValue, nil
	}

	oldServer, oldErr := mcp.Decode(vault)

	newServer, newErr := mcp.Decode(local)
	if oldErr != nil || newErr != nil {
		return DivJSONValue, nil //nolint:nilerr // an undecodable side is a plain value change
	}

	if commandDiffers(oldServer, newServer) || urlDiffers(oldServer, newServer) {
		return DivJSONRiskyValue, nil
	}

	return DivJSONValue, nil
}

func textDivergence(vault, local []byte) string {
	if collapseSpace(string(vault)) == collapseSpace(string(local)) {
		return DivTextFormatting
	}

	return DivTextContent
}

func collapseSpace(text string) string {
	return strings.Join(strings.Fields(text), "")
}

func normalizeScope(scope string) string {
	trimmed := strings.TrimSpace(scope)
	if trimmed == "" {
		return ScopeGlobal
	}

	return trimmed
}

func ValidateScope(scope string) error {
	trimmed := normalizeScope(scope)

	switch {
	case trimmed == ScopeGlobal:
		return nil
	case strings.HasPrefix(trimmed, "host:"), strings.HasPrefix(trimmed, "project:"):
		if strings.TrimSpace(strings.SplitN(trimmed, ":", 2)[1]) == "" {
			return fmt.Errorf("scope %q has an empty value", scope)
		}

		return nil
	default:
		return fmt.Errorf("unknown scope %q (expected global, host:<agent> or project:<id>)", scope)
	}
}
