package engine

import (
	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
	"github.com/odiumuniverse/agents-sync/pkg/state"
)

type Action string

const (
	ActionNoop      Action = "noop"
	ActionPushed    Action = "pushed"
	ActionWouldPush Action = "would-push"
	ActionSkipped   Action = "skipped"
	ActionError     Action = "error"
	ActionAlias     Action = "alias"
	ActionPullOnly  Action = "pull-only"
)

const (
	OpAdded    = "added"
	OpModified = "modified"
	OpDeleted  = "deleted"
)

type Change struct {
	Agent string `json:"agent"`
	Key   string `json:"key"`
	Op    string `json:"op"`
}

type ItemChange struct {
	Key    string `json:"key"`
	Op     string `json:"op"`
	Before []byte `json:"-"`
	After  []byte `json:"-"`
}

type AgentResult struct {
	Agent      string       `json:"agent"`
	Mode       config.Mode  `json:"mode"`
	Action     Action       `json:"action"`
	Changes    []ItemChange `json:"changes,omitempty"`
	Note       string       `json:"note,omitempty"`
	ReloadHint string       `json:"reload_hint,omitempty"`
}

type KindReport struct {
	Kind         kind.ID       `json:"kind"`
	VaultChanged bool          `json:"vault_changed"`
	Pulled       []Change      `json:"pulled,omitempty"`
	Agents       []AgentResult `json:"agents,omitempty"`
	Inbox        []InboxResult `json:"inbox,omitempty"`
	Warnings     []string      `json:"warnings,omitempty"`
	Err          string        `json:"error,omitempty"`
}

func (r *KindReport) add(result AgentResult) {
	r.Agents = append(r.Agents, result)
}

func (r KindReport) Agent(id string) (AgentResult, bool) {
	for _, result := range r.Agents {
		if result.Agent == id {
			return result, true
		}
	}

	return AgentResult{}, false
}

type Report struct {
	DryRun    bool             `json:"dry_run"`
	Kinds     []KindReport     `json:"kinds"`
	Conflicts []state.Conflict `json:"conflicts,omitempty"`
	Plugins   []PluginResult   `json:"plugins,omitempty"`
	Farm      []FarmResult     `json:"farm,omitempty"`
	Digest    []DigestResult   `json:"digest,omitempty"`
	Warnings  []string         `json:"warnings,omitempty"`
}

func (r *Report) Kind(k kind.ID) *KindReport {
	for i := range r.Kinds {
		if r.Kinds[i].Kind == k {
			return &r.Kinds[i]
		}
	}

	return nil
}

func (r *Report) Action(k kind.ID, agentID string) Action {
	kr := r.Kind(k)
	if kr == nil {
		return ""
	}

	result, ok := kr.Agent(agentID)
	if !ok {
		return ""
	}

	return result.Action
}

func (r *Report) VaultChanged() bool {
	for _, kr := range r.Kinds {
		if kr.VaultChanged {
			return true
		}
	}

	return false
}

func (r *Report) Pushed() bool {
	for _, kr := range r.Kinds {
		for _, result := range kr.Agents {
			if result.Action == ActionPushed || result.Action == ActionWouldPush {
				return true
			}
		}
	}

	return false
}

func (r *Report) ConflictsOf(k kind.ID) []state.Conflict {
	var out []state.Conflict

	for _, c := range r.Conflicts {
		if c.Kind == k {
			out = append(out, c)
		}
	}

	return out
}

func (r *Report) Errors() []string {
	var errs []string

	for _, kr := range r.Kinds {
		if kr.Err != "" {
			errs = append(errs, string(kr.Kind)+": "+kr.Err)
		}

		for _, result := range kr.Agents {
			if result.Action == ActionError {
				errs = append(errs, string(kr.Kind)+"/"+result.Agent+": "+result.Note)
			}
		}
	}

	return errs
}
