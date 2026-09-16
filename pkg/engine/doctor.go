package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/odiumuniverse/agents-sync/pkg/agent"
	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
	"github.com/odiumuniverse/agents-sync/pkg/plugin"
	"github.com/odiumuniverse/agents-sync/pkg/state"
)

const (
	SeverityError = "error"
	SeverityWarn  = "warn"
	SeverityInfo  = "info"
)

const maxObjectIssues = 5

type Issue struct {
	Severity string  `json:"severity"`
	Kind     kind.ID `json:"kind,omitempty"`
	Agent    string  `json:"agent,omitempty"`
	Message  string  `json:"message"`
}

func (e *Engine) Doctor(ctx context.Context) ([]Issue, error) {
	issues := e.checkVault()

	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		return append(issues, Issue{Severity: SeverityError, Message: err.Error()}), nil //nolint:nilerr // a damaged state is a finding, not a reason to stop diagnosing
	}

	issues = append(issues, e.checkObjects(st)...)
	issues = append(issues, conflictIssues(st)...)

	active, err := e.activeAgents(ctx)
	if err != nil {
		return nil, err
	}

	for _, a := range active {
		issues = append(issues, symlinkIssues(a)...)
	}

	plan, err := e.Sync(ctx, SyncOptions{DryRun: true})
	if err != nil {
		issues = append(issues, Issue{Severity: SeverityWarn, Message: "cannot compute pending changes: " + err.Error()})
	} else {
		issues = append(issues, planIssues(plan)...)
	}

	issues = append(issues, e.skillCollisionIssues(active)...)
	issues = append(issues, e.secretIssues()...)
	issues = append(issues, e.projectScopeIssues(ctx, active)...)
	issues = append(issues, e.pluginRefIssues(active)...)
	issues = append(issues, e.pluginPivotIssues(ctx)...)

	return issues, nil
}

func (e *Engine) checkVault() []Issue {
	info, err := os.Stat(e.vault.Root())
	if err != nil {
		return []Issue{{Severity: SeverityError, Message: fmt.Sprintf("vault is not accessible: %v", err)}}
	}

	var issues []Issue

	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		issues = append(issues, Issue{Severity: SeverityWarn, Message: fmt.Sprintf("vault directory has mode %04o, expected 0700", perm)})
	}

	for _, path := range []string{e.vault.ConfigPath(), e.vault.StatePath(), e.vault.SecretsPath()} {
		fileInfo, err := os.Stat(path)
		if errors.Is(err, fs.ErrNotExist) && path != e.vault.ConfigPath() {
			continue
		}

		if err != nil {
			issues = append(issues, Issue{Severity: SeverityError, Message: fmt.Sprintf("%s is not accessible: %v", path, err)})

			continue
		}

		if perm := fileInfo.Mode().Perm(); perm&0o077 != 0 {
			issues = append(issues, Issue{Severity: SeverityWarn, Message: fmt.Sprintf("%s has mode %04o, expected 0600", path, perm)})
		}
	}

	return issues
}

func (e *Engine) checkObjects(st *state.State) []Issue {
	var issues []Issue

	missing := 0

	for _, hash := range st.Hashes() {
		if e.store.Has(hash) {
			continue
		}

		missing++

		if missing <= maxObjectIssues {
			issues = append(issues, Issue{
				Severity: SeverityError,
				Message:  fmt.Sprintf("object %s referenced by the sync state is missing from %s", hash, e.vault.ObjectsDir()),
			})
		}
	}

	if missing > maxObjectIssues {
		issues = append(issues, Issue{Severity: SeverityError, Message: fmt.Sprintf("%d more objects are missing", missing-maxObjectIssues)})
	}

	return issues
}

func conflictIssues(st *state.State) []Issue {
	var issues []Issue

	for _, c := range st.OpenConflicts() {
		issues = append(issues, Issue{
			Severity: SeverityWarn,
			Kind:     c.Kind,
			Agent:    c.Agent,
			Message: fmt.Sprintf("conflict %s: %s differs between the vault and %s (%s); run agent-sync resolve %s",
				c.ID(), c.TargetKey(), c.Agent, c.Reason, c.ID()),
		})
	}

	return issues
}

func planIssues(plan *Report) []Issue {
	var issues []Issue

	for _, kr := range plan.Kinds {
		if kr.Err != "" {
			issues = append(issues, Issue{Severity: SeverityError, Kind: kr.Kind, Message: kr.Err})
		}

		for _, warning := range kr.Warnings {
			issues = append(issues, Issue{Severity: SeverityInfo, Kind: kr.Kind, Message: warning})
		}

		pending := map[string]int{}

		for _, change := range kr.Pulled {
			pending[change.Agent]++
		}

		for _, agentID := range slices.Sorted(maps.Keys(pending)) {
			issues = append(issues, Issue{
				Severity: SeverityWarn, Kind: kr.Kind, Agent: agentID,
				Message: fmt.Sprintf("%d change(s) are not in the vault yet; run agent-sync sync", pending[agentID]),
			})
		}

		for _, result := range kr.Agents {
			if issue, ok := resultIssue(kr.Kind, result); ok {
				issues = append(issues, issue)
			}
		}
	}

	return issues
}

func resultIssue(k kind.ID, result AgentResult) (Issue, bool) {
	issue := Issue{Kind: k, Agent: result.Agent}

	switch result.Action {
	case ActionWouldPush:
		issue.Severity = SeverityWarn
		issue.Message = fmt.Sprintf("differs from the vault in %d item(s); run agent-sync sync", len(result.Changes))
	case ActionError:
		issue.Severity = SeverityError
		issue.Message = result.Note
	case ActionAlias, ActionSkipped:
		issue.Severity = SeverityInfo
		issue.Message = result.Note
	default:
		return Issue{}, false
	}

	return issue, true
}

func symlinkIssues(a *agent.Agent) []Issue {
	var issues []Issue

	for _, surface := range a.Surfaces {
		path := surface.Path()

		info, err := os.Lstat(path)
		if err != nil {
			continue
		}

		if info.Mode()&fs.ModeSymlink != 0 && !fsutil.Exists(path) {
			issues = append(issues, Issue{Severity: SeverityError, Agent: a.ID, Message: "broken symlink: " + path})

			continue
		}

		if !fsutil.Exists(path) || !isDir(path) {
			continue
		}

		entries, err := os.ReadDir(path)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			child := filepath.Join(path, entry.Name())
			if entry.Type()&fs.ModeSymlink != 0 && !fsutil.Exists(child) {
				issues = append(issues, Issue{Severity: SeverityError, Agent: a.ID, Message: "broken symlink: " + child})
			}
		}
	}

	return issues
}

func (e *Engine) skillCollisionIssues(active []*agent.Agent) []Issue {
	byKey := map[string]map[string][]string{}

	add := func(source, dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}

		for _, entry := range entries {
			name := entry.Name()
			key := norm.NFC.String(strings.ToLower(name))

			if byKey[key] == nil {
				byKey[key] = map[string][]string{}
			}

			byKey[key][name] = append(byKey[key][name], source)
		}
	}

	add("vault", e.vault.SkillsDir())

	for _, a := range active {
		if surface := a.Surface(kind.Skills); surface != nil {
			add(a.ID, surface.Path())
		}
	}

	var issues []Issue

	for _, key := range slices.Sorted(maps.Keys(byKey)) {
		names := byKey[key]
		if len(names) < 2 {
			continue
		}

		parts := make([]string, 0, len(names))

		for _, name := range slices.Sorted(maps.Keys(names)) {
			parts = append(parts, name+" ("+strings.Join(names[name], ", ")+")")
		}

		issues = append(issues, Issue{Severity: SeverityError, Kind: kind.Skills, Message: "case or unicode collision: " + strings.Join(parts, " vs ")})
	}

	return issues
}

func (e *Engine) secretIssues() []Issue {
	refs, err := e.secretRefs()
	if err != nil {
		return []Issue{{Severity: SeverityError, Kind: kind.MCP, Message: "read secret references: " + err.Error()}}
	}

	var issues []Issue

	for _, name := range refs {
		if !e.secrets.Has(name) {
			issues = append(issues, Issue{
				Severity: SeverityError, Kind: kind.MCP,
				Message: fmt.Sprintf("secret %s has no value in %s; run agent-sync secrets set %s", name, e.vault.SecretsPath(), name),
			})
		}
	}

	for _, name := range e.secrets.Names() {
		if !slices.Contains(refs, name) {
			issues = append(issues, Issue{Severity: SeverityInfo, Kind: kind.MCP, Message: fmt.Sprintf("secret %s is unused; run agent-sync secrets prune", name)})
		}
	}

	return issues
}

func (e *Engine) projectScopeIssues(ctx context.Context, active []*agent.Agent) []Issue {
	if agent.ByID(active, agent.ClaudeCodeID) == nil {
		return nil
	}

	dir, err := os.Getwd()
	if err != nil {
		return nil
	}

	vaultItems, _, err := e.loadVault(kind.MCP)
	if err != nil {
		return nil
	}

	var issues []Issue

	repo, present, err := agent.ClaudeProjectMCP(ctx, dir)

	switch {
	case err != nil:
		issues = append(issues, projectIssue(SeverityWarn, fmt.Sprintf("cannot read .mcp.json in %s: %v (project scope is not managed by agent-sync)", dir, err)))
	case present:
		issues = append(issues, scopeIssues(".mcp.json", repo, vaultItems)...)
	}

	if e.home == "" {
		return issues
	}

	local, present, err := agent.ClaudeLocalMCP(e.home, dir)

	switch {
	case err != nil:
		issues = append(issues, projectIssue(SeverityWarn, fmt.Sprintf("cannot read ~/.claude.json projects for %s: %v", dir, err)))
	case present:
		issues = append(issues, scopeIssues(fmt.Sprintf("~/.claude.json projects[%q]", dir), local, vaultItems)...)
	}

	return issues
}

func scopeIssues(source string, scope, vaultItems kind.Items) []Issue {
	var (
		issues []Issue
		extra  []string
	)

	for _, name := range scope.Keys() {
		if _, ok := vaultItems[name]; ok {
			issues = append(issues, projectIssue(SeverityWarn, fmt.Sprintf(
				"MCP server %q collides with a vault server via %s: Claude Code prefers the project scope, which agent-sync does not manage",
				name, source)))

			continue
		}

		extra = append(extra, name)
	}

	if len(extra) > 0 {
		issues = append(issues, projectIssue(SeverityInfo, fmt.Sprintf(
			"%s defines MCP servers outside the vault (project scope, not managed by agent-sync): %s",
			source, strings.Join(extra, ", "))))
	}

	return issues
}

func projectIssue(severity, message string) Issue {
	return Issue{Severity: severity, Kind: kind.MCP, Agent: agent.ClaudeCodeID, Message: message}
}

func (e *Engine) pluginRefIssues(active []*agent.Agent) []Issue {
	if e.home == "" {
		return nil
	}

	seen := map[string]bool{}

	var issues []Issue

	for _, a := range active {
		for _, surface := range a.Surfaces {
			realPath := agent.RealPath(surface.Path())
			if seen[realPath] {
				continue
			}

			seen[realPath] = true

			data, err := os.ReadFile(realPath) //nolint:gosec // G304: paths come from the agent definitions
			if err != nil {
				continue
			}

			refs, err := plugin.References(data, e.home)
			if err != nil {
				continue
			}

			for _, ref := range refs {
				if fsutil.Exists(ref.Path) {
					continue
				}

				issues = append(issues, Issue{
					Severity: SeverityError,
					Agent:    a.ID,
					Message: fmt.Sprintf("broken plugin reference: %s (%s %s)",
						displayHomePath(ref.Path, e.home), displayHomePath(realPath, e.home), ref.Pointer),
				})
			}
		}
	}

	return issues
}

func displayHomePath(path, home string) string {
	if home != "" && strings.HasPrefix(path, home+"/") {
		return "~" + strings.TrimPrefix(path, home)
	}

	return path
}

func (e *Engine) pluginPivotIssues(_ context.Context) []Issue {
	if e.home == "" {
		return nil
	}

	manifest, err := plugin.Read(e.home)
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Message: "cannot read the plugin registry: " + err.Error()}}
	}

	ledger, _, err := loadPluginLedger(e.vault.PluginsLedgerPath())
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Message: "cannot read the plugin ledger: " + err.Error()}}
	}

	installed := map[string]plugin.Plugin{}

	for _, group := range groupPlugins(manifest.Plugins) {
		installed[pluginKey(group.Marketplace, group.Name)] = chooseRecord(group.Plugins)
	}

	keys := make(map[string]struct{}, len(installed)+len(ledger.Plugins))

	for key := range installed {
		keys[key] = struct{}{}
	}

	for key := range ledger.Plugins {
		keys[key] = struct{}{}
	}

	var issues []Issue

	for _, key := range slices.Sorted(maps.Keys(keys)) {
		record, isInstalled := installed[key]
		rec, isParked := ledger.Plugins[key]

		issues = append(issues, e.pivotDriftIssues(key, record, isInstalled, rec, isParked)...)
	}

	return issues
}

func (e *Engine) pivotDriftIssues(key string, record plugin.Plugin, installed bool, rec pluginLedgerRec, parked bool) []Issue {
	if issues, final := pivotLifecycleIssues(key, rec); final {
		return issues
	}

	switch {
	case parked && !installed:
		return []Issue{pivotIssue(SeverityWarn, fmt.Sprintf("plugin %s is no longer installed (pivot left in place)", key))}
	case installed && !parked:
		return []Issue{pivotIssue(SeverityWarn, fmt.Sprintf("plugin %s is not parked yet; run agent-sync sync", key))}
	}

	var issues []Issue

	issues = append(issues, pivotTargetIssues(key, record, rec)...)

	pivot := filepath.Join(e.vault.PluginsDir(), record.Marketplace, record.Name, "current")

	link, err := os.Readlink(pivot)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		issues = append(issues, pivotIssue(SeverityWarn, fmt.Sprintf("plugin %s pivot is missing; run agent-sync sync", key)))
	case err != nil:
		issues = append(issues, pivotIssue(SeverityWarn, fmt.Sprintf("plugin %s pivot cannot be read: %v", key, err)))
	case link != rec.Target:
		issues = append(issues, pivotIssue(SeverityWarn, fmt.Sprintf("plugin %s pivot is stale: %s", key, link)))
	}

	return issues
}

func pivotTargetIssues(key string, record plugin.Plugin, rec pluginLedgerRec) []Issue {
	var issues []Issue

	if !fsutil.Exists(rec.Target) {
		issues = append(issues, pivotIssue(SeverityError, fmt.Sprintf("plugin %s pivot target is missing: %s", key, rec.Target)))
	}

	if record.InstallPath != rec.Target && !fsutil.Exists(record.InstallPath) {
		issues = append(issues, pivotIssue(SeverityError, fmt.Sprintf("plugin %s install path is missing: %s", key, record.InstallPath)))
	}

	if rec.Version != record.Version || rec.Sha != record.GitCommitSha || rec.Target != record.InstallPath {
		issues = append(issues, pivotIssue(SeverityWarn, fmt.Sprintf("plugin %s changed %s → %s; run agent-sync sync", key, rec.Version, record.Version)))
	}

	return issues
}

func pivotLifecycleIssues(key string, rec pluginLedgerRec) ([]Issue, bool) {
	switch {
	case !rec.QuarantinedAt.IsZero():
		return []Issue{pivotIssue(SeverityError, fmt.Sprintf("plugin %s@%s is quarantined since %s; run agent-sync heal",
			key, rec.Version, rec.QuarantinedAt.Format(time.RFC3339)))}, true
	case !rec.RetiredAt.IsZero():
		return nil, true
	default:
		return nil, false
	}
}

func pivotIssue(severity, message string) Issue {
	return Issue{Severity: severity, Message: message}
}

func isDir(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.IsDir()
}
