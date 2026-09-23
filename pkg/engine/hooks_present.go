package engine

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/hooks"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// hookPresentSeam lets tests interleave a change between the plan and the
// write; production leaves it nil.
var hookPresentSeam func(path string)

// hookFileTarget pairs a host with its user-level hooks file.
type hookFileTarget struct {
	agentID string
	host    hooks.Host
	file    string
}

// hookFileTargets lists the user-level hooks files beadle presents the canon
// into. Native bundle hosts (Claude, Gemini, Antigravity) render hooks through
// pkg/bundle instead.
func hookFileTargets(home string) []hookFileTarget {
	targets := make([]hookFileTarget, 0, 2)

	for _, host := range []hooks.Host{hooks.Cursor, hooks.Codex} {
		file, ok := hooks.HostFile(host, home)
		if !ok {
			continue
		}

		id := agent.CursorID
		if host == hooks.Codex {
			id = agent.CodexID
		}

		targets = append(targets, hookFileTarget{agentID: id, host: host, file: file})
	}

	return targets
}

// presentHooks renders the approved hooks canon into the user-level hooks
// files of Cursor and Codex. The canon is authoring-only, so this is a forward
// presentation: nothing is pulled back and foreign entries in the host file
// are preserved. Removal advice and trust stay with the hosts (Codex reviews
// every hook by hash before it runs).
func (e *Engine) presentHooks(st *state.State, report *Report, active []*agent.Agent, opts SyncOptions) {
	if e.home == "" || opts.Direction == config.ModePull {
		return
	}

	canon, err := hooks.Load(e.vault.HooksPath())
	if err != nil {
		report.Warnings = append(report.Warnings, "hooks: "+err.Error())

		return
	}

	approved := hooks.Approved(e.config)

	for _, target := range hookFileTargets(e.home) {
		host := agent.ByID(active, target.agentID)
		if host == nil {
			continue
		}

		if detected, err := host.Detect(); err != nil || !detected {
			continue
		}

		e.presentHookFile(target, st, report, canon, approved, opts)
	}
}

// presentHookFile patches one host hooks file with the approved canon hooks.
func (e *Engine) presentHookFile(
	target hookFileTarget, st *state.State, report *Report, canon map[string]hooks.Hook, approved map[string]bool, opts SyncOptions,
) {
	shown := displayHomePath(target.file, e.home)
	owned := st.HookRenders[string(target.host)]

	if len(canon) == 0 && len(owned) == 0 {
		return
	}

	path, err := resolveHookTarget(target.file)
	if err != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("hooks: %s: %v", shown, err))

		return
	}

	data, present, err := readOptional(path)
	if err != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("hooks: %s: %v", shown, err))

		return
	}

	plan, err := hooks.PlanHostFile(target.host, data, canon, approved, owned)
	if err != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("hooks: %s: %v", shown, err))

		return
	}

	report.Warnings = append(report.Warnings, plan.Warnings...)

	if len(plan.Ops) == 0 {
		if len(plan.Owned) == 0 {
			delete(st.HookRenders, string(target.host))
		}

		return
	}

	if opts.DryRun {
		report.Notes = append(report.Notes, hookRenderNote(plan, shown, "would render", "would remove"))

		return
	}

	note, err := e.applyHookPlan(target, path, data, present, plan)
	if err != nil {
		report.Warnings = append(report.Warnings, "hooks: "+err.Error())

		return
	}

	setHookRenders(st, target.host, plan.Owned)

	report.Notes = append(report.Notes, note)
}

// applyHookPlan patches one host hooks file, checks it did not change under
// the plan and writes it back atomically. It returns the report note.
func (e *Engine) applyHookPlan(target hookFileTarget, path string, data []byte, present bool, plan hooks.FilePlan) (string, error) {
	shown := displayHomePath(target.file, e.home)

	if !present {
		data = []byte("{}\n")
	}

	out, err := agent.PatchJSON(data, patchOps(plan.Ops))
	if err != nil {
		return "", fmt.Errorf("%s: %w", shown, err)
	}

	if hookPresentSeam != nil {
		hookPresentSeam(path)
	}

	if present {
		current, _, err := readOptional(path)
		if err != nil {
			return "", fmt.Errorf("%s: %w", shown, err)
		}

		if !bytes.Equal(current, data) {
			return "", fmt.Errorf("%s changed concurrently; skipped", shown)
		}
	}

	if err := fsutil.WriteFileAtomic(path, out, hookFilePerm(path)); err != nil {
		return "", fmt.Errorf("%s: %w", shown, err)
	}

	return hookRenderNote(plan, shown, "rendered", "removed"), nil
}

// setHookRenders records the commands beadle owns in one host hooks file.
func setHookRenders(st *state.State, host hooks.Host, owned []string) {
	if len(owned) == 0 {
		delete(st.HookRenders, string(host))

		return
	}

	if st.HookRenders == nil {
		st.HookRenders = map[string][]string{}
	}

	st.HookRenders[string(host)] = owned
}

// hookRenderNote words one presentation result.
func hookRenderNote(plan hooks.FilePlan, shown, verb, removeVerb string) string {
	if plan.Rendered == 0 {
		return fmt.Sprintf("hooks: %s the canon hooks from %s", removeVerb, shown)
	}

	return fmt.Sprintf("hooks: %s %d hook(s) into %s", verb, plan.Rendered, shown)
}

// hookFileIssues reports the host-side trust step for the hooks beadle renders
// into Codex's user-level file: Codex trusts hooks by hash and skips new or
// changed ones until they are reviewed in /hooks. Cursor user hooks carry no
// such review. The note stays only while the rendered commands are still in
// the file, so it disappears once the user removes them.
func (e *Engine) hookFileIssues(active []*agent.Agent) []Issue {
	if e.home == "" || agent.ByID(active, agent.CodexID) == nil {
		return nil
	}

	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		return nil
	}

	rendered := st.HookRenders[string(hooks.Codex)]
	if len(rendered) == 0 {
		return nil
	}

	path, _ := hooks.HostFile(hooks.Codex, e.home)

	target, err := resolveHookTarget(path)
	if err != nil {
		return nil
	}

	data, present, err := readOptional(target)
	if err != nil || !present || !hooks.FileHasCommands(hooks.Codex, data, rendered) {
		return nil
	}

	return []Issue{{
		Severity: SeverityInfo, Agent: agent.CodexID,
		Message: fmt.Sprintf(
			"%d hook(s) are rendered into %s; Codex trusts hooks by hash and skips new or changed ones until you review them in /hooks",
			len(rendered), displayHomePath(path, e.home)),
	}}
}

// patchOps converts the pure hooks plan into agent patch operations.
func patchOps(ops []hooks.Op) []agent.PatchOp {
	out := make([]agent.PatchOp, len(ops))

	for i, op := range ops {
		out[i] = agent.PatchOp{Op: op.Op, Path: op.Path, Value: op.Value}
	}

	return out
}

// resolveHookTarget follows a symlinked hooks file to its target so a write
// never replaces the link; a broken link is an error.
func resolveHookTarget(path string) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return path, nil
	}

	if err != nil {
		return "", err
	}

	if info.Mode()&fs.ModeSymlink == 0 {
		return path, nil
	}

	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("broken symlink: %w", err)
	}

	return target, nil
}

// hookFilePerm preserves the mode of an existing hooks file and defaults to
// 0600 for a new one.
func hookFilePerm(path string) fs.FileMode {
	if info, err := os.Stat(path); err == nil {
		return info.Mode().Perm()
	}

	return 0o600
}
