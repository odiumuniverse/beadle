package sync

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
	Mode        Mode
	Rules       ResourceReport
	MCP         ResourceReport
	Skills      ResourceReport
	Permissions ResourceReport
	Actions     map[string]AgentActions
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
