package agent

import (
	"fmt"

	yaml "go.yaml.in/yaml/v3"

	"github.com/odiumuniverse/beadle/pkg/frontmatter"
	"github.com/odiumuniverse/beadle/pkg/skill"
)

// ompSkillCodec checks one canon skill tree against the omp native provider
// dialect. omp discovers skills one level under skills/ and requires a
// non-empty description for its native (.omp) provider: a skill without one
// is dropped from the catalog. Every other key — including the invocation
// switch in either spelling, disable-model-invocation and
// disableModelInvocation — is accepted, and unknown keys are preserved as
// metadata (live-verified against omp 18.4.1 with `omp read skill://<name>`).
// The codec therefore validates instead of rewriting: a tree that passes
// travels byte-for-byte, so the canon and the host file stay identical and
// repeat syncs are no-ops.
type ompSkillCodec struct{}

// toHost validates one canon skill tree for omp and returns it unchanged.
func (c ompSkillCodec) toHost(name string, tree skill.Tree) (skill.Tree, error) {
	if !skill.ValidName(name) {
		return nil, fmt.Errorf(
			"skill name %q is not kebab-case; beadle and the omp skill readers agree on [a-z0-9]+(-[a-z0-9]+)*", name)
	}

	root, ok := tree[skill.FileName]
	if !ok {
		return nil, fmt.Errorf("skill %s has no %s", name, skill.FileName)
	}

	front, _, ok := frontmatter.Split(root)
	if !ok {
		return nil, fmt.Errorf(
			"skill %s: %s needs YAML frontmatter with a description; the omp native provider drops a skill without one",
			name, skill.FileName)
	}

	var raw map[string]any

	if err := yaml.Unmarshal(front, &raw); err != nil {
		return nil, fmt.Errorf("skill %s: parse frontmatter: %w", name, err)
	}

	if err := ompSkillDescription(raw, name); err != nil {
		return nil, err
	}

	if err := ompSkillName(raw, name); err != nil {
		return nil, err
	}

	return tree, nil
}

// ompSkillDescription checks the required description: the native provider
// discovers a directory skill only when its frontmatter carries a non-empty
// description.
func ompSkillDescription(raw map[string]any, name string) error {
	value, ok := raw["description"]
	if !ok {
		return fmt.Errorf("skill %s: frontmatter requires a description; the omp native provider drops a skill without one", name)
	}

	text, ok := value.(string)
	if !ok || text == "" {
		return fmt.Errorf("skill %s: frontmatter description must be a non-empty string", name)
	}

	return nil
}

// ompSkillName checks the optional frontmatter name. omp defaults the skill
// name to its directory and otherwise exposes it under the frontmatter name,
// so a name that is missing is fine while a mismatched or non-kebab one would
// change the skill identity beadle synced.
func ompSkillName(raw map[string]any, name string) error {
	value, ok := raw["name"]
	if !ok {
		return nil
	}

	text, ok := value.(string)
	if !ok || text == "" {
		return fmt.Errorf("skill %s: frontmatter name must be a non-empty string", name)
	}

	if !skill.ValidName(text) {
		return fmt.Errorf("skill %s: frontmatter name %q is not kebab-case; omp would expose it under that name", name, text)
	}

	if text != name {
		return fmt.Errorf("skill %s: frontmatter name %q does not match the skill directory; omp would expose it as %q",
			name, text, text)
	}

	return nil
}
