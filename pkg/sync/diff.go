package sync

import (
	"context"
	"maps"
	"reflect"
	"slices"

	"github.com/pmezard/go-difflib/difflib"

	"github.com/odiumuniverse/agents-sync/pkg/mcp"
	"github.com/odiumuniverse/agents-sync/pkg/skill"
)

const (
	StatusAdded   = "added"
	StatusRemoved = "removed"
	StatusChanged = "changed"
)

type DiffReport struct {
	Rules       map[string]string
	MCP         map[string][]ServerChange
	Skills      map[string][]SkillChange
	Permissions map[string][]RuleChange
}

type ServerChange struct {
	Server string
	Status string
}

type SkillChange struct {
	Skill  string
	Status string
}

type RuleChange struct {
	Key    string
	Status string
}

func (e *Engine) Diff(ctx context.Context) (DiffReport, error) {
	active, err := e.activeAdapters()
	if err != nil {
		return DiffReport{}, err
	}

	state, err := e.loadCanon()
	if err != nil {
		return DiffReport{}, err
	}

	report := DiffReport{
		Rules:       map[string]string{},
		MCP:         map[string][]ServerChange{},
		Skills:      map[string][]SkillChange{},
		Permissions: map[string][]RuleChange{},
	}

	for _, a := range active {
		snapshot, err := a.Export(ctx)
		if err != nil {
			return DiffReport{}, err
		}

		if a.RulesPush() {
			report.Rules[a.ID()] = rulesDiff(a.ID(), string(snapshot.Rules), string(state.rules))
		}

		if snapshot.MCPPresent {
			report.MCP[a.ID()] = mcpDiff(snapshot.MCP, state.servers)
		}

		if snapshot.Skills != nil {
			report.Skills[a.ID()] = skillDiff(snapshot.Skills, state.skills)
		}

		if e.permissionsEnabled() && snapshot.PermissionsPresent {
			report.Permissions[a.ID()] = permissionDiff(snapshot.Permissions, state.permissions)
		}
	}

	return report, nil
}

func rulesDiff(agent, agentRules, vaultRules string) string {
	if agentRules == vaultRules {
		return ""
	}

	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(agentRules),
		B:        difflib.SplitLines(vaultRules),
		FromFile: "agent:" + agent,
		ToFile:   ResourceVault,
		Context:  3,
	})
	if err != nil {
		return ""
	}

	return diff
}

func mcpDiff(agent, vault mcp.Servers) []ServerChange {
	changes := make([]ServerChange, 0)

	for _, name := range serverNames(union(agent, vault)) {
		agentServer, agentOK := agent[name]
		vaultServer, vaultOK := vault[name]

		switch {
		case agentOK && !vaultOK:
			changes = append(changes, ServerChange{Server: name, Status: StatusAdded})
		case !agentOK && vaultOK:
			changes = append(changes, ServerChange{Server: name, Status: StatusRemoved})
		case !reflect.DeepEqual(agentServer, vaultServer):
			changes = append(changes, ServerChange{Server: name, Status: StatusChanged})
		}
	}

	return changes
}

func skillDiff(agent, vault map[string]skill.Tree) []SkillChange {
	changes := make([]SkillChange, 0)

	for _, name := range unionSkillNames(agent, vault) {
		agentTree, agentOK := agent[name]
		vaultTree, vaultOK := vault[name]

		switch {
		case agentOK && !vaultOK:
			changes = append(changes, SkillChange{Skill: name, Status: StatusAdded})
		case !agentOK && vaultOK:
			changes = append(changes, SkillChange{Skill: name, Status: StatusRemoved})
		case !treesEqual(agentTree, vaultTree):
			changes = append(changes, SkillChange{Skill: name, Status: StatusChanged})
		}
	}

	return changes
}

func union(a, b mcp.Servers) mcp.Servers {
	out := make(mcp.Servers, len(a)+len(b))
	maps.Copy(out, a)
	maps.Copy(out, b)

	return out
}

func unionSkillNames(a, b map[string]skill.Tree) []string {
	seen := make(map[string]struct{}, len(a)+len(b))

	for _, m := range []map[string]skill.Tree{a, b} {
		for name := range m {
			seen[name] = struct{}{}
		}
	}

	return slices.Sorted(maps.Keys(seen))
}
