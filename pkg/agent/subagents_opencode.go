package agent

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/odiumuniverse/beadle/pkg/permission"
	"github.com/odiumuniverse/beadle/pkg/subagent"
)

// ocPermissionRule is one entry of the OpenCode v2 permissions array.
type ocPermissionRule struct {
	Action   string `yaml:"action"`
	Resource string `yaml:"resource"`
	Effect   string `yaml:"effect"`
}

// Canonical tool names, shared by the OpenCode tables.
const (
	toolRead            = "Read"
	toolWrite           = "Write"
	toolEdit            = "Edit"
	toolNotebookEdit    = "NotebookEdit"
	toolBash            = "Bash"
	toolGrep            = "Grep"
	toolGlob            = "Glob"
	toolWebFetch        = "WebFetch"
	toolWebSearch       = "WebSearch"
	toolTask            = "Task"
	toolAgent           = "Agent"
	toolSkill           = "Skill"
	toolAskUserQuestion = "AskUserQuestion"
)

// OpenCode v2 action names.
const (
	ocRead      = "read"
	ocEdit      = "edit"
	ocShell     = "shell"
	ocGrep      = "grep"
	ocGlob      = "glob"
	ocWebFetch  = "webfetch"
	ocWebSearch = "websearch"
	ocSubagent  = "subagent"
	ocSkill     = "skill"
	ocQuestion  = "question"
)

// ocToolActions maps canonical tool names to OpenCode v2 actions.
var ocToolActions = map[string]string{
	toolRead:            ocRead,
	toolWrite:           ocEdit,
	toolEdit:            ocEdit,
	toolNotebookEdit:    ocEdit,
	toolBash:            ocShell,
	toolGrep:            ocGrep,
	toolGlob:            ocGlob,
	toolWebFetch:        ocWebFetch,
	toolWebSearch:       ocWebSearch,
	toolTask:            ocSubagent,
	toolAgent:           ocSubagent,
	toolSkill:           ocSkill,
	toolAskUserQuestion: ocQuestion,
}

// ocActionTools maps OpenCode v2 actions back to canonical tool names. The
// write class expands to every name it covers so a push maps them back to
// the same action.
var ocActionTools = map[string][]string{
	ocRead:      {toolRead},
	ocEdit:      {toolWrite, toolEdit, toolNotebookEdit},
	ocShell:     {toolBash},
	ocGrep:      {toolGrep},
	ocGlob:      {toolGlob},
	ocWebFetch:  {toolWebFetch},
	ocWebSearch: {toolWebSearch},
	ocSubagent:  {toolTask},
	ocSkill:     {toolSkill},
	ocQuestion:  {toolAskUserQuestion},
}

// ocV1Actions maps OpenCode v1 tool keys to the v2 action names.
var ocV1Actions = map[string]string{
	"write": ocEdit,
	"patch": ocEdit,
	"bash":  ocShell,
	"task":  ocSubagent,
}

// ocWriteClass lists the canonical tools that the edit action covers.
var ocWriteClass = []string{toolWrite, toolEdit, toolNotebookEdit}

// ocToolOrder lists canonical tools in render order so parsed lists are
// stable across reads.
var ocToolOrder = []string{
	toolRead, toolWrite, toolEdit, toolNotebookEdit, toolBash, toolGrep,
	toolGlob, toolWebFetch, toolWebSearch, toolTask, toolAgent, toolSkill,
	toolAskUserQuestion,
}

// ocToolRank ranks canonical tools in render order.
var ocToolRank = func() map[string]int {
	rank := make(map[string]int, len(ocToolOrder))

	for i, tool := range ocToolOrder {
		rank[tool] = i
	}

	return rank
}()

// ocModes lists the mode values OpenCode accepts.
var ocModes = []string{subagentLabel, primaryMode, allMode}

// ocV1Keys lists the legacy OpenCode v1 keys. Any key outside the v2 set
// switches the host to its legacy decoder, which ignores v2 permissions, so
// a write must remove them.
var ocV1Keys = []string{promptKey, "permission", "temperature", "top_p", "disable", "maxSteps", "options"}

// ocV2Keys lists every key of the OpenCode v2 subagent schema. Anything else
// makes the host fall back to its legacy decoder.
var ocV2Keys = []string{
	"description", "mode", "model", "hidden", "color", "steps", "permissions",
	"variant", "request", "system", "disabled",
}

var (
	ocColorRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	ocModelRE = regexp.MustCompile(`^[^/#]+/[^#]+(?:#[^#]+)?$`)
)

// openCodeSubagentCodec reads and writes OpenCode subagent files. OpenCode
// derives the agent id from the file name and rejects unknown frontmatter
// keys by falling back to its legacy decoder, so the codec writes a strict
// allowlist of v2 keys and never writes the canonical name. Kilo Code is an
// OpenCode fork with the same schema and reuses the codec with its own
// directory layout and host label.
type openCodeSubagentCodec struct {
	// host names the host in diagnostics; empty means OpenCode.
	host string
	// primaryDirs lists parent directory names whose files the host treats
	// as primary agents (Kilo's legacy {mode,modes} directories).
	primaryDirs []string
}

// label names the host in diagnostics.
func (c openCodeSubagentCodec) label() string {
	if c.host != "" {
		return c.host
	}

	return "opencode"
}

// ocSubagentFrontmatter is the subset of OpenCode frontmatter the codec reads.
type ocSubagentFrontmatter struct {
	Description string             `yaml:"description"`
	Mode        string             `yaml:"mode"`
	Model       string             `yaml:"model"`
	Steps       int                `yaml:"steps"`
	Color       string             `yaml:"color"`
	Hidden      *bool              `yaml:"hidden"`
	Permissions []ocPermissionRule `yaml:"permissions"`
	Prompt      string             `yaml:"prompt"`
	Tools       map[string]bool    `yaml:"tools"`
	Permission  map[string]any     `yaml:"permission"`
}

func (c openCodeSubagentCodec) parse(path string, data []byte) (subagent.Document, bool, error) {
	front, body, ok := subagent.Split(data)
	if !ok {
		return subagent.Document{}, false, errors.New("no frontmatter; OpenCode subagents need a description and a mode")
	}

	var raw ocSubagentFrontmatter

	if err := yaml.Unmarshal(front, &raw); err != nil {
		return subagent.Document{}, false, fmt.Errorf("parse frontmatter: %w", err)
	}

	doc := subagent.Document{
		Name:        strings.TrimSuffix(filepath.Base(path), ".md"),
		Description: raw.Description,
		Mode:        raw.Mode,
		Model:       raw.Model,
		MaxTurns:    raw.Steps,
		Color:       raw.Color,
		Hidden:      raw.Hidden,
		Body:        body,
	}

	if doc.Body == "" && raw.Prompt != "" {
		doc.Body = raw.Prompt
	}

	// Kilo's legacy {mode,modes} directories hold primary agents.
	if slices.Contains(c.primaryDirs, filepath.Base(filepath.Dir(path))) {
		doc.Mode = primaryMode
	}

	doc.Tools, doc.DisallowedTools = ocToolsFromEffects(ocEffects(raw))

	return doc, true, nil
}

// strayKeys lists frontmatter keys OpenCode does not know.
func (openCodeSubagentCodec) strayKeys(data []byte) []string {
	front, _, ok := subagent.Split(data)
	if !ok {
		return nil
	}

	var raw map[string]any

	if err := yaml.Unmarshal(front, &raw); err != nil {
		return nil
	}

	var out []string

	for key := range raw {
		if !slices.Contains(ocV2Keys, key) {
			out = append(out, key)
		}
	}

	slices.Sort(out)

	return out
}

// ocEffects folds the v2 permissions, the v1 tools map and the v1 permission
// map into one action-to-effect map.
func ocEffects(raw ocSubagentFrontmatter) map[string]string {
	effects := map[string]string{}

	for _, rule := range raw.Permissions {
		if rule.Action == "" || rule.Action == "*" {
			continue
		}

		if rule.Resource != "" && rule.Resource != "*" {
			continue
		}

		effects[rule.Action] = rule.Effect
	}

	// The v1 maps are deltas over the host defaults, not allowlists: an
	// enabled tool or an explicit allow says nothing about the others, so
	// only a deny reaches the canon. Mapping allows would turn every legacy
	// agent into a deny-everything allowlist.
	for action, allowed := range raw.Tools {
		if allowed {
			continue
		}

		action = ocActionName(action)

		if _, seen := effects[action]; !seen {
			effects[action] = permission.EffectDeny
		}
	}

	for action, value := range raw.Permission {
		if effect, ok := value.(string); ok && effect == permission.EffectDeny {
			effects[ocActionName(action)] = effect
		}
	}

	return effects
}

func ocActionName(action string) string {
	if mapped, ok := ocV1Actions[action]; ok {
		return mapped
	}

	return action
}

func ocToolsFromEffects(effects map[string]string) (tools, denied []string) {
	for _, action := range slices.Sorted(maps.Keys(effects)) {
		names := ocActionTools[action]

		if len(names) == 0 {
			mcp, ok := ocMCPToolForAction(action)
			if !ok {
				continue
			}

			names = []string{mcp}
		}

		switch effects[action] {
		case permission.EffectAllow:
			tools = append(tools, names...)
		case permission.EffectDeny:
			denied = append(denied, names...)
		}
	}

	return sortCanonicalTools(tools), sortCanonicalTools(denied)
}

func sortCanonicalTools(tools []string) []string {
	slices.SortFunc(tools, func(a, b string) int {
		ra, oka := ocToolRank[a]
		rb, okb := ocToolRank[b]

		switch {
		case oka && okb:
			return cmp.Compare(ra, rb)
		case oka:
			return -1
		case okb:
			return 1
		default:
			return strings.Compare(a, b)
		}
	})

	return tools
}

func (openCodeSubagentCodec) fields(doc subagent.Document, existing []byte) ([]subagent.Field, string, error) {
	if doc.Name == "" {
		return nil, "", errors.New("subagent name is required")
	}

	fields := make([]subagent.Field, 0, len(subagent.Keys()))
	add := func(key string, value any) {
		fields = append(fields, subagent.Field{Key: key, Value: value})
	}

	if doc.Description != "" {
		add("description", doc.Description)
	}

	mode := doc.Mode
	if !slices.Contains(ocModes, mode) {
		// An unknown mode (or none) would make OpenCode reject the agent;
		// fall back to the safest explicit value.
		mode = subagentLabel
	}

	add("mode", mode)

	if ocModelRE.MatchString(doc.Model) {
		add("model", doc.Model)
	}

	if doc.MaxTurns > 0 {
		add("steps", doc.MaxTurns)
	}

	if doc.Color != "" && ocColorRE.MatchString(doc.Color) {
		add("color", doc.Color)
	}

	if doc.Hidden != nil {
		add("hidden", *doc.Hidden)
	}

	rules, err := ocPermissions(doc, existing)
	if err != nil {
		return nil, "", err
	}

	if len(rules) > 0 {
		add("permissions", rules)
	}

	return fields, doc.Body, nil
}

func (openCodeSubagentCodec) managed() []string {
	out := append(subagent.Keys(), "steps", "permissions")

	return append(out, ocV1Keys...)
}

// ocPermissions renders the permissions array: the rules the canon
// regenerates, followed by the rules the codec must not lose.
func ocPermissions(doc subagent.Document, existing []byte) ([]ocPermissionRule, error) {
	generated := ocGeneratedPermissions(doc)

	preserved, err := ocPreservedPermissions(existing, generated)
	if err != nil {
		return nil, err
	}

	return append(generated, preserved...), nil
}

// ocGeneratedPermissions renders the allowlist envelope, the allowed and
// denied tools, and the derived read-only deny.
func ocGeneratedPermissions(doc subagent.Document) []ocPermissionRule {
	var rules []ocPermissionRule

	allowed := map[string]bool{}

	for _, tool := range doc.Tools {
		action, ok := ocActionForTool(tool)
		if !ok || allowed[action] {
			continue
		}

		allowed[action] = true

		rules = append(rules, ocPermissionRule{Action: action, Resource: "*", Effect: permission.EffectAllow})
	}

	// An allowlist is expressed as "deny everything, then allow the listed
	// actions". An empty allowlist therefore denies every tool, which is the
	// safe projection of an allowlist the host cannot fully express.
	if doc.Tools != nil {
		rules = append([]ocPermissionRule{{Action: "*", Resource: "*", Effect: permission.EffectDeny}}, rules...)
	}

	denied := map[string]bool{}

	for _, tool := range doc.DisallowedTools {
		if action, ok := ocActionForTool(tool); ok {
			denied[action] = true
		}
	}

	if ocReadOnly(doc) {
		denied[ocEdit] = true
	}

	for _, action := range slices.Sorted(maps.Keys(denied)) {
		if allowed[action] {
			continue
		}

		rules = append(rules, ocPermissionRule{Action: action, Resource: "*", Effect: permission.EffectDeny})
	}

	return rules
}

// ocActionForTool maps a canonical tool name to the OpenCode action name:
// plain tools use the table, MCP tools follow the pull-side convention
// mcp__<server>__<tool> → <server>_<tool>. A name whose server does not
// round-trip through splitOpenCodeMCPKey has no OpenCode form and is
// dropped.
func ocActionForTool(tool string) (string, bool) {
	if action, ok := ocToolActions[tool]; ok {
		return action, true
	}

	rest, ok := strings.CutPrefix(tool, "mcp__")
	if !ok {
		return "", false
	}

	server, name, ok := strings.Cut(rest, "__")
	if !ok || server == "" || name == "" {
		return "", false
	}

	action := server + "_" + name

	if back, ok := ocMCPToolForAction(action); !ok || back != tool {
		return "", false
	}

	return action, true
}

// ocMCPToolForAction maps an OpenCode MCP action name back to the canonical
// mcp__<server>__<tool> form.
func ocMCPToolForAction(action string) (string, bool) {
	server, name, ok := splitOpenCodeMCPKey(action)
	if !ok {
		return "", false
	}

	return "mcp__" + server + "__" + name, true
}

// ocPreservedPermissions keeps the rules the codec does not regenerate: the
// legacy v1 permission map (converted to v2 rules) and every v2 rule that is
// not one of the generated ones and that the canon does not own. They are
// appended after the managed rules, so they keep the last-match-wins
// precedence.
func ocPreservedPermissions(existing []byte, generated []ocPermissionRule) ([]ocPermissionRule, error) {
	if len(existing) == 0 {
		return nil, nil
	}

	front, _, ok := subagent.Split(existing)
	if !ok {
		return nil, nil
	}

	var raw struct {
		Permissions []ocPermissionRule `yaml:"permissions"`
		Permission  map[string]any     `yaml:"permission"`
	}

	if err := yaml.Unmarshal(front, &raw); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}

	legacy := ocLegacyPermissionRules(raw.Permission)

	var out []ocPermissionRule

	for _, rule := range raw.Permissions {
		if slices.Contains(generated, rule) || slices.Contains(legacy, rule) {
			continue
		}

		if ocPreserveRule(rule) {
			out = append(out, rule)
		}
	}

	return append(legacy, out...), nil
}

// ocLegacyPermissionRules converts the v1 permission map into v2 rules for
// the entries the canonical model cannot carry: resource-scoped patterns and
// ask effects.
func ocLegacyPermissionRules(rules map[string]any) []ocPermissionRule {
	var out []ocPermissionRule

	for action, value := range rules {
		action = ocActionName(action)

		switch typed := value.(type) {
		case string:
			if typed == permission.EffectAsk {
				out = append(out, ocPermissionRule{Action: action, Resource: "*", Effect: typed})
			}
		case map[string]any:
			for pattern, effect := range typed {
				if name, ok := effect.(string); ok {
					out = append(out, ocPermissionRule{Action: action, Resource: pattern, Effect: name})
				}
			}
		}
	}

	slices.SortFunc(out, func(a, b ocPermissionRule) int {
		return cmp.Or(cmp.Compare(a.Action, b.Action), cmp.Compare(a.Resource, b.Resource), cmp.Compare(a.Effect, b.Effect))
	})

	return out
}

// ocPreserveRule reports whether an existing v2 rule must survive a write.
// The codec owns the action-level allow/deny rules of known actions: the
// parse side lifts them into the canon, so the canon decides their fate, and
// the envelope is a codec artifact that is always regenerated. Only the
// host's own policy stays: resource-scoped patterns, unknown actions and ask
// effects.
func ocPreserveRule(rule ocPermissionRule) bool {
	if rule.Action == "" || rule.Action == "*" {
		return false
	}

	if rule.Resource != "" && rule.Resource != "*" {
		return true
	}

	if _, managed := ocActionTools[ocActionName(rule.Action)]; !managed {
		if _, mcp := ocMCPToolForAction(rule.Action); !mcp {
			return true
		}
	}

	return rule.Effect == permission.EffectAsk
}

// ocReadOnly reports the derived read-only state: no write-class tool is
// available to the agent.
func ocReadOnly(doc subagent.Document) bool {
	if doc.Tools != nil {
		for _, tool := range doc.Tools {
			if slices.Contains(ocWriteClass, tool) {
				return false
			}
		}

		return true
	}

	for _, tool := range ocWriteClass {
		if !slices.Contains(doc.DisallowedTools, tool) {
			return false
		}
	}

	return len(doc.DisallowedTools) > 0
}

// audit lists the canonical tools OpenCode cannot express and the model
// values that fall back to the host default.
func (c openCodeSubagentCodec) audit(doc subagent.Document) []string {
	var notes []string

	for _, tool := range append(slices.Clone(doc.Tools), doc.DisallowedTools...) {
		if _, _, mcp := splitCanonicalMCP(tool); mcp {
			if _, ok := ocActionForTool(tool); !ok {
				notes = append(notes, fmt.Sprintf(
					"MCP tool %q has no %s form: server names cannot contain underscores", tool, c.label()))
			}

			continue
		}

		if _, ok := ocActionForTool(tool); !ok {
			notes = append(notes, fmt.Sprintf("unmappable tool %q for %s; skipped", tool, c.label()))
		}
	}

	if doc.Model != "" && !ocModelRE.MatchString(doc.Model) {
		notes = append(notes, fmt.Sprintf("model %q does not map to %s; the host keeps its model", doc.Model, c.label()))
	}

	return dedupStrings(notes)
}
