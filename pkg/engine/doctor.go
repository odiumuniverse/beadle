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
	"github.com/odiumuniverse/beadle/pkg/daemon"
	"github.com/odiumuniverse/beadle/pkg/digest"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/permission"
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
	issues = append(issues, e.rulingIssues()...)

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
	issues = append(issues, e.skillShadowIssues(st, active)...)
	issues = append(issues, e.visibilityIssues(st, active)...)
	issues = append(issues, e.skillReferenceIssues(st, active)...)
	issues = append(issues, e.adoptionIssues(st)...)
	issues = append(issues, e.canonSkillValidityIssues(st)...)
	issues = append(issues, e.secretIssues()...)
	issues = append(issues, e.projectScopeIssues(ctx, active)...)
	issues = append(issues, e.projectPolicyIssues(active)...)
	issues = append(issues, e.claudeUserRulesIssues(active)...)
	issues = append(issues, e.claudeProjectRulesIssues(active)...)
	issues = append(issues, e.pluginRefIssues(active)...)
	issues = append(issues, e.pluginPivotIssues(ctx)...)
	issues = append(issues, e.pluginSourceIssues()...)
	issues = append(issues, e.pluginDuplicateIssues()...)
	issues = append(issues, e.orphanPivotIssues()...)
	issues = append(issues, e.pluginPinIssues(active)...)
	issues = append(issues, e.pluginMigrationIssues(ctx, ledger)...)
	issues = append(issues, e.farmPresentationIssues(active, ledger)...)
	issues = append(issues, e.bundleIssues(ctx)...)
	issues = append(issues, e.digestIssues(active, st)...)
	issues = append(issues, e.memorySecretIssues()...)
	issues = append(issues, e.rulesSecretIssues()...)
	issues = append(issues, e.permissionCanonIssues()...)
	issues = append(issues, e.subagentIssues(active)...)
	issues = append(issues, e.commandIssues(active)...)
	issues = append(issues, e.openCodeInlineAgentIssues()...)
	issues = append(issues, e.daemonIssues()...)
	issues = append(issues, e.piMCPAdapterIssues(ctx, active)...)
	issues = append(issues, e.kiloLegacySkillIssues(st, active)...)
	issues = append(issues, e.hookFileIssues(active)...)
	issues = append(issues, e.pluginHookIssues()...)
	issues = append(issues, e.hookSecretIssues()...)
	issues = append(issues, e.FlatSkillIssues(active)...)
	issues = append(issues, e.FarmAgentIssues()...)
	issues = append(issues, e.FarmCommandIssues()...)
	issues = append(issues, e.DSHIssues()...)
	issues = append(issues, e.dshMCPIssues(active)...)

	return issues, nil
}

var daemonStatusRunner secret.Runner = secret.ExecRunner{}

func (e *Engine) daemonIssues() []Issue {
	if e.home == "" {
		return nil
	}

	status, err := daemon.Check(e.home, daemon.DefaultLabel, daemonStatusRunner)
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Message: "cannot inspect the background watcher: " + err.Error()}}
	}

	switch {
	case !status.Installed:
		return []Issue{{Severity: SeverityWarn, Message: "daemon is not installed; run beadle daemon install"}}
	case !status.Loaded:
		return []Issue{{Severity: SeverityWarn, Message: fmt.Sprintf(
			"daemon is installed but not loaded; load %s with launchctl/systemctl or reinstall it with beadle daemon install", status.Path)}}
	default:
		return e.daemonEnvIssues(status)
	}
}

func (e *Engine) permissionCanonIssues() []Issue {
	data, present, err := readOptional(e.vault.PermissionsPath())
	if err != nil || !present {
		return nil
	}

	rules, err := permission.Parse(data)
	if err != nil {
		return nil
	}

	var issues []Issue

	for _, key := range rules.Keys() {
		if permission.Canonical(key) {
			continue
		}

		message := fmt.Sprintf("canonical permission key %q is not in canonical form and is never applied", key)

		if example := permissionCanonExample(key); example != "" {
			message += fmt.Sprintf("; expected %q", example)
		}

		issues = append(issues, Issue{Severity: SeverityWarn, Kind: kind.Permissions, Message: message})
	}

	return issues
}

func permissionCanonExample(key string) string {
	k, server, pattern, ok := permission.Split(key)
	if !ok {
		return ""
	}

	switch k {
	case permission.KindTool:
		return permission.ToolKey(strings.ToLower(pattern))
	case permission.KindMCP:
		return permission.MCPKey(server, strings.ToLower(pattern))
	default:
		return ""
	}
}

func (e *Engine) notePermissionCanonIssues(spec kind.Spec, opts SyncOptions, items kind.Items, report *KindReport) {
	if spec.ID != kind.Permissions || opts.DryRun {
		return
	}

	report.Warnings = append(report.Warnings, permissionCanonWarnings(items)...)
}

func permissionCanonWarnings(items kind.Items) []string {
	invalid := 0

	for key := range items {
		if !permission.Canonical(key) {
			invalid++
		}
	}

	if invalid == 0 {
		return nil
	}

	return []string{fmt.Sprintf("%d permission key(s) are not in canonical form and are never applied; run beadle doctor", invalid)}
}

func (e *Engine) rulesSecretIssues() []Issue {
	data, present, err := readOptional(e.vault.RulesPath())
	if err != nil || !present {
		return nil
	}

	hits := secret.ScanText(data)
	if len(hits) == 0 {
		return nil
	}

	return []Issue{{
		Severity: SeverityWarn, Kind: kind.Rules,
		Message: fmt.Sprintf(
			"rules canon %s holds %d secret-like line(s); the secret gate covers mcp, memory and project files only — keep tokens out of rules",
			e.vault.RulesPath(), len(hits)),
	}}
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
		return e.missingDigestIssue(target, path, len(notes) > 0)
	}

	_, fence, found, err := digest.Strip(data)
	if err != nil {
		return []Issue{{Severity: SeverityError, Kind: kind.Projects, Agent: target.agent.ID, Message: "cannot parse the beadle block in " + path}}
	}

	if !found {
		return e.missingDigestIssue(target, path, len(notes) > 0)
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

func (e *Engine) missingDigestIssue(target projectTarget, path string, hasNotes bool) []Issue {
	if !hasNotes {
		return nil
	}

	message := fmt.Sprintf("no memory digest in %s; run beadle sync", path)

	if !e.digestPublishable(target) {
		message = fmt.Sprintf(
			"no memory digest in %s; the file is not publishable (not gitignored), so beadle sync will not write it (override with beadle project enable --allow-secrets)",
			path)
	}

	return []Issue{{
		Severity: SeverityInfo, Kind: kind.Projects, Agent: target.agent.ID,
		Message: message,
	}}
}

func (e *Engine) digestPublishable(target projectTarget) bool {
	policy, err := e.projectPolicy()
	if err != nil {
		return false
	}

	if policy.AllowsSecrets(target.rel) {
		return true
	}

	publishable, err := e.projectPublishable(target.rel)

	return err == nil && publishable
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

// canonSkillValidityIssues reports canon directories that are not skills:
// no root SKILL.md. An owned directory is retired by the next sync; an
// unowned one is left alone.
func (e *Engine) canonSkillValidityIssues(st *state.State) []Issue {
	dir := e.vault.SkillsDir()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var issues []Issue

	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(dir, name)

		if !skill.ValidName(name) || entry.Type()&fs.ModeSymlink != 0 || !entry.IsDir() || skill.HasRoot(path) {
			continue
		}

		if skillNameOwned(st, name) {
			issues = append(issues, Issue{
				Severity: SeverityWarn, Kind: kind.Skills,
				Message: fmt.Sprintf("canon skill %s has no root SKILL.md; run beadle sync to retire it", name),
			})

			continue
		}

		issues = append(issues, Issue{
			Severity: SeverityInfo, Kind: kind.Skills,
			Message: fmt.Sprintf("canon directory %s has no root SKILL.md; it is not treated as a skill", name),
		})
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

func (e *Engine) skillShadowIssues(st *state.State, active []*agent.Agent) []Issue {
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

		issues = append(issues, e.skillShadowIssuesFor(st, a, refs)...)
	}

	return issues
}

func (e *Engine) skillShadowIssuesFor(st *state.State, a *agent.Agent, refs []agent.SkillRef) []Issue {
	if surface := a.Surface(kind.Skills); surface != nil && skillCaps(surface).Shadowing {
		// A host with verified precedence shows one copy per name: hidden
		// copies are not user-visible duplicates. The visible winner is still
		// checked against the canon by visibilityIssues.
		return nil
	}

	byName := map[string][]agent.SkillRef{}

	for _, ref := range refs {
		byName[ref.Name] = append(byName[ref.Name], ref)
	}

	var issues []Issue

	for _, name := range slices.Sorted(maps.Keys(byName)) {
		copies := byName[name]

		for i := range copies {
			for j := i + 1; j < len(copies); j++ {
				if copies[i].Dir == copies[j].Dir {
					continue
				}

				same, err := e.sameSkillTree(st, copies[i].Root, copies[j].Root)
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

// visibilityIssues reports the canon skills a foreign copy already delivers
// to file hosts, plus the forks between the canon and the copies a host can
// read. Bundle hosts report both through bundleHostStateIssues (В-19).
func (e *Engine) visibilityIssues(st *state.State, active []*agent.Agent) []Issue {
	items, _, err := e.loadVault(kind.Skills)
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Kind: kind.Skills, Message: "skills: " + err.Error()}}
	}

	canon := skill.Group(items)

	var issues []Issue

	for _, a := range active {
		surface := a.Surface(kind.Skills)
		if surface == nil || len(e.foreignReadDirs(a, surface)) == 0 || e.bundleActiveFor(st, a.ID) {
			continue
		}

		vis := e.resolveSkillVisibility(a, surface, st, canon)

		for _, warn := range vis.Warnings {
			issues = append(issues, Issue{Severity: SeverityWarn, Kind: kind.Skills, Agent: a.ID, Message: warn})
		}

		covered := vis.foreignCoverage()
		if len(covered) == 0 {
			continue
		}

		names := slices.Sorted(maps.Keys(covered))

		issues = append(issues, Issue{
			Severity: SeverityInfo, Kind: kind.Skills, Agent: a.ID,
			Message: fmt.Sprintf("%d skill(s) covered by other tools: %s", len(names), summarizeFarmNames(names)),
		})
	}

	return issues
}

// adoptionIssues reports the active skill adoptions: one Info per record,
// plus a Warn when neither the stashed original nor its symlink target is
// left, so a restore is no longer possible.
func (e *Engine) adoptionIssues(st *state.State) []Issue {
	var issues []Issue

	for _, record := range st.Adoptions {
		stash := e.adoptionStashPath(record.Host, record.Name)

		issues = append(issues, Issue{
			Severity: SeverityInfo, Kind: kind.Skills, Agent: record.Host,
			Message: fmt.Sprintf("skill %s was adopted (original at %s); run beadle skills unadopt %s --host %s to restore it",
				record.Name, displayHomePath(stash, e.home), record.Name, record.Host),
		})

		if entryExists(stash) || (record.Target != "" && entryExists(record.Target)) {
			continue
		}

		issues = append(issues, Issue{
			Severity: SeverityWarn, Kind: kind.Skills, Agent: record.Host,
			Message: fmt.Sprintf("the adopted copy of %s is gone (neither the stash nor the original target is there); the record stays for manual review", record.Name),
		})
	}

	return issues
}

// bundleActiveFor reports whether the agent's native bundle owns the skills
// channel; it reports its coverage and forks itself.
func (e *Engine) bundleActiveFor(st *state.State, agentID string) bool {
	host, ok := bundleHostFor(agentID)
	if !ok {
		return false
	}

	entry, ok := st.Bundles[string(host)]

	return ok && entry.Enabled
}

func (e *Engine) sameSkillTree(st *state.State, left, right string) (bool, error) {
	leftDigest, err := e.skillTreeDigest(st, left)
	if err != nil {
		return false, err
	}

	rightDigest, err := e.skillTreeDigest(st, right)
	if err != nil {
		return false, err
	}

	return leftDigest == rightDigest, nil
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

	manifest, manifestErr := plugin.ReadAll(e.home)

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

// pinSource reports the host that provides the plugin of a pin: the parked
// ledger record wins, then the installed manifest.
func pinSource(key string, ledger pluginLedger, manifest plugin.Manifest) string {
	if rec, parked := ledger.Plugins[key]; parked {
		return recSource(rec)
	}

	for _, p := range manifest.Plugins {
		if pluginKey(p.Origin, p.Name) == key {
			return p.Source
		}
	}

	return ""
}

func (e *Engine) pinIssues(agentID, key, version string, ledger pluginLedger, manifest plugin.Manifest, manifestErr error) []Issue {
	marketplace, name, ok := strings.Cut(key, "/")
	if !ok || !validPluginKey(marketplace, name) {
		return []Issue{pinIssue(SeverityWarn, agentID, fmt.Sprintf("plugin pin %q has an invalid key", key))}
	}

	if source := pinSource(key, ledger, manifest); source != "" && source != plugin.SourceClaudeCode {
		return []Issue{pinIssue(SeverityWarn, agentID, fmt.Sprintf(
			"plugin %s belongs to %s; version pins cover the Claude Code plugin cache only, so the pin has no effect", key, source))}
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
		if pluginKey(p.Origin, p.Name) == key {
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
		if !marketplace.IsDir() || reservedPluginDir(marketplace.Name()) {
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

func (e *Engine) orphanPivotIssues() []Issue {
	ledger, _, err := loadPluginLedger(e.vault.PluginsLedgerPath())
	if err != nil {
		return nil
	}

	var issues []Issue

	for _, dir := range e.orphanPivotDirs(ledger, e.pinnedVersions()) {
		key := pluginKey(filepath.Base(filepath.Dir(dir)), filepath.Base(dir))

		if _, safe := orphanPivotSafe(dir); !safe {
			issues = append(issues, Issue{Severity: SeverityWarn, Message: fmt.Sprintf(
				"plugin %s has an orphan pivot holding regular files; review and remove it manually", key)})

			continue
		}

		issues = append(issues, Issue{Severity: SeverityWarn, Message: fmt.Sprintf(
			"plugin %s has an orphan pivot left in the vault (not in the ledger); run beadle heal", key)})
	}

	if e.home != "" {
		links, _ := e.pruneOrphanFarmLinks(ledger, true)

		count := 0

		for _, link := range links {
			count += link.Count
		}

		if count > 0 {
			issues = append(issues, Issue{Severity: SeverityWarn, Kind: kind.Skills, Message: fmt.Sprintf(
				"%d orphan plugin skill link(s) point into retired pivots; run beadle heal", count)})
		}
	}

	return issues
}

func (e *Engine) pluginPivotIssues(_ context.Context) []Issue {
	if e.home == "" {
		return nil
	}

	manifest, err := plugin.ReadAll(e.home)
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Message: "cannot read the plugin registry: " + err.Error()}}
	}

	ledger, _, err := loadPluginLedger(e.vault.PluginsLedgerPath())
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Message: "cannot read the plugin ledger: " + err.Error()}}
	}

	installed := map[string]plugin.Plugin{}

	groups, _, _ := groupPlugins(manifest.Plugins)

	for _, group := range groups {
		installed[pluginKey(group.Origin, group.Name)] = chooseRecord(group.Plugins)
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
		if key == bundlePluginKey() {
			continue
		}

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

	pivot := filepath.Join(e.vault.PluginsDir(), record.Origin, record.Name, "current")

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

	// One file, one diagnostic: several hosts read the same project rules
	// (AGENTS.md), and the first surface already reports the shared path.
	seen := map[string]bool{}

	for _, surface := range projectSurfaces(active) {
		realPath := agent.RealPath(surface.Path())
		if seen[realPath] {
			continue
		}

		seen[realPath] = true

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

	// A per-file project surface (.cursor/rules, .claude/rules) is a directory
	// of files: the single-file target gate below would refuse the directory
	// itself, so it is skipped for surfaces that declare themselves as such.
	_, directory := surface.(agent.ProjectDirectory)

	var issues []Issue

	if !directory {
		if err := agent.CheckProjectTarget(path); err != nil {
			issues = append(issues, Issue{Severity: SeverityError, Kind: kind.Projects, Message: err.Error()})
		}
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

	if directory {
		return issues
	}

	return append(issues, projectLeakIssues(surface, path, rel, allowed)...)
}

func projectLeakIssues(surface agent.Surface, path, rel string, allowed bool) []Issue {
	if _, directory := surface.(agent.ProjectDirectory); directory {
		return nil
	}

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

// subagentIssues reports the canonical subagent diagnostics:
// host-expressiveness notes and secret-like canon lines.
func (e *Engine) subagentIssues(active []*agent.Agent) []Issue {
	items, _, err := e.loadVault(kind.Subagents)
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Kind: kind.Subagents, Message: "cannot read the subagent canon: " + err.Error()}}
	}

	issues := []Issue{}

	for _, a := range active {
		for _, key := range slices.Sorted(maps.Keys(items)) {
			for _, notice := range a.Notices(kind.Subagents, key, items[key]) {
				issues = append(issues, Issue{Severity: SeverityInfo, Kind: kind.Subagents, Agent: a.ID, Message: notice.Message})
			}
		}
	}

	for _, key := range slices.Sorted(maps.Keys(items)) {
		if hits := secret.ScanText(items[key]); len(hits) > 0 {
			issues = append(issues, Issue{
				Severity: SeverityWarn, Kind: kind.Subagents,
				Message: fmt.Sprintf(
					"subagent %s holds %d secret-like line(s); the secret gate covers mcp, memory and project files only — keep tokens out of subagents",
					key, len(hits)),
			})
		}
	}

	return issues
}

// openCodeInlineAgentIssues reports the agents declared inline in the
// OpenCode config: they are a surface beadle does not manage (A-28 §5.5).
func (e *Engine) openCodeInlineAgentIssues() []Issue {
	if e.home == "" {
		return nil
	}

	names, err := agent.OpenCodeInlineAgents(e.home)
	if err != nil {
		return []Issue{{
			Severity: SeverityWarn, Kind: kind.Subagents, Agent: agent.OpenCodeID,
			Message: "cannot read inline agents: " + err.Error(),
		}}
	}

	if len(names) == 0 {
		return nil
	}

	return []Issue{{
		Severity: SeverityInfo, Kind: kind.Subagents, Agent: agent.OpenCodeID,
		Message: fmt.Sprintf(
			"opencode.json(c) declares inline agents: %s; beadle does not manage them — move them to agents/<name>.md",
			strings.Join(names, ", ")),
	}}
}

// commandIssues reports the canonical command diagnostics: host notes,
// the Claude skill-over-command precedence, the deprecated Codex prompts,
// the unmanaged Cursor directory and secret-like canon lines.
func (e *Engine) commandIssues(active []*agent.Agent) []Issue {
	items, _, err := e.loadVault(kind.Commands)
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Kind: kind.Commands, Message: "cannot read the command canon: " + err.Error()}}
	}

	var issues []Issue

	for _, a := range active {
		for _, key := range slices.Sorted(maps.Keys(items)) {
			for _, notice := range a.Notices(kind.Commands, key, items[key]) {
				issues = append(issues, Issue{Severity: SeverityInfo, Kind: kind.Commands, Agent: a.ID, Message: notice.Message})
			}
		}
	}

	issues = append(issues, e.claudeCommandSkillIssues(items)...)
	issues = append(issues, e.codexPromptIssues(active)...)
	issues = append(issues, e.cursorCommandIssues()...)

	for _, key := range slices.Sorted(maps.Keys(items)) {
		if hits := secret.ScanText(items[key]); len(hits) > 0 {
			issues = append(issues, Issue{
				Severity: SeverityWarn, Kind: kind.Commands,
				Message: fmt.Sprintf(
					"command %s holds %d secret-like line(s); the secret gate covers mcp, memory and project files only — keep tokens out of commands",
					key, len(hits)),
			})
		}
	}

	return issues
}

// claudeCommandSkillIssues reports names where a Claude skill shadows a
// legacy command: the host prefers the skill.
func (e *Engine) claudeCommandSkillIssues(items kind.Items) []Issue {
	skills, err := skill.ReadDir(e.vault.SkillsDir())
	if err != nil {
		return nil
	}

	var issues []Issue

	for _, key := range slices.Sorted(maps.Keys(items)) {
		name := strings.TrimSuffix(key, ".md")

		if _, taken := skills[name]; taken {
			issues = append(issues, Issue{
				Severity: SeverityWarn, Kind: kind.Commands, Agent: agent.ClaudeCodeID,
				Message: fmt.Sprintf("claude prefers the skill %q over the legacy command %s; rename one of them", name, key),
			})
		}
	}

	return issues
}

// codexPromptIssues reports the deprecated Codex prompts surface.
func (e *Engine) codexPromptIssues(active []*agent.Agent) []Issue {
	var issues []Issue

	for _, a := range active {
		if a.Surface(kind.Commands) == nil {
			continue
		}

		if a.ID == agent.CodexID {
			issues = append(issues, Issue{
				Severity: SeverityInfo, Kind: kind.Commands, Agent: a.ID,
				Message: "codex prompts are deprecated; beadle pulls them into the vault and does not write them back",
			})
		}
	}

	return issues
}

// cursorCommandIssues reports the unmanaged Cursor commands directory.
func (e *Engine) cursorCommandIssues() []Issue {
	if e.home == "" {
		return nil
	}

	if _, err := os.Stat(filepath.Join(e.home, ".cursor", "commands")); err == nil {
		return []Issue{{
			Severity: SeverityInfo, Kind: kind.Commands, Agent: agent.CursorID,
			Message: "~/.cursor/commands exists; cursor file commands are not supported yet and are left untouched",
		}}
	}

	return nil
}
