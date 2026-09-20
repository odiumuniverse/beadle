package engine

import (
	"bytes"
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

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/digest"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/plugin"
	proj "github.com/odiumuniverse/beadle/pkg/project"
	"github.com/odiumuniverse/beadle/pkg/secret"
	"github.com/odiumuniverse/beadle/pkg/skill"
	"github.com/odiumuniverse/beadle/pkg/state"
)

const (
	SeverityError = "error"
	SeverityWarn  = "warn"
	SeverityInfo  = "info"
)

const maxObjectIssues = 5

var hostFSType = fsutil.FSType

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
	issues = append(issues, refusalIssues(st, e.now())...)

	active, err := e.activeAgents(ctx)
	if err != nil {
		return nil, err
	}

	ledger, _, _ := loadPluginLedger(e.vault.PluginsLedgerPath())
	if ledger.Plugins == nil {
		ledger = emptyPluginLedger()
	}

	for _, a := range active {
		issues = append(issues, e.symlinkIssues(a, ledger)...)
	}

	plan, err := e.Sync(ctx, SyncOptions{DryRun: true})
	if err != nil {
		issues = append(issues, Issue{Severity: SeverityWarn, Message: "cannot compute pending changes: " + err.Error()})
	} else {
		issues = append(issues, planIssues(plan)...)
	}

	issues = append(issues, e.skillCollisionIssues(active)...)
	issues = append(issues, e.skillShadowIssues(active)...)
	issues = append(issues, e.secretIssues()...)
	issues = append(issues, e.projectScopeIssues(ctx, active)...)
	issues = append(issues, e.projectPolicyIssues(active)...)
	issues = append(issues, e.pluginRefIssues(active)...)
	issues = append(issues, e.pluginPivotIssues(ctx)...)
	issues = append(issues, e.pluginPinIssues(active)...)
	issues = append(issues, e.pluginMigrationIssues(ctx, ledger)...)
	issues = append(issues, e.farmPresentationIssues(active, ledger)...)
	issues = append(issues, e.bundleIssues()...)
	issues = append(issues, e.digestIssues(active, st)...)
	issues = append(issues, e.memorySecretIssues()...)

	return issues, nil
}

func (e *Engine) memorySecretIssues() []Issue {
	items, _, err := loadNotesDir(e.vault.MemoryDir())
	if err != nil {
		return []Issue{{Severity: SeverityError, Kind: kind.Memory, Message: "read memory canon: " + err.Error()}}
	}

	var (
		issues     []Issue
		refs       int
		notes      int
		plaintexts []string
	)

	for _, key := range slices.Sorted(maps.Keys(items)) {
		data := items[key]

		if len(secret.ScanText(data)) > 0 {
			plaintexts = append(plaintexts, key)
		}

		if found := len(secret.RefsText(data)); found > 0 {
			refs += found
			notes++
		}
	}

	ignored, err := e.vault.MemoryIgnored()
	if err != nil {
		issues = append(issues, Issue{Severity: SeverityWarn, Kind: kind.Memory, Message: err.Error()})
	}

	if len(plaintexts) > 0 && !ignored {
		issues = append(issues, Issue{
			Severity: SeverityError, Kind: kind.Memory,
			Message: fmt.Sprintf("memory notes with plaintext secrets are not ignored by git: %s; run beadle sync", strings.Join(plaintexts, ", ")),
		})
	}

	if notes > 0 {
		issues = append(issues, Issue{
			Severity: SeverityInfo, Kind: kind.Memory,
			Message: fmt.Sprintf("%d secret reference(s) in %d memory note(s)", refs, notes),
		})
	}

	return issues
}

func (e *Engine) digestIssues(active []*agent.Agent, st *state.State) []Issue {
	if e.home == "" || !e.config.KindEnabled(kind.Projects) {
		return nil
	}

	vaultItems, _, err := e.loadVault(kind.Memory)
	if err != nil {
		return []Issue{{Severity: SeverityError, Kind: kind.Projects, Message: "cannot read the memory canon: " + err.Error()}}
	}

	var issues []Issue

	for _, target := range e.projectTargets(active) {
		issues = append(issues, e.digestTargetIssues(st, target, vaultItems)...)
	}

	return issues
}

func (e *Engine) digestTargetIssues(st *state.State, target projectTarget, vaultItems kind.Items) []Issue {
	if !target.active() {
		return nil
	}

	path := target.path()

	data, present, err := readOptional(path)
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Kind: kind.Projects, Agent: target.agent.ID, Message: err.Error()}}
	}

	notes := digestNotesFor(vaultItems, target.notesSlug)

	if !present {
		return missingDigestIssue(target, path, len(notes) > 0)
	}

	_, fence, found, err := digest.Strip(data)
	if err != nil {
		return []Issue{{Severity: SeverityError, Kind: kind.Projects, Agent: target.agent.ID, Message: "cannot parse the beadle block in " + path}}
	}

	if !found {
		return missingDigestIssue(target, path, len(notes) > 0)
	}

	stored, hasStored := st.Renders[path]

	if hasStored && stored.BlockHash != cas.HashOf(fence) {
		severity := SeverityWarn
		if st.Drift[path].Count >= 2 {
			severity = SeverityError
		}

		return []Issue{{
			Severity: severity, Kind: kind.Projects, Agent: target.agent.ID,
			Message: fmt.Sprintf("manual edits inside the generated block in %s; the digest is frozen: restore the bytes, delete the block, or run beadle sync --refresh-digest", path),
		}}
	}

	if _, ok := digest.Verify(fence); !ok {
		return []Issue{{Severity: SeverityError, Kind: kind.Projects, Agent: target.agent.ID, Message: "cannot parse the beadle block in " + path}}
	}

	if !hasStored {
		return []Issue{{
			Severity: SeverityInfo, Kind: kind.Projects, Agent: target.agent.ID,
			Message: fmt.Sprintf("existing digest in %s is not tracked yet; the next sync adopts it as a baseline", path),
		}}
	}

	return nil
}

func missingDigestIssue(target projectTarget, path string, hasNotes bool) []Issue {
	if !hasNotes {
		return nil
	}

	return []Issue{{
		Severity: SeverityInfo, Kind: kind.Projects, Agent: target.agent.ID,
		Message: fmt.Sprintf("no memory digest in %s; run beadle sync", path),
	}}
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
			Message: fmt.Sprintf("conflict %s: %s differs between the vault and %s (%s); run beadle resolve %s",
				c.ID(), c.TargetKey(), c.Agent, c.Reason, c.ID()),
		})
	}

	return issues
}

func refusalIssues(st *state.State, now time.Time) []Issue {
	last, ok := st.LastRefusal()
	if !ok || now.Sub(last.At) > 24*time.Hour {
		return nil
	}

	return []Issue{{
		Severity: SeverityInfo,
		Kind:     last.Kind,
		Agent:    last.Agent,
		Message:  fmt.Sprintf("%d conflict resolution(s) were refused recently (last: %s)", len(st.Refusals), last.Code),
	}}
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
				Message: fmt.Sprintf("%d change(s) are not in the vault yet; run beadle sync", pending[agentID]),
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
		issue.Message = fmt.Sprintf("differs from the vault in %d item(s); run beadle sync", len(result.Changes))
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

func (e *Engine) symlinkIssues(a *agent.Agent, ledger pluginLedger) []Issue {
	var issues []Issue

	for _, surface := range a.Surfaces {
		path := surface.Path()

		info, err := os.Lstat(path)
		if err != nil {
			continue
		}

		if info.Mode()&fs.ModeSymlink != 0 && !fsutil.Exists(path) {
			issues = append(issues, e.brokenSymlinkIssues(a, surface, path, ledger)...)

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
				issues = append(issues, e.brokenSymlinkIssues(a, surface, child, ledger)...)
			}
		}
	}

	return issues
}

func (e *Engine) brokenSymlinkIssues(a *agent.Agent, surface agent.Surface, path string, ledger pluginLedger) []Issue {
	if surface.Kind() == kind.Skills && e.home != "" &&
		e.config.ModeFor(a.ID, kind.Skills, surface.Traits().DefaultMode) != config.ModeOff {
		if link, err := os.Readlink(path); err == nil {
			if _, _, ok := e.cacheLinkKey(link, ledger); ok {
				return nil
			}
		}
	}

	return []Issue{{Severity: SeverityError, Agent: a.ID, Message: "broken symlink: " + path}}
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

func (e *Engine) skillShadowIssues(active []*agent.Agent) []Issue {
	var issues []Issue

	for _, a := range active {
		surface := a.Surface(kind.Skills)
		if surface == nil {
			continue
		}

		reader, ok := surface.(agent.SkillReader)
		if !ok {
			continue
		}

		refs, err := reader.ReadableSkills()
		if err != nil {
			issues = append(issues, Issue{Severity: SeverityWarn, Kind: kind.Skills, Agent: a.ID, Message: err.Error()})

			continue
		}

		issues = append(issues, e.skillShadowIssuesFor(a, refs)...)
	}

	return issues
}

func (e *Engine) skillShadowIssuesFor(a *agent.Agent, refs []agent.SkillRef) []Issue {
	byName := map[string][]agent.SkillRef{}

	for _, ref := range refs {
		byName[ref.Name] = append(byName[ref.Name], ref)
	}

	cache := map[string]skill.Tree{}

	var issues []Issue

	for _, name := range slices.Sorted(maps.Keys(byName)) {
		copies := byName[name]

		for i := range copies {
			for j := i + 1; j < len(copies); j++ {
				if copies[i].Dir == copies[j].Dir {
					continue
				}

				same, err := e.sameSkillTree(cache, copies[i].Root, copies[j].Root)
				if err != nil {
					issues = append(issues, Issue{
						Severity: SeverityWarn, Kind: kind.Skills, Agent: a.ID,
						Message: fmt.Sprintf("cannot compare skill %s: %v", name, err),
					})

					continue
				}

				if same {
					continue
				}

				issues = append(issues, Issue{
					Severity: SeverityWarn, Kind: kind.Skills, Agent: a.ID,
					Message: fmt.Sprintf("skill %s differs between %s and %s: precedence is agent-defined; keep one copy or align the contents",
						name, displayHomePath(copies[i].Dir, e.home), displayHomePath(copies[j].Dir, e.home)),
				})
			}
		}
	}

	return issues
}

func (e *Engine) sameSkillTree(cache map[string]skill.Tree, left, right string) (bool, error) {
	leftTree, err := cachedSkillTree(cache, left)
	if err != nil {
		return false, err
	}

	rightTree, err := cachedSkillTree(cache, right)
	if err != nil {
		return false, err
	}

	return maps.Equal(skill.ManifestOf(leftTree), skill.ManifestOf(rightTree)), nil
}

func cachedSkillTree(cache map[string]skill.Tree, root string) (skill.Tree, error) {
	if tree, ok := cache[root]; ok {
		return tree, nil
	}

	tree, err := skill.ReadTree(root)
	if err != nil {
		return nil, err
	}

	cache[root] = tree

	return tree, nil
}

func (e *Engine) secretIssues() []Issue {
	issues := []Issue{{Severity: SeverityInfo, Message: "secrets backend: " + e.secrets.Backend()}}

	if err := e.secrets.Probe(); err != nil {
		return append(issues, Issue{Severity: SeverityWarn, Message: "keyring unavailable: " + err.Error()})
	}

	refs, err := e.secretRefs()
	if err != nil {
		return append(issues, Issue{Severity: SeverityError, Kind: kind.MCP, Message: "read secret references: " + err.Error()})
	}

	for _, name := range refs {
		if !e.secrets.Has(name) {
			issues = append(issues, Issue{
				Severity: SeverityError,
				Message:  fmt.Sprintf("secret %s has no value in %s; run beadle secrets set %s", name, e.vault.SecretsPath(), name),
			})
		}
	}

	for _, name := range e.secrets.Names() {
		if !slices.Contains(refs, name) {
			issues = append(issues, Issue{Severity: SeverityInfo, Message: fmt.Sprintf("secret %s is unused; run beadle secrets prune", name)})
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

	if !e.projectRelManaged(".mcp.json") {
		repo, present, err := agent.ClaudeProjectMCP(ctx, dir)

		switch {
		case err != nil:
			issues = append(issues, projectIssue(SeverityWarn, fmt.Sprintf("cannot read .mcp.json in %s: %v (project scope is not managed by beadle)", dir, err)))
		case present:
			issues = append(issues, scopeIssues(".mcp.json", repo, vaultItems)...)
		}
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

func (e *Engine) projectRelManaged(rel string) bool {
	policy, err := e.projectPolicy()

	return err == nil && policy.Enabled(rel)
}

func scopeIssues(source string, scope, vaultItems kind.Items) []Issue {
	var (
		issues []Issue
		extra  []string
	)

	for _, name := range scope.Keys() {
		if _, ok := vaultItems[name]; ok {
			issues = append(issues, projectIssue(SeverityWarn, fmt.Sprintf(
				"MCP server %q collides with a vault server via %s: Claude Code prefers the project scope, which beadle does not manage",
				name, source)))

			continue
		}

		extra = append(extra, name)
	}

	if len(extra) > 0 {
		issues = append(issues, projectIssue(SeverityInfo, fmt.Sprintf(
			"%s defines MCP servers outside the vault (project scope, not managed by beadle): %s",
			source, strings.Join(extra, ", "))))
	}

	return issues
}

func projectIssue(severity, message string) Issue {
	return Issue{Severity: severity, Kind: kind.MCP, Agent: agent.ClaudeCodeID, Message: message}
}

func (e *Engine) farmPresentationIssues(active []*agent.Agent, ledger pluginLedger) []Issue {
	if e.home == "" || !e.config.KindEnabled(kind.Skills) {
		return nil
	}

	plan, _ := e.buildFarmPlan(ledger)

	var issues []Issue

	for _, a := range active {
		surface := a.Surface(kind.Skills)
		if surface == nil || e.config.ModeFor(a.ID, kind.Skills, surface.Traits().DefaultMode) == config.ModeOff {
			continue
		}

		dir := surface.Path()
		if !isDir(dir) {
			continue
		}

		fsType, err := hostFSType(dir)
		if err != nil || !fsutil.UnsupportedSymlinkFS(fsType) {
			continue
		}

		issues = append(issues, Issue{
			Severity: SeverityWarn,
			Kind:     kind.Skills,
			Agent:    a.ID,
			Message:  farmPresentationWarning(dir, e.home, len(plan.Desired)),
		})
	}

	return issues
}

func farmPresentationWarning(dir, home string, skills int) string {
	display := displayHomePath(dir, home)

	if skills == 0 {
		return fmt.Sprintf("symlinks are not supported in %s; a copy fallback is intentionally not performed", display)
	}

	return fmt.Sprintf("symlinks are not supported in %s; %d plugin skill(s) are not presented; a copy fallback is intentionally not performed", display, skills)
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

func (e *Engine) pluginPinIssues(active []*agent.Agent) []Issue {
	if e.home == "" {
		return nil
	}

	ledger, _, err := loadPluginLedger(e.vault.PluginsLedgerPath())
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Message: "cannot read the plugin ledger: " + err.Error()}}
	}

	manifest, manifestErr := plugin.Read(e.home)

	activeIDs := make(map[string]struct{}, len(active))

	for _, a := range active {
		activeIDs[a.ID] = struct{}{}
	}

	var issues []Issue

	for _, agentID := range slices.Sorted(maps.Keys(e.config.Agents)) {
		if _, on := activeIDs[agentID]; !on {
			continue
		}

		pins := e.config.Agents[agentID].PluginPins

		for _, key := range slices.Sorted(maps.Keys(pins)) {
			issues = append(issues, e.pinIssues(agentID, key, pins[key], ledger, manifest, manifestErr)...)
		}
	}

	return append(issues, e.strayPinPivotIssues(ledger)...)
}

func (e *Engine) pinIssues(agentID, key, version string, ledger pluginLedger, manifest plugin.Manifest, manifestErr error) []Issue {
	marketplace, name, ok := strings.Cut(key, "/")
	if !ok || !validPluginKey(marketplace, name) {
		return []Issue{pinIssue(SeverityWarn, agentID, fmt.Sprintf("plugin pin %q has an invalid key", key))}
	}

	var issues []Issue

	target := filepath.Join(e.home, pluginCacheDir, marketplace, name, version)
	if !isDir(target) {
		issues = append(issues, pinIssue(SeverityError, agentID, fmt.Sprintf(
			"plugin %s pinned to %s is not in the plugin cache; %s is missing its skills and MCP servers", key, version, agentID)))
	}

	pivot := filepath.Join(e.vault.PluginsDir(), marketplace, name, pinPivotName(version))

	pivotInfo, pivotErr := os.Lstat(pivot)
	if pivotErr == nil && isDir(target) && !pivotValid(pivot) {
		note := "is dangling"
		if pivotInfo.Mode()&fs.ModeSymlink == 0 {
			note = "is not a symlink"
		}

		issues = append(issues, pinIssue(SeverityError, agentID, fmt.Sprintf("plugin %s %s pivot %s; run beadle sync", key, pinPivotName(version), note)))
	}

	if e.pinUnknownPlugin(key, ledger, manifest, manifestErr) {
		issues = append(issues, pinIssue(SeverityWarn, agentID, fmt.Sprintf("plugin %s is not installed; the pin has no effect", key)))
	}

	return issues
}

func (e *Engine) pinUnknownPlugin(key string, ledger pluginLedger, manifest plugin.Manifest, manifestErr error) bool {
	if _, parked := ledger.Plugins[key]; parked {
		return false
	}

	if manifestErr != nil {
		return false
	}

	for _, p := range manifest.Plugins {
		if pluginKey(p.Marketplace, p.Name) == key {
			return false
		}
	}

	return true
}

func (e *Engine) strayPinPivotIssues(ledger pluginLedger) []Issue {
	pinned := e.pinnedVersions()

	marketplaces, err := os.ReadDir(e.vault.PluginsDir())
	if err != nil {
		return nil
	}

	var issues []Issue

	for _, marketplace := range marketplaces {
		if !marketplace.IsDir() || marketplace.Name() == quarantineDirName {
			continue
		}

		names, err := os.ReadDir(filepath.Join(e.vault.PluginsDir(), marketplace.Name()))
		if err != nil {
			continue
		}

		for _, name := range names {
			if !name.IsDir() {
				continue
			}

			key := pluginKey(marketplace.Name(), name.Name())
			if pinQuarantined(ledger, key) {
				continue
			}

			dir := filepath.Join(e.vault.PluginsDir(), marketplace.Name(), name.Name())

			issues = append(issues, strayPinDirIssues(key, pinned[key], dir)...)
		}
	}

	return issues
}

func strayPinDirIssues(key string, versions []string, dir string) []Issue {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var issues []Issue

	for _, entry := range entries {
		version, pinned := strings.CutPrefix(entry.Name(), pinPivotPrefix)
		if !pinned || version == "" || entry.Type()&fs.ModeSymlink == 0 || slices.Contains(versions, version) {
			continue
		}

		issues = append(issues, pivotIssue(SeverityWarn, fmt.Sprintf("plugin %s has a stale %s pivot; run beadle sync", key, entry.Name())))
	}

	return issues
}

func pinIssue(severity, agentID, message string) Issue {
	return Issue{Severity: severity, Kind: kind.Skills, Agent: agentID, Message: message}
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
		return []Issue{pivotIssue(SeverityWarn, fmt.Sprintf("plugin %s is not parked yet; run beadle sync", key))}
	}

	var issues []Issue

	issues = append(issues, pivotTargetIssues(key, record, rec)...)

	pivot := filepath.Join(e.vault.PluginsDir(), record.Marketplace, record.Name, "current")

	link, err := os.Readlink(pivot)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		issues = append(issues, pivotIssue(SeverityWarn, fmt.Sprintf("plugin %s pivot is missing; run beadle sync", key)))
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
		issues = append(issues, pivotIssue(SeverityWarn, fmt.Sprintf("plugin %s changed %s → %s; run beadle sync", key, rec.Version, record.Version)))
	}

	return issues
}

func pivotLifecycleIssues(key string, rec pluginLedgerRec) ([]Issue, bool) {
	switch {
	case !rec.QuarantinedAt.IsZero():
		return []Issue{pivotIssue(SeverityError, fmt.Sprintf("plugin %s@%s is quarantined since %s; run beadle heal",
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

func (e *Engine) projectPolicyIssues(active []*agent.Agent) []Issue {
	policy, err := e.projectPolicy()
	if err != nil {
		return []Issue{{Severity: SeverityError, Kind: kind.Projects, Message: "project policy: " + err.Error()}}
	}

	identity := e.projectIdentity()

	issues := []Issue{{Severity: SeverityInfo, Kind: kind.Projects, Message: projectIdentityMessage(identity)}}

	for _, surface := range projectSurfaces(active) {
		issues = append(issues, e.projectSurfaceIssues(surface, policy)...)
	}

	return issues
}

func projectIdentityMessage(identity proj.Identity) string {
	if identity.Slugs {
		return fmt.Sprintf("project %s (path slug; not a git checkout)", identity.ID)
	}

	remote := identity.Remote
	if remote == "" {
		remote = "none"
	}

	return fmt.Sprintf("project %s (remote: %s)", identity.ID, remote)
}

func projectSurfaces(active []*agent.Agent) []agent.Surface {
	var out []agent.Surface

	for _, a := range active {
		for _, surface := range a.SurfacesOf(kind.Projects) {
			if _, ok := surface.(agent.ProjectFile); ok {
				out = append(out, surface)
			}
		}
	}

	return out
}

func (e *Engine) projectSurfaceIssues(surface agent.Surface, policy proj.Policy) []Issue {
	file, _ := surface.(agent.ProjectFile)

	rel := file.ProjectRel()
	path := surface.Path()

	var issues []Issue

	if err := agent.CheckProjectTarget(path); err != nil {
		issues = append(issues, Issue{Severity: SeverityError, Kind: kind.Projects, Message: err.Error()})
	}

	publishable, err := e.projectPublishable(rel)
	if err != nil {
		issues = append(issues, Issue{Severity: SeverityWarn, Kind: kind.Projects, Message: err.Error()})
	}

	allowed := policy.AllowsSecrets(rel) || publishable

	if policy.Enabled(rel) {
		issues = append(issues, Issue{
			Severity: SeverityInfo, Kind: kind.Projects,
			Message: fmt.Sprintf("project file %s: enabled (publishable: %s)", rel, boolWord(allowed)),
		})
	}

	return append(issues, projectLeakIssues(surface, path, rel, allowed)...)
}

func projectLeakIssues(surface agent.Surface, path, rel string, allowed bool) []Issue {
	data, present, err := readOptional(path)
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Kind: kind.Projects, Message: err.Error()}}
	}

	if !present || allowed {
		return nil
	}

	var issues []Issue

	if _, fenced := surface.(fencedSurface); fenced {
		if _, _, found, err := digest.Strip(data); err == nil && found {
			issues = append(issues, Issue{
				Severity: SeverityError, Kind: kind.Projects,
				Message: fmt.Sprintf("the generated digest block in %s lives in a git-tracked file; gitignore it or enable the file with --allow-secrets", path),
			})
		}
	}

	if strings.HasSuffix(rel, ".json") && bytes.Contains(data, []byte("{secret:")) {
		issues = append(issues, Issue{
			Severity: SeverityWarn, Kind: kind.Projects,
			Message: fmt.Sprintf("project MCP file %s is git-tracked and holds {secret:} references; values are rendered as ${NAME}", rel),
		})
	}

	return issues
}

func boolWord(value bool) string {
	if value {
		return "yes"
	}

	return "no"
}
