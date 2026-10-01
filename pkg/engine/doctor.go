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

// Severity vocabulary (W7-UX §3.2). "warn" and "ok" are gone: a check that
// passes is not a finding, it is the absence of one, and only findings are
// reported. "failed" stays distinct from a severity — it is the one word that
// means "we tried and it broke", and collapsing it into a warning hides a bug.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
	SeverityInfo    = "info"
)

// SeverityWarn is the old name, kept so the existing call sites keep compiling
// while they are migrated one check at a time. It is the value "warning".
const SeverityWarn = SeverityWarning

const maxObjectIssues = 5

// beadleName is the product name as the user types it: every printed fix
// command starts with it.
const beadleName = "beadle"

// syncCommand is the subcommand a finding names when the repair is beadle's own
// to run. A constant because a printed command a user pastes has to be the same
// command everywhere it appears, and because goconst is right that four copies
// of a string a user will type is four places to fix a typo.
const syncCommand = "sync"

// Finding is what every doctor check reports (W7-UX §3.1). A check that passes
// returns nothing: only findings are reported, so a clean run is one line.
type Finding struct {
	// Severity is error, warning or info.
	Severity string `json:"severity"`

	// Subject is the stable "kind.name" a script matches on. Message is prose
	// and is not stable; this is.
	Subject string `json:"subject"`

	// Message is the prose the user reads.
	Message string `json:"message"`

	// Kind and Agent are what Subject is derived from when a check does not
	// name its own. They stay in the JSON because existing readers use them.
	Kind  kind.ID `json:"kind,omitempty"`
	Agent string  `json:"agent,omitempty"`

	// Fix is a list of ready-to-run argv arrays, one step each and in order,
	// so a user can paste entry 0 and stop. It is a list rather than a string
	// because a rendered command cannot be re-split safely. Empty means there
	// is nothing to suggest, not that the finding is harmless.
	Fix [][]string `json:"fix,omitempty"`

	// SafeToAutofix is true only when the fix is a repair beadle fully owns —
	// creating a directory, re-registering a service — and changes nothing the
	// user wrote. A consent prompt, a conflict, a policy refusal and anything
	// under a user-edited file stay false, and stay a printed instruction.
	SafeToAutofix bool `json:"safe_to_autofix"`
}

// Issue is the historical name of Finding, kept so the ~120 existing call
// sites keep compiling. Kind and Agent remain the two things a subject is
// derived from when a check does not name its own.
type Issue = Finding

// Scope returns the stable subject for a finding: the explicit Subject when a
// check named one, otherwise derived from Kind and Agent so every finding
// carries something a script can match.
func (f Finding) Scope() string {
	if f.Subject != "" {
		return f.Subject
	}

	switch {
	case f.Agent != "" && f.Kind != "":
		return f.Agent + "/" + string(f.Kind)
	case f.Agent != "":
		return f.Agent
	case f.Kind != "":
		return string(f.Kind)
	default:
		return "vault"
	}
}

// CanAutoFix reports whether `doctor --fix` would apply this finding.
func (f Finding) CanAutoFix() bool { return f.SafeToAutofix && len(f.Fix) > 0 }

// migrationSkippedIssues names the plugins the farm migration could not
// resolve. The migration records them in `farm_migration.skipped` — key to
// reason — and deliberately does not stall on them: the rest of the farm
// moves and this one waits for the user. That is a decision the user has to
// be able to see, because a ledger entry that silently never migrates looks
// exactly like one that was never there.
//
// The fix is a printed instruction, never an auto-fix: re-resolving a
// marketplace needs a decision only the user can make.
func migrationSkippedIssues(st *state.State) []Issue {
	if st == nil || st.FarmMigration == nil || len(st.FarmMigration.Skipped) == 0 {
		return nil
	}

	keys := make([]string, 0, len(st.FarmMigration.Skipped))
	for key := range st.FarmMigration.Skipped {
		keys = append(keys, key)
	}

	slices.Sort(keys)

	issues := make([]Issue, 0, len(keys))

	for _, key := range keys {
		reason := st.FarmMigration.Skipped[key]
		if reason == "" {
			reason = "the plugin manager could not resolve it"
		}

		issues = append(issues, Issue{
			Severity: SeverityWarning,
			Subject:  "plugin.skipped." + key,
			Message:  "plugin " + key + " skipped during migration: " + reason,
			// Installing it by hand is the user's call: the marketplace may be
			// private, gone, or reachable only from their account.
			Fix:           [][]string{{beadleName, "plugins", cliInstall, key}},
			SafeToAutofix: false,
		})
	}

	return issues
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
	issues = append(issues, migrationSkippedIssues(st)...)
	issues = append(issues, e.bundleFarmIssues(st)...)

	active, err := e.activeAgents(ctx)
	if err != nil {
		return nil, err
	}

	ledger, _, _ := loadPluginLedger(e.vault.PluginsLedgerPath())
	if ledger.Plugins == nil {
		ledger = emptyPluginLedger()
	}

	for _, a := range active {
		issues = append(issues, e.symlinkIssues(a)...)
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
	issues = append(issues, e.secretHistoryIssues(ctx)...)
	issues = append(issues, e.projectScopeIssues(ctx, active)...)
	issues = append(issues, e.projectPolicyIssues(active)...)
	issues = append(issues, e.claudeUserRulesIssues(active)...)
	issues = append(issues, e.claudeProjectRulesIssues(active)...)
	issues = append(issues, e.pluginRefIssues(active)...)
	issues = append(issues, e.pluginSourceIssues()...)
	issues = append(issues, e.pluginDuplicateIssues()...)
	issues = append(issues, e.pluginMigrationIssues(ctx)...)
	issues = append(issues, e.bundleIssues(ctx)...)
	issues = append(issues, e.digestIssues(active, st)...)
	issues = append(issues, e.surfaceOffIssues(ctx, st, active)...)
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
	issues = append(issues, e.hookModuleIssues()...)
	issues = append(issues, e.hookSecretIssues()...)
	issues = append(issues, e.FlatSkillIssues(active)...)
	issues = append(issues, e.DSHIssues()...)
	issues = append(issues, e.dshMCPIssues(active)...)
	issues = append(issues, e.OmpIssues()...)

	return issues, nil
}

// surfaceOffIssues reports the surfaces a user turned off while the files
// beadle synced earlier are still on disk. beadle never deletes on a mode flip
// — nothing goes without the user's decision — so the doctor says what remains
// and how to get back, once per agent and kind.
//
// The count comes from the state's base (the content beadle itself wrote at the
// last sync) intersected with what the surface reads now: a foreign file the
// user placed in the directory is never beadle's to report, and a file the user
// already removed is not counted as remaining.
func (e *Engine) surfaceOffIssues(ctx context.Context, st *state.State, active []*agent.Agent) []Issue {
	var issues []Issue

	for _, a := range active {
		for _, surface := range a.Surfaces {
			k := surface.Kind()

			if e.config.ModeFor(a.ID, k, surface.Traits().DefaultMode) != config.ModeOff {
				continue
			}

			base, ok := st.Base(k, a.ID)
			if !ok || len(base) == 0 {
				continue
			}

			snap, err := surface.Read(ctx)
			if err != nil {
				continue
			}

			remaining := 0

			for key := range base {
				if _, present := snap.Items[key]; present {
					remaining++
				}
			}

			if remaining == 0 {
				continue
			}

			issues = append(issues, Issue{
				Severity: SeverityInfo, Kind: k, Agent: a.ID,
				Message: fmt.Sprintf(
					"%s %s: surface is off; %d file(s) beadle synced earlier remain in %s "+
						"(remove them by hand, or re-enable with `beadle agents mode %s %s sync`)",
					a.ID, k, remaining, surface.Path(), a.ID, k),
			})
		}
	}

	return issues
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
		// Installing the watcher is a repair beadle owns: it writes a unit
		// under the user's own LaunchAgents / systemd user directory and
		// changes nothing the user wrote. That is what makes it the one
		// finding doctor may apply without asking twice.
		return []Issue{{
			Severity:      SeverityWarn,
			Subject:       "daemon.installed",
			Message:       "daemon is not installed: the background watcher will not run",
			Fix:           [][]string{{beadleName, "daemon", "install"}},
			SafeToAutofix: true,
		}}
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
			Message: fmt.Sprintf("memory notes with plaintext secrets are not ignored by git: %s; run beadle sync",
				strings.Join(plaintexts, ", ")),
			Fix: [][]string{{beadleName, syncCommand}},
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
			Message: fmt.Sprintf("manual edits inside the generated block in %s; the digest is frozen: restore the bytes, delete the block, or run beadle sync --refresh-digest",
				path),
			Fix: [][]string{{beadleName, "sync", "--refresh-digest"}},
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
		// The spec's template (W7-UX §3.3): the runnable command that used to
		// be buried in the prose becomes argv, and the conflict id becomes a
		// stable subject. Never safe to autofix — resolving picks a side, and
		// the choice is the user's.
		issues = append(issues, Issue{
			Severity: SeverityWarn,
			Kind:     c.Kind,
			Agent:    c.Agent,
			Subject:  "conflict." + c.ID(),
			Message: fmt.Sprintf("%s differs between the vault and %s (%s)",
				c.TargetKey(), c.Agent, c.Reason),
			Fix: [][]string{{beadleName, "resolve", c.ID()}},
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
				Message: fmt.Sprintf("%d change(s) are not in the vault yet; run beadle sync",
					pending[agentID]),
				Fix: [][]string{{beadleName, syncCommand}},
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

// symlinkIssues reports a broken symlink in any agent surface beadle owns.
// The farm used to link plugin skills into those surfaces, so a link into the
// plugin cache was expected to dangle until the next sync; the farm is gone,
// so a broken link is simply broken.
func (e *Engine) symlinkIssues(a *agent.Agent) []Issue {
	var issues []Issue

	for _, surface := range a.Surfaces {
		path := surface.Path()

		info, err := os.Lstat(path)
		if err != nil {
			continue
		}

		if info.Mode()&fs.ModeSymlink != 0 && !fsutil.Exists(path) {
			issues = append(issues, e.brokenSymlinkIssues(a, path)...)

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
				issues = append(issues, e.brokenSymlinkIssues(a, child)...)
			}
		}
	}

	return issues
}

func (e *Engine) brokenSymlinkIssues(a *agent.Agent, path string) []Issue {
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
			Fix: [][]string{{beadleName, "skills", "unadopt", record.Name, "--host", record.Host}},
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

	// A keyring-backed store with no stored value is never probed: reading the
	// keychain opens a system access dialog beside a destructive "Reset To
	// Defaults" button, and an empty store has nothing to verify.
	switch keyringErr := e.secrets.KeyringErr(); {
	case e.secrets.Backend() != secret.BackendKeyring:
		if err := e.secrets.Probe(); err != nil {
			return append(issues, Issue{Severity: SeverityWarn, Message: "keyring unavailable: " + err.Error()})
		}
	case keyringErr != nil:
		// The store already failed on load; reporting that needs no second
		// keychain read.
		return append(issues, Issue{Severity: SeverityWarn, Message: "keyring unavailable: " + keyringErr.Error()})
	case !e.secrets.NeedsProbe():
		issues = append(issues, Issue{Severity: SeverityInfo, Message: "keyring backend, no stored values — keychain not probed"})
	default:
		if err := e.secrets.Probe(); err != nil {
			return append(issues, Issue{Severity: SeverityWarn, Message: "keyring unavailable: " + err.Error()})
		}
	}

	refs, err := e.secretRefs()
	if err != nil {
		return append(issues, Issue{Severity: SeverityError, Kind: kind.MCP, Message: "read secret references: " + err.Error()})
	}

	for _, name := range refs {
		if !e.secrets.Has(name) {
			issues = append(issues, Issue{
				Severity: SeverityError,
				Message: fmt.Sprintf("secret %s has no value in %s; run beadle secrets set %s",
					name, e.vault.SecretsPath(), name),
				Fix: [][]string{{beadleName, "secrets", "set", name}},
			})
		}
	}

	for _, name := range e.secrets.Names() {
		if !slices.Contains(refs, name) {
			issues = append(issues, Issue{
				Severity: SeverityInfo,
				Message:  fmt.Sprintf("secret %s is unused; run beadle secrets prune", name),
				Fix:      [][]string{{beadleName, "secrets", "prune", name}},
			})
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

// hiddenStaleIssues warns about host files kept for canon items the surface
// cannot express when the kept file still holds different content: hiding
// never deletes the file (A-29), so the host keeps loading the stale copy.
// Identical content stays with the expressiveness Info, a missing file is
// silence, and a pull-mode surface is not managed here at all.
func (e *Engine) hiddenStaleIssues(id kind.ID, active []*agent.Agent, items kind.Items) []Issue {
	var issues []Issue

	for _, a := range active {
		surface := a.Surface(id)
		if surface == nil {
			continue
		}

		reporter, ok := surface.(agent.HiddenHostCopies)
		if !ok {
			continue
		}

		if !e.config.ModeFor(a.ID, id, surface.Traits().DefaultMode).Pushes() {
			continue
		}

		proj := project(items, surface)

		for _, key := range slices.Sorted(maps.Keys(proj.hidden)) {
			kept, err := reporter.HiddenCopy(key, items[key])
			if err != nil || !kept.Present || !kept.Differs {
				continue
			}

			issues = append(issues, Issue{
				Severity: SeverityWarn, Kind: id, Agent: a.ID,
				Message: fmt.Sprintf(
					"host copy %s is stale: the canon item %s cannot be expressed by %s; the host still loads the old content (remove the file or change the mode)",
					displayHomePath(kept.Path, e.home), key, a.Name),
			})
		}
	}

	return issues
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

	issues = append(issues, e.hiddenStaleIssues(kind.Subagents, active, items)...)

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
// the Claude skill-over-command precedence, the deprecated Codex prompts
// and secret-like canon lines.
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

	issues = append(issues, e.hiddenStaleIssues(kind.Commands, active, items)...)

	issues = append(issues, e.claudeCommandSkillIssues(items)...)
	issues = append(issues, e.codexPromptIssues(active)...)

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
