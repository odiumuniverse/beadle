package agent

import (
	"fmt"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/odiumuniverse/beadle/pkg/frontmatter"
	"github.com/odiumuniverse/beadle/pkg/skill"
)

// dshSkillCodec checks one canon skill tree against the DeepSeek Harness
// dialect. DSH reads the Agent Skills layout: it requires a kebab-case
// frontmatter name and a description, accepts whenToUse, metadata and the
// disable-model-invocation/user-invocable pair, ignores unknown keys, and
// drops a skill whose invocation value is not a boolean. The codec therefore
// validates instead of rewriting: a tree that passes travels byte-for-byte,
// so the canon and the host file stay identical and repeat syncs are no-ops.
type dshSkillCodec struct{}

// dshInvocationKeys are the invocation controls DSH reads from the
// frontmatter. They are independent: an omitted key permits its surface.
var dshInvocationKeys = []string{disableModelInvocationKey, "user-invocable"}

// dshLegacyInvocationKeys lists the camelCase spellings DSH rejects outright:
// a skill carrying one is dropped from the catalog, so the write refuses it
// and names the canonical key to use instead. The hints are per key because
// modelInvocable has the opposite polarity to disable-model-invocation — a
// literal "use disable-model-invocation" would invert the meaning. The order
// is fixed for deterministic diagnostics.
var dshLegacyInvocationKeys = []struct{ legacy, hint string }{
	{"disableModelInvocation", "use " + disableModelInvocationKey},
	{"modelInvocable", "use " + disableModelInvocationKey + " with the inverted value, or drop the key"},
	{"userInvocable", "use user-invocable"},
}

// checkName rejects a canon skill name DSH cannot read. toHost calls it
// before any parsing, so a bad directory name reports the naming rule rather
// than a missing SKILL.md.
func (dshSkillCodec) checkName(name string) error {
	if !skill.ValidName(name) {
		return fmt.Errorf("skill name %q is not kebab-case; DSH reads [a-z0-9]+(-[a-z0-9]+)*", name)
	}

	return nil
}

// toHost validates one canon skill tree for DSH and returns it unchanged: the
// DSH frontmatter dialect is the canon dialect, so only readability is
// enforced.
func (c dshSkillCodec) toHost(name string, tree skill.Tree) (skill.Tree, error) {
	if err := c.checkName(name); err != nil {
		return nil, err
	}

	root, ok := tree[skill.FileName]
	if !ok {
		return nil, fmt.Errorf("skill %s has no %s", name, skill.FileName)
	}

	front, _, ok := frontmatter.Split(root)
	if !ok {
		return nil, fmt.Errorf("skill %s: %s needs YAML frontmatter with name and description; DSH ignores a skill without it",
			name, skill.FileName)
	}

	if !dshClosingFence(root) {
		return nil, fmt.Errorf("skill %s: the %s frontmatter fence must be exactly \"---\"; DSH ignores a skill whose closing fence carries trailing whitespace",
			name, skill.FileName)
	}

	var raw map[string]any

	if err := yaml.Unmarshal(front, &raw); err != nil {
		return nil, fmt.Errorf("skill %s: parse frontmatter: %w", name, err)
	}

	if err := dshSkillName(raw, name); err != nil {
		return nil, err
	}

	if err := dshSkillDescription(raw, name); err != nil {
		return nil, err
	}

	if err := dshInvocation(raw, name); err != nil {
		return nil, err
	}

	return tree, nil
}

// dshClosingFence reports whether the first frontmatter-closing candidate
// after the opening fence is exactly "---". frontmatter.Split tolerates
// trailing spaces on the fence while DSH requires the exact line, so a fence
// Split accepts could still make DSH ignore the whole skill.
func dshClosingFence(data []byte) bool {
	lines := strings.Split(string(data), "\n")

	for _, line := range lines[1:] {
		line = strings.TrimSuffix(line, "\r")

		if strings.TrimRight(line, " \t") == "---" {
			return line == "---"
		}
	}

	return false
}

// dshSkillName checks the required frontmatter name: DSH uses it as the skill
// identity, so it must be present and match the canon name, or the host would
// expose the skill under a different name than the directory it lives in.
func dshSkillName(raw map[string]any, name string) error {
	value, ok := raw["name"]
	if !ok {
		return fmt.Errorf("skill %s: frontmatter requires a name; DSH ignores a skill without one", name)
	}

	text, ok := value.(string)
	if !ok || text == "" {
		return fmt.Errorf("skill %s: frontmatter name must be a non-empty string", name)
	}

	if !skill.ValidName(text) {
		return fmt.Errorf("skill %s: frontmatter name %q is not kebab-case; DSH reads [a-z0-9]+(-[a-z0-9]+)*", name, text)
	}

	if text != name {
		return fmt.Errorf("skill %s: frontmatter name %q does not match the skill directory; DSH would expose it as %q",
			name, text, text)
	}

	return nil
}

// dshSkillDescription checks the required description.
func dshSkillDescription(raw map[string]any, name string) error {
	value, ok := raw["description"]
	if !ok {
		return fmt.Errorf("skill %s: frontmatter requires a description; DSH ignores a skill without one", name)
	}

	text, ok := value.(string)
	if !ok || text == "" {
		return fmt.Errorf("skill %s: frontmatter description must be a non-empty string", name)
	}

	return nil
}

// dshInvocation checks the invocation pair: DSH drops the whole skill when a
// value is not a boolean or when a legacy camelCase key is present.
func dshInvocation(raw map[string]any, name string) error {
	for _, key := range dshInvocationKeys {
		value, ok := raw[key]
		if !ok {
			continue
		}

		if _, ok := dshSkillBool(value); !ok {
			return fmt.Errorf(
				"skill %s: frontmatter %s must be a boolean (true/false, yes/no, on/off, 1/0); DSH drops a skill with any other value",
				name, key)
		}
	}

	for _, key := range dshLegacyInvocationKeys {
		if _, ok := raw[key.legacy]; ok {
			return fmt.Errorf("skill %s: frontmatter %s is rejected by DSH; %s", name, key.legacy, key.hint)
		}
	}

	return nil
}

// dshSkillBool decodes the strict boolean grammar DSH accepts for skill
// frontmatter: YAML booleans, 1/0, and the case-insensitive true/false,
// yes/no, on/off strings.
func dshSkillBool(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case int:
		switch typed {
		case 1:
			return true, true
		case 0:
			return false, true
		}
	case string:
		switch strings.ToLower(typed) {
		case "true", "yes", "on", "1":
			return true, true
		case "false", "no", "off", "0":
			return false, true
		}
	}

	return false, false
}
