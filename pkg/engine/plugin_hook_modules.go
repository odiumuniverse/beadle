package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/hooks"
	"github.com/odiumuniverse/beadle/pkg/hostcli"
	"github.com/odiumuniverse/beadle/pkg/plugin"
	"github.com/odiumuniverse/beadle/pkg/state"
)

const (
	hookModuleNotePrefix = "hook modules: "
	hookModuleDirName    = "hooks"
	hookModuleFileMode   = 0o600

	// ompHookLoadFailure is the omp stderr marker of a module that did not
	// load. It is matched with the module's hooks/<phase>/<name> suffix, never
	// with an absolute path: omp abbreviates HOME to "~" in the message and
	// its exit code is not informative (live-verified on 18.4.1).
	ompHookLoadFailure = "Failed to load extension"
	// ompHookProbePrompt and ompHookProbeMaxTime bound the headless probe
	// session. The modules are imported before omp needs a model, so the
	// prompt is unused and a bounded session is enough.
	ompHookProbePrompt  = "omp"
	ompHookProbeMaxTime = "5s"
	ompHookProbeTimeout = 30 * time.Second
)

// The warning the delivery and the doctor owe before the first write: omp runs
// hook modules with the user's full rights, asks for no trust of its own, and
// beadle did not audit the bytes it copies.
const (
	hookModuleRightsWarning  = "omp neither sandboxes hook modules nor asks for trust: a module runs with your full rights and can do anything you can"
	hookModuleAuditWarning   = "the bytes come from a plugin beadle did not audit; beadle only copies them and verifies that the module loads"
	hookModuleRestartWarning = "restart omp: a live session keeps running the modules it loaded at start"
)

// hookModuleWanted is one module a run wants on disk.
type hookModuleWanted struct {
	// key is what the state records this module under, and target is where the
	// file lives. They are two things: the record travels between machines and
	// the file does not, so a key that named the file's path would name nothing
	// on the next machine and beadle's own file would look foreign there.
	key    string
	target string
	data   []byte
	digest cas.Hash
	source string
	phase  string
	name   string
}

// deliverHookModules copies the approved hook modules of the installed omp
// plugins into the active omp agent's hooks directory, records what it wrote
// in state, withdraws the modules it owns but no longer wants, and proves the
// fresh modules load. omp is the only host with a module surface; every other
// host keeps its declarative hooks.
func (e *Engine) deliverHookModules(ctx context.Context, st *state.State, report *Report, active []*agent.Agent, opts SyncOptions) {
	if e.home == "" || opts.Direction == config.ModePull {
		return
	}

	host := agent.ByID(active, agent.OmpID)
	if host == nil {
		return
	}

	if detected, err := host.Detect(); err != nil || !detected {
		return
	}

	manifest, err := plugin.ReadAll(e.home)
	if err != nil {
		report.Warnings = append(report.Warnings, hookModuleNote("cannot read the plugin registry: "+err.Error()))

		return
	}

	desired, pending := e.wantedHookModules(manifest, report)

	e.withdrawHookModules(st, report, desired, opts)

	written := e.writeHookModules(st, report, desired, opts)

	if len(pending) > 0 {
		report.Notes = append(report.Notes, pending...)
	}

	if len(written) > 0 && !opts.DryRun {
		e.probeHookModules(ctx, st, report, written)
	}
}

// wantedHookModules resolves the modules this run should deliver: every
// consented module of an installed omp plugin, keyed by its target path.
// Unconsented modules are reported once per plugin and never written.
func (e *Engine) wantedHookModules(manifest plugin.Manifest, report *Report) (map[string]hookModuleWanted, []string) {
	dir := filepath.Join(agent.OmpAgentDir(e.home), hookModuleDirName)

	desired := map[string]hookModuleWanted{}

	pendingByPlugin := map[string]int{}
	pluginOrder := []string{}

	for _, p := range manifest.Plugins {
		if p.Source != plugin.SourceOMP {
			continue
		}

		key := pluginKey(p.Origin, p.Name)
		if key == selfPluginKey {
			continue
		}

		modules, warns := plugin.HookModules(p.Source, p.InstallPath)
		report.Warnings = append(report.Warnings, warns...)

		for _, module := range modules {
			data, err := os.ReadFile(module.Path) //nolint:gosec // G304: the module path is resolved from the plugin install path
			if err != nil {
				report.Warnings = append(report.Warnings, hookModuleNote(fmt.Sprintf(
					"%s: cannot read %s: %v", key, filepath.ToSlash(filepath.Join(hookModuleDirName, module.Phase, module.Name)), err)))

				continue
			}

			digest := cas.HashOf(data)

			if !e.config.HookApproved(hooks.HookModuleKey(key, module.Phase, module.Name, digest)) {
				if pendingByPlugin[key] == 0 {
					pluginOrder = append(pluginOrder, key)
				}

				pendingByPlugin[key]++

				continue
			}

			target := filepath.Join(dir, module.Phase, module.Name)
			record := state.HomeKey(target, e.home)
			desired[record] = hookModuleWanted{
				key: record, target: target,
				data: data, digest: digest, source: key, phase: module.Phase, name: module.Name,
			}
		}
	}

	pending := make([]string, 0, len(pluginOrder))

	for _, key := range pluginOrder {
		pending = append(pending, fmt.Sprintf(
			"%splugin %s ships %d hook module(s) awaiting consent (%s; %s); approve with `beadle hooks approve --plugin %s`, then restart omp",
			hookModuleNotePrefix, key, pendingByPlugin[key], hookModuleRightsWarning, hookModuleAuditWarning, key))
	}

	return desired, pending
}

// withdrawHookModules removes the recorded modules this run no longer wants:
// the source plugin is gone or the consent was revoked. A file whose digest is
// not the recorded one is foreign and is never removed; only the record goes.
func (e *Engine) withdrawHookModules(st *state.State, report *Report, desired map[string]hookModuleWanted, opts SyncOptions) {
	removed := 0

	for _, key := range slices.Sorted(maps.Keys(st.HookModules)) {
		if _, wanted := desired[key]; wanted {
			continue
		}

		record := st.HookModules[key]

		if !opts.DryRun {
			target := state.HomePath(key, e.home)
			if err := removeOwnedHookModule(target, record.Digest); err != nil {
				report.Warnings = append(report.Warnings, hookModuleNote(fmt.Sprintf("%s: %v", displayHomePath(target, e.home), err)))

				continue
			}

			delete(st.HookModules, key)
		}

		removed++
	}

	if len(st.HookModules) == 0 {
		st.HookModules = nil
	}

	if removed > 0 {
		verb := "removed"
		if opts.DryRun {
			verb = "would remove"
		}

		report.Notes = append(report.Notes, hookModuleNote(fmt.Sprintf("%s %d module(s)", verb, removed)))
	}
}

// hookModuleDecision is what one wanted module target needs.
type hookModuleDecision int

const (
	// hookModuleKeep leaves the target as it is: our file already matches.
	hookModuleKeep hookModuleDecision = iota
	// hookModuleForeign leaves a file beadle never wrote.
	hookModuleForeign
	// hookModuleReplaced leaves a file someone put in our record's place.
	hookModuleReplaced
	// hookModuleWrite writes (or rewrites) the wanted bytes.
	hookModuleWrite
)

// decideHookModule classifies one wanted module target. A file with no record
// of ours, or one whose bytes no longer match the record, is foreign: beadle
// leaves it and delivers nothing there.
func decideHookModule(current []byte, present bool, record state.HookModule, ours bool, want hookModuleWanted) (hookModuleDecision, string) {
	switch {
	case present && !ours:
		return hookModuleForeign, "already exists and is not beadle's; left untouched"
	case ours && present && cas.HashOf(current) != record.Digest:
		return hookModuleReplaced, "changed since beadle wrote it; left untouched"
	case ours && present && record.Digest == want.digest:
		return hookModuleKeep, ""
	default:
		return hookModuleWrite, ""
	}
}

// writeHookModules writes the wanted modules that are not already on disk with
// the wanted bytes. A target with no record of ours is foreign and is left
// untouched. It returns the modules it actually wrote, for the load probe.
func (e *Engine) writeHookModules(st *state.State, report *Report, desired map[string]hookModuleWanted, opts SyncOptions) []hookModuleWanted {
	var written []hookModuleWanted

	for _, key := range slices.Sorted(maps.Keys(desired)) {
		want := desired[key]

		current, present, err := readOptional(want.target)
		if err != nil {
			report.Warnings = append(report.Warnings, hookModuleNote(fmt.Sprintf("%s: %v", displayHomePath(want.target, e.home), err)))

			continue
		}

		record, ours := st.HookModules[key]
		decision, note := decideHookModule(current, present, record, ours, want)

		switch decision {
		case hookModuleForeign:
			report.Notes = append(report.Notes, hookModuleNote(displayHomePath(want.target, e.home)+" "+note))

			continue
		case hookModuleReplaced:
			// Someone replaced our file: it is foreign now, drop the record
			// and never overwrite it.
			if !opts.DryRun {
				delete(st.HookModules, key)
			}

			report.Notes = append(report.Notes, hookModuleNote(displayHomePath(want.target, e.home)+" "+note))

			continue
		case hookModuleKeep:
			continue
		case hookModuleWrite:
			// Fall through to the write below.
		}

		if opts.DryRun {
			report.Notes = append(report.Notes, hookModuleNote("would write "+displayHomePath(want.target, e.home)))

			continue
		}

		if err := writeHookModule(want.target, want.data); err != nil {
			report.Warnings = append(report.Warnings, hookModuleNote(fmt.Sprintf("%s: %v", displayHomePath(want.target, e.home), err)))

			continue
		}

		if st.HookModules == nil {
			st.HookModules = map[string]state.HookModule{}
		}

		st.HookModules[key] = state.HookModule{Digest: want.digest, Source: want.source, Phase: want.phase, Name: want.name}

		written = append(written, want)
	}

	if len(written) > 0 {
		report.Notes = append(report.Notes, hookModuleNote(fmt.Sprintf(
			"%d module(s) written into %s: %s; %s; %s",
			len(written), displayHomePath(filepath.Join(agent.OmpAgentDir(e.home), hookModuleDirName), e.home),
			hookModuleRightsWarning, hookModuleAuditWarning, hookModuleRestartWarning)))
	}

	return written
}

// probeHookModules proves the modules this run wrote load, because omp reports
// no module health on its own: the only signal is a `Failed to load extension
// <path>` line on the stream of a session start. A failed module is removed and
// its record dropped, so a broken delivery never stays where omp would load
// it. A missing CLI is unverifiable, never a success.
func (e *Engine) probeHookModules(ctx context.Context, st *state.State, report *Report, written []hookModuleWanted) {
	bin, err := e.hostCLI(bundle.Omp)
	if errors.Is(err, hostcli.ErrNotFound) {
		report.Notes = append(report.Notes, hookModuleNote(
			"cannot verify the delivered module(s) load: the omp CLI is not on PATH (unverifiable)"))

		return
	}

	if err != nil {
		report.Warnings = append(report.Warnings, hookModuleNote("cannot resolve the omp CLI: "+err.Error()))

		return
	}

	probeCtx, cancel := context.WithTimeout(ctx, ompHookProbeTimeout)
	defer cancel()

	output, notFound := hookModuleProbe(probeCtx, bin, []string{"-p", ompHookProbePrompt, "--max-time", ompHookProbeMaxTime})
	if notFound {
		report.Notes = append(report.Notes, hookModuleNote(
			"cannot verify the delivered module(s) load: the omp CLI vanished (unverifiable)"))

		return
	}

	failed := map[string]string{}

	for line := range strings.SplitSeq(string(output), "\n") {
		if !strings.Contains(line, ompHookLoadFailure) {
			continue
		}

		for _, want := range written {
			// omp abbreviates HOME to "~", so the module is matched by its
			// hooks/<phase>/<name> suffix, never by an absolute prefix.
			if strings.Contains(line, filepath.Join(hookModuleDirName, want.phase, want.name)) {
				failed[want.key] = strings.TrimSpace(line)
			}
		}
	}

	if len(failed) == 0 {
		report.Notes = append(report.Notes, hookModuleNote(fmt.Sprintf(
			"verified %d delivered module(s) load on omp", len(written))))

		return
	}

	for _, key := range slices.Sorted(maps.Keys(failed)) {
		record := st.HookModules[key]
		target := state.HomePath(key, e.home)

		if err := removeOwnedHookModule(target, record.Digest); err != nil {
			report.Warnings = append(report.Warnings, hookModuleNote(fmt.Sprintf("%s: %v", displayHomePath(target, e.home), err)))
		}

		delete(st.HookModules, key)

		report.Warnings = append(report.Warnings, hookModuleNote(fmt.Sprintf(
			"delivery failed: %s did not load: %s", displayHomePath(target, e.home), failed[key])))
	}
}

// hookModuleIssues reports the module consent and drift the doctor owes: a
// plugin whose modules await approval, a delivered module that changed or
// vanished, and a recorded module whose plugin is retired.
func (e *Engine) hookModuleIssues() []Issue {
	if e.home == "" {
		return nil
	}

	manifest, err := plugin.ReadAll(e.home)
	if err != nil {
		return nil
	}

	installed := map[string]bool{}

	var issues []Issue

	for _, p := range manifest.Plugins {
		if p.Source != plugin.SourceOMP {
			continue
		}

		key := pluginKey(p.Origin, p.Name)
		if key == selfPluginKey {
			continue
		}

		installed[key] = true

		modules, _ := plugin.HookModules(p.Source, p.InstallPath)

		pending := 0

		for _, module := range modules {
			data, err := os.ReadFile(module.Path) //nolint:gosec // G304: resolved from the plugin install path
			if err != nil {
				continue
			}

			if !e.config.HookApproved(hooks.HookModuleKey(key, module.Phase, module.Name, cas.HashOf(data))) {
				pending++
			}
		}

		if pending > 0 {
			issues = append(issues, Issue{Severity: SeverityInfo, Agent: agent.OmpID, Message: fmt.Sprintf(
				"%splugin %s ships %d hook module(s) awaiting consent: %s; %s; approve with `beadle hooks approve --plugin %s`, then restart omp",
				hookModuleNotePrefix, key, pending, hookModuleRightsWarning, hookModuleAuditWarning, key)})
		}
	}

	issues = append(issues, e.hookModuleDriftIssues(installed)...)

	return issues
}

// hookModuleDriftIssues reports the recorded modules that are missing, changed
// or whose source plugin is retired: the next sync withdraws the retired ones
// and a user fix restores a missing one.
func (e *Engine) hookModuleDriftIssues(installed map[string]bool) []Issue {
	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		return nil
	}

	var issues []Issue

	for _, key := range slices.Sorted(maps.Keys(st.HookModules)) {
		record := st.HookModules[key]
		target := state.HomePath(key, e.home)

		if !installed[record.Source] {
			issues = append(issues, Issue{Severity: SeverityInfo, Agent: agent.OmpID, Message: fmt.Sprintf(
				"%s%s comes from retired plugin %s; the next sync withdraws it",
				hookModuleNotePrefix, displayHomePath(target, e.home), record.Source)})

			continue
		}

		data, present, err := readOptional(target)
		if err != nil {
			continue
		}

		if !present {
			issues = append(issues, Issue{Severity: SeverityWarn, Agent: agent.OmpID, Message: fmt.Sprintf(
				"%s%s is missing; run `beadle sync` to restore it", hookModuleNotePrefix, displayHomePath(target, e.home))})

			continue
		}

		if cas.HashOf(data) != record.Digest {
			issues = append(issues, Issue{Severity: SeverityWarn, Agent: agent.OmpID, Message: fmt.Sprintf(
				"%s%s changed since beadle wrote it; beadle leaves it and delivers nothing there", hookModuleNotePrefix, displayHomePath(target, e.home))})
		}
	}

	return issues
}

// removeOwnedHookModule deletes a delivered module only when it still carries
// the digest beadle wrote: a file someone replaced is foreign and stays.
func removeOwnedHookModule(target string, digest cas.Hash) error {
	data, present, err := readOptional(target)
	if err != nil || !present {
		return err
	}

	if cas.HashOf(data) != digest {
		return nil
	}

	if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	return nil
}

// writeHookModule writes one module atomically, creating the phase directory.
func writeHookModule(target string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}

	return fsutil.WriteFileAtomic(target, data, hookModuleFileMode)
}

func hookModuleNote(note string) string {
	return hookModuleNotePrefix + note
}

// hookModuleProber runs the headless probe session; tests replace it.
type hookModuleProber func(ctx context.Context, bin hostcli.Binary, args []string) (output []byte, notFound bool)

var hookModuleProbe hookModuleProber = execHookModuleProbe

// execHookModuleProbe runs the resolved omp binary and returns stdout and
// stderr concatenated: the module-load failure is a diagnostic line and which
// stream carries it is a host detail. A binary that vanished is notFound.
func execHookModuleProbe(ctx context.Context, bin hostcli.Binary, args []string) ([]byte, bool) {
	var buf bytes.Buffer

	cmd := exec.CommandContext(ctx, bin.Path, args...) //nolint:gosec // G204: only a resolved host CLI runs
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if bin.PATH != "" {
		cmd.Env = append(os.Environ(), "PATH="+bin.PATH)
	}

	err := cmd.Run()

	switch {
	case err == nil:
		return buf.Bytes(), false
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, exec.ErrNotFound):
		return buf.Bytes(), true
	default:
		return buf.Bytes(), false
	}
}
