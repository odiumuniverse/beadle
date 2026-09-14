package sync

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/odiumuniverse/agents-sync/pkg/adapter"
	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
	"github.com/odiumuniverse/agents-sync/pkg/mcp"
)

const (
	SeverityError = "error"
	SeverityWarn  = "warn"
	SeverityInfo  = "info"
)

type Issue struct {
	Severity string `json:"severity"`
	Resource string `json:"resource,omitempty"`
	Agent    string `json:"agent,omitempty"`
	Message  string `json:"message"`
}

func (e *Engine) Doctor(ctx context.Context) ([]Issue, error) {
	issues := e.checkVault()

	issues = append(issues, e.checkBaseBlobs()...)
	issues = append(issues, e.checkConflicts()...)

	active, snapshots, err := e.exportAll(ctx)
	if err != nil {
		return nil, err
	}

	state, err := e.loadCanon()
	if err != nil {
		return nil, err
	}

	for _, a := range active {
		issues = append(issues, checkSymlinks(a)...)
		issues = append(issues, e.driftIssues(a.ID(), snapshots[a.ID()], state)...)
	}

	issues = append(issues, e.checkSkillNameCollisions(active)...)
	issues = append(issues, e.checkSecrets(state)...)
	issues = append(issues, e.checkProjectScope(active, state)...)

	return issues, nil
}

func (e *Engine) checkProjectScope(active []adapter.Adapter, state *canon) []Issue {
	dir, err := os.Getwd()
	if err != nil {
		return nil
	}

	var claude *adapter.ClaudeCode

	for _, a := range active {
		if c, ok := a.(*adapter.ClaudeCode); ok {
			claude = c

			break
		}
	}

	if claude == nil {
		return nil
	}

	var issues []Issue

	repo, present, err := claude.ProjectMCP(dir)
	if err != nil {
		issues = append(issues, Issue{
			Severity: SeverityWarn,
			Resource: ResourceMCP,
			Agent:    claude.ID(),
			Message:  fmt.Sprintf("cannot read .mcp.json in %s: %v (project scope is not managed by agent-sync)", dir, err),
		})
	} else if present {
		issues = append(issues, projectScopeIssues(".mcp.json", repo, state.servers, claude.ID())...)
	}

	local, present, err := claude.LocalProjectMCP(dir)
	if err != nil {
		issues = append(issues, Issue{
			Severity: SeverityWarn,
			Resource: ResourceMCP,
			Agent:    claude.ID(),
			Message:  fmt.Sprintf("cannot read ~/.claude.json projects for %s: %v (project scope is not managed by agent-sync)", dir, err),
		})
	} else if present {
		issues = append(issues, projectScopeIssues(fmt.Sprintf("~/.claude.json projects[%q]", dir), local, state.servers, claude.ID())...)
	}

	return issues
}

func projectScopeIssues(source string, scope, vault mcp.Servers, agentID string) []Issue {
	var issues []Issue

	var extra []string

	for _, name := range slices.Sorted(maps.Keys(scope)) {
		if _, ok := vault[name]; ok {
			issues = append(issues, Issue{
				Severity: SeverityWarn,
				Resource: ResourceMCP,
				Agent:    agentID,
				Message: fmt.Sprintf(
					"MCP server %q collides with a vault server via %s: Claude Code prefers the project scope, which is not managed by agent-sync",
					name, source,
				),
			})

			continue
		}

		extra = append(extra, name)
	}

	if len(extra) > 0 {
		issues = append(issues, Issue{
			Severity: SeverityInfo,
			Resource: ResourceMCP,
			Agent:    agentID,
			Message: fmt.Sprintf(
				"%s defines MCP servers outside the vault (project scope, read-only, not managed by agent-sync): %s",
				source, strings.Join(extra, ", "),
			),
		})
	}

	return issues
}

func (e *Engine) checkVault() []Issue {
	var issues []Issue

	info, err := os.Stat(e.vault.Root())
	if err != nil {
		return []Issue{{Severity: SeverityError, Resource: ResourceVault, Message: fmt.Sprintf("vault is not accessible: %v", err)}}
	}

	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		issues = append(issues, Issue{
			Severity: SeverityWarn,
			Resource: ResourceVault,
			Message:  fmt.Sprintf("vault directory has mode %04o, expected 0700", perm),
		})
	}

	for _, path := range []string{e.vault.ConfigPath(), e.vault.RegistryPath()} {
		fileInfo, err := os.Stat(path)
		if err != nil {
			issues = append(issues, Issue{
				Severity: SeverityError,
				Resource: ResourceVault,
				Message:  fmt.Sprintf("%s is not accessible: %v", path, err),
			})

			continue
		}

		if perm := fileInfo.Mode().Perm(); perm&0o077 != 0 {
			issues = append(issues, Issue{
				Severity: SeverityWarn,
				Resource: ResourceVault,
				Message:  fmt.Sprintf("%s has mode %04o, expected 0600", path, perm),
			})
		}
	}

	issues = append(issues, e.checkSecretsFile()...)

	return issues
}

func (e *Engine) checkSecretsFile() []Issue {
	path := e.vault.SecretsPath()

	info, err := os.Stat(path)
	if err != nil {
		return nil
	}

	perm := info.Mode().Perm()
	if perm&0o077 == 0 {
		return nil
	}

	return []Issue{{
		Severity: SeverityWarn,
		Resource: ResourceMCP,
		Message:  fmt.Sprintf("%s has mode %04o, expected 0600", path, perm),
	}}
}

func (e *Engine) checkBaseBlobs() []Issue {
	var issues []Issue

	for resource, entry := range e.state.Resources {
		if entry.Base == "" || e.store.Has(entry.Base) {
			continue
		}

		issues = append(issues, Issue{
			Severity: SeverityError,
			Resource: resource,
			Message:  fmt.Sprintf("base blob %s is missing from the objects store; run agent-sync sync", entry.Base),
		})
	}

	return issues
}

func (e *Engine) checkConflicts() []Issue {
	var issues []Issue

	for resource, entry := range e.state.Resources {
		if !entry.Conflict {
			continue
		}

		issues = append(issues, Issue{
			Severity: SeverityWarn,
			Resource: resource,
			Message:  fmt.Sprintf("resource %s has an unresolved conflict; run agent-sync resolve %s", resource, resource),
		})
	}

	return issues
}

func checkSymlinks(a adapter.Adapter) []Issue {
	var issues []Issue

	for _, path := range a.Paths() {
		info, err := os.Lstat(path)
		if err != nil {
			continue
		}

		if info.Mode()&os.ModeSymlink != 0 {
			if !fsutil.Exists(path) {
				issues = append(issues, Issue{
					Severity: SeverityError,
					Agent:    a.ID(),
					Message:  "broken symlink: " + path,
				})
			}

			continue
		}

		if !info.IsDir() {
			continue
		}

		issues = append(issues, checkDirSymlinks(a.ID(), path)...)
	}

	return issues
}

func checkDirSymlinks(agentID, dir string) []Issue {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var issues []Issue

	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink == 0 {
			continue
		}

		target := filepath.Join(dir, entry.Name())
		if !fsutil.Exists(target) {
			issues = append(issues, Issue{
				Severity: SeverityError,
				Agent:    agentID,
				Message:  "broken symlink: " + target,
			})
		}
	}

	return issues
}

func (e *Engine) driftIssues(agentID string, snap adapter.Snapshot, state *canon) []Issue {
	a := e.adapterByID(agentID)

	var issues []Issue

	if a != nil && a.RulesPush() && !bytes.Equal(snap.Rules, state.rules) {
		issues = append(issues, driftIssue(agentID, ResourceRules, "rules differ from the vault; run agent-sync sync"))
	}

	if snap.MCPPresent && !serversEqual(snap.MCP, state.servers) {
		issues = append(issues, driftIssue(agentID, ResourceMCP, "MCP servers differ from the vault; run agent-sync sync"))
	}

	if skillsDiffer(a, snap, state) {
		issues = append(issues, driftIssue(agentID, ResourceSkills, "skills differ from the vault; run agent-sync sync"))
	}

	if e.permissionsEnabled() && snap.PermissionsPresent && !rulesEqual(snap.Permissions, state.permissions) {
		issues = append(issues, driftIssue(agentID, ResourcePermissions, "permissions differ from the vault; run agent-sync sync"))
	}

	return issues
}

func driftIssue(agentID, resource, message string) Issue {
	return Issue{Severity: SeverityWarn, Resource: resource, Agent: agentID, Message: message}
}

func skillsDiffer(a adapter.Adapter, snap adapter.Snapshot, state *canon) bool {
	if snap.Skills != nil {
		return !skillMapsEqual(snap.Skills, state.skills)
	}

	return a != nil && a.SkillsPush() && len(state.skills) > 0
}

func (e *Engine) checkSkillNameCollisions(active []adapter.Adapter) []Issue {
	byKey := map[string]map[string][]string{}

	add := func(source, dir string) {
		names, err := skillNames(dir)
		if err != nil {
			return
		}

		for _, name := range names {
			key := norm.NFC.String(strings.ToLower(name))

			if byKey[key] == nil {
				byKey[key] = map[string][]string{}
			}

			byKey[key][name] = append(byKey[key][name], source)
		}
	}

	add(ResourceVault, filepath.Join(e.vault.Root(), "skills"))

	if e.sharedSkillsDir != "" {
		add("shared", e.sharedSkillsDir)
	}

	for _, a := range active {
		add(a.ID(), a.SkillsDir())
	}

	var issues []Issue

	for _, names := range byKey {
		if len(names) < 2 {
			continue
		}

		issues = append(issues, Issue{
			Severity: SeverityError,
			Resource: ResourceSkills,
			Message:  "case or unicode collision: " + formatCollision(names),
		})
	}

	return issues
}

func formatCollision(names map[string][]string) string {
	parts := make([]string, 0, len(names))

	for name, sources := range names {
		parts = append(parts, name+" ("+strings.Join(sources, ", ")+")")
	}

	return strings.Join(parts, " vs ")
}

func skillNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(entries))

	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}

	return names, nil
}
