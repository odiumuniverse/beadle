package sync

import (
	"maps"
	"slices"
)

type Mode int

const (
	ModeSync Mode = iota
	ModePull
	ModePush
)

const (
	ResourceRules       = "rules"
	ResourceMCP         = "mcp"
	ResourceSkills      = "skills"
	ResourcePermissions = "permissions"
	ResourceVault       = "vault"
)

const sharedSkillsAgent = "shared"

const (
	ActionPushed  = "pushed"
	ActionNoop    = "noop"
	ActionSkipped = "skipped"
	ActionError   = "error"
)

type Report struct {
	Mode           Mode
	Rules          ResourceReport
	MCP            ResourceReport
	Skills         ResourceReport
	Permissions    ResourceReport
	Actions        map[string]AgentActions
	MissingSecrets map[string][]string
}

func (r *Report) addMissingSecrets(agent string, names []string) {
	if r.MissingSecrets == nil {
		r.MissingSecrets = map[string][]string{}
	}

	r.MissingSecrets[agent] = names
}

func (r Report) MissingSecretNames() []string {
	if len(r.MissingSecrets) == 0 {
		return nil
	}

	seen := make(map[string]struct{})

	for _, names := range r.MissingSecrets {
		for _, name := range names {
			seen[name] = struct{}{}
		}
	}

	return slices.Sorted(maps.Keys(seen))
}

type ResourceReport struct {
	Changed   bool
	Conflicts []AgentConflict
}

type AgentConflict struct {
	Agent string `json:"agent"`
	Count int    `json:"count"`
}

type AgentActions struct {
	Rules       string `json:"rules,omitempty"`
	MCP         string `json:"mcp,omitempty"`
	Skills      string `json:"skills,omitempty"`
	Permissions string `json:"permissions,omitempty"`
}

func (r *ResourceReport) addConflict(agent string, count int) {
	r.Conflicts = append(r.Conflicts, AgentConflict{Agent: agent, Count: count})
}

func (r ResourceReport) conflictedAgent(agent string) bool {
	for _, c := range r.Conflicts {
		if c.Agent == agent {
			return true
		}
	}

	return false
}

func (r ResourceReport) ConflictCount() int {
	total := 0

	for _, c := range r.Conflicts {
		total += c.Count
	}

	return total
}
