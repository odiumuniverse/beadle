package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/skill"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// Adoption actions reported by adopt and unadopt.
const (
	adoptDone    = "adopted"
	adoptPlan    = "would-adopt"
	adoptSkipped = "skipped"
	unadoptDone  = "restored"
	unadoptPlan  = "would-restore"
	unadoptKept  = "kept"
)

// AdoptResult is one host's outcome of an adoption or of a rollback.
type AdoptResult struct {
	Agent    string `json:"agent"`
	Name     string `json:"name"`
	Action   string `json:"action"`
	Provider string `json:"provider,omitempty"`
	Note     string `json:"note,omitempty"`
}

// Adopt moves the foreign copy of a canon skill on the given hosts into the
// vault stash, so the ordinary sync delivers the name through beadle. The
// original is never deleted: unadopt moves it back. Nothing is adopted
// automatically — only this explicit command touches a foreign copy.
func (e *Engine) Adopt(ctx context.Context, name, hostName string, dryRun bool) (Report, error) {
	var report Report

	release, err := e.lock(ctx)
	if err != nil {
		return report, err
	}

	defer release()

	targets, err := e.adoptionTargets(ctx, hostName)
	if err != nil {
		return report, err
	}

	st, canon, err := e.adoptionContext()
	if err != nil {
		return report, err
	}

	tree, ok := canon[name]
	if !ok {
		return report, fmt.Errorf("skill %s is not in the canon", name)
	}

	digest := skill.TreeDigest(tree)

	staged, adoptable, err := e.collectAdoptions(&report, targets, name, digest, hostName, dryRun, st, canon)
	if err != nil {
		return report, err
	}

	if !adoptable {
		return report, fmt.Errorf("no adoptable foreign copy of %s; nothing changed", name)
	}

	if dryRun || len(staged) == 0 {
		return report, nil
	}

	st.Adoptions = append(st.Adoptions, staged...)

	if err := st.Save(e.vault.StatePath()); err != nil {
		e.unstashAdoptions(staged)

		return report, err
	}

	return report, nil
}

// collectAdoptions resolves every target and returns the records to stash.
// With an explicit host a refusal is an error; otherwise the host is skipped
// with a warning and the other hosts still proceed. A dry run cannot move the
// copy, so it simulates the moves: a provider already planned for one host is
// reported as adopted there and skipped for the next, exactly like the real
// run where the first stash removes the copy from the other read areas.
func (e *Engine) collectAdoptions(
	report *Report, targets []*agent.Agent, name string, digest cas.Hash,
	hostName string, dryRun bool, st *state.State, canon map[string]skill.Tree,
) ([]state.Adoption, bool, error) {
	var (
		staged    []state.Adoption
		planned   map[string]string
		adoptable bool
	)

	if dryRun {
		planned = map[string]string{}
	}

	for _, a := range targets {
		record, note := e.adoptable(a, name, digest, st, canon)
		if record.Provider == "" {
			if hostName != "" {
				return nil, false, errors.New(note)
			}

			report.Warnings = append(report.Warnings, a.ID+": "+note)
			report.Adoptions = append(report.Adoptions, AdoptResult{Agent: a.ID, Name: name, Action: adoptSkipped, Note: note})

			continue
		}

		if owner, taken := planned[record.Provider]; taken {
			note = fmt.Sprintf("the copy at %s is already adopted for %s", record.Provider, owner)

			report.Warnings = append(report.Warnings, a.ID+": "+note)
			report.Adoptions = append(report.Adoptions, AdoptResult{Agent: a.ID, Name: name, Action: adoptSkipped, Note: note})

			continue
		}

		adoptable = true

		if dryRun {
			planned[record.Provider] = a.ID

			report.Adoptions = append(report.Adoptions, AdoptResult{Agent: a.ID, Name: name, Action: adoptPlan, Provider: record.Provider, Note: note})

			continue
		}

		if err := e.stashAdoption(record); err != nil {
			e.unstashAdoptions(staged)

			return nil, false, err
		}

		staged = append(staged, record)
		report.Adoptions = append(report.Adoptions, AdoptResult{Agent: a.ID, Name: name, Action: adoptDone, Provider: record.Provider, Note: note})
	}

	return staged, adoptable, nil
}

// Unadopt moves an adopted original back to its surface. The record stays
// when the original is gone or when the slot holds something beadle does not
// own: nothing of somebody else's is overwritten.
func (e *Engine) Unadopt(ctx context.Context, name, hostName string, dryRun bool) (Report, error) {
	var report Report

	release, err := e.lock(ctx)
	if err != nil {
		return report, err
	}

	defer release()

	hostID := ""

	if hostName != "" {
		target, err := e.adoptionTarget(hostName)
		if err != nil {
			return report, err
		}

		hostID = target.ID
	}

	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		return report, err
	}

	records := e.adoptionRecords(st, name, hostID)
	if len(records) == 0 {
		if hostID != "" {
			return report, fmt.Errorf("no adoption record of %s for %s", name, hostID)
		}

		return report, fmt.Errorf("no adoption record of %s", name)
	}

	done, err := e.restoreAdoptions(&report, records, name, dryRun, st)
	if err != nil {
		return report, err
	}

	if dryRun || len(done) == 0 {
		return report, nil
	}

	if err := st.Save(e.vault.StatePath()); err != nil {
		e.restashAdoptions(done)

		return report, err
	}

	return report, nil
}

// restoreAdoptions validates and applies every rollback, returning the
// records that left the state.
func (e *Engine) restoreAdoptions(report *Report, records []state.Adoption, name string, dryRun bool, st *state.State) ([]state.Adoption, error) {
	var done []state.Adoption

	for _, record := range records {
		plan, note, ok := e.restorable(record, st)
		if !ok {
			report.Warnings = append(report.Warnings, record.Host+": "+note)
			report.Adoptions = append(report.Adoptions, AdoptResult{Agent: record.Host, Name: name, Action: unadoptKept, Provider: record.Provider, Note: note})

			continue
		}

		if dryRun {
			report.Adoptions = append(report.Adoptions, AdoptResult{Agent: record.Host, Name: name, Action: unadoptPlan, Provider: record.Provider, Note: note})

			continue
		}

		if err := e.applyRestore(plan); err != nil {
			e.restashAdoptions(done)

			return nil, err
		}

		st.RemoveAdoption(record.Host, name)
		done = append(done, record)

		report.Adoptions = append(report.Adoptions, AdoptResult{Agent: record.Host, Name: name, Action: unadoptDone, Provider: record.Provider, Note: note})
	}

	return done, nil
}

// adoptionContext loads the state and the canon trees the adoption commands
// work with.
func (e *Engine) adoptionContext() (*state.State, map[string]skill.Tree, error) {
	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		return nil, nil, err
	}

	items, _, err := e.loadVault(kind.Skills)
	if err != nil {
		return nil, nil, err
	}

	return st, skill.Group(items), nil
}

// adoptionTargets resolves the host flag: empty means every active host.
func (e *Engine) adoptionTargets(ctx context.Context, hostName string) ([]*agent.Agent, error) {
	active, err := e.activeAgents(ctx)
	if err != nil {
		return nil, err
	}

	if hostName == "" {
		return active, nil
	}

	target, err := e.adoptionTarget(hostName)
	if err != nil {
		return nil, err
	}

	if !slices.ContainsFunc(active, func(a *agent.Agent) bool { return a.ID == target.ID }) {
		return nil, fmt.Errorf("host %s is not active (enable it or start the agent first)", target.ID)
	}

	return []*agent.Agent{target}, nil
}

// adoptionTarget resolves an agent id or a bundle host alias.
func (e *Engine) adoptionTarget(hostName string) (*agent.Agent, error) {
	id := strings.TrimSpace(hostName)

	if host, err := bundle.ParseHost(id); err == nil {
		id = host.AgentID()
	}

	if a := agent.ByID(e.agents, id); a != nil {
		return a, nil
	}

	return nil, fmt.Errorf("unknown host %q (expected an agent id or claude/gemini/antigravity)", hostName)
}

// adoptable classifies one host for adoption: it returns the record to write
// when a matching foreign copy is there, or the refusal reason otherwise.
func (e *Engine) adoptable(a *agent.Agent, name string, digest cas.Hash, st *state.State, canon map[string]skill.Tree) (state.Adoption, string) {
	surface := a.Surface(kind.Skills)
	if surface == nil {
		return state.Adoption{}, "the host has no skills surface"
	}

	if _, taken := st.AdoptionFor(a.ID, name); taken {
		return state.Adoption{}, fmt.Sprintf("skill %s is already adopted for %s; run beadle skills unadopt %s --host %s first", name, a.ID, name, a.ID)
	}

	vis := e.resolveSkillVisibility(a, surface, st, canon)

	prov, ok := vis.foreignCoverage()[name]
	if !ok {
		return state.Adoption{}, refusalReason(a.ID, name, vis)
	}

	link, symlink := skillLinkTarget(prov.dir, name)

	record := state.Adoption{
		Host:     a.ID,
		Name:     name,
		Provider: prov.path,
		Digest:   digest,
		At:       e.now().UTC(),
	}

	if symlink {
		record.Target = link
	}

	return record, ""
}

// refusalReason explains why a host has nothing to adopt.
func refusalReason(host, name string, vis visibility) string {
	res, known := vis.Skills[name]
	if !known {
		return fmt.Sprintf("no readable copy of %s for %s", name, host)
	}

	if forks := res.visibleForks(vis.Caps, vis.order()); len(forks) > 0 {
		paths := make([]string, 0, len(forks))

		for _, fork := range forks {
			paths = append(paths, fork.path)
		}

		return fmt.Sprintf("skill %s differs between the canon and %s; align the copy or remove it manually", name, strings.Join(paths, ", "))
	}

	if len(res.Matches) > 0 {
		return fmt.Sprintf("beadle already delivers %s to %s (%s); nothing to adopt", name, host, strings.Join(matchClasses(res.Matches), ", "))
	}

	return fmt.Sprintf("no readable copy of %s for %s; nothing to adopt", name, host)
}

// matchClasses lists the channels that already hold a matching copy.
func matchClasses(matches []provider) []string {
	var classes []string

	for _, p := range matches {
		if class := string(p.class); !slices.Contains(classes, class) {
			classes = append(classes, class)
		}
	}

	slices.Sort(classes)

	return classes
}

// stashAdoption moves one foreign copy into the vault stash. The move stays
// on one filesystem: a cross-device vault is an explicit error, never a
// silent copy of foreign bytes.
func (e *Engine) stashAdoption(record state.Adoption) error {
	stash := e.adoptionStashPath(record.Host, record.Name)

	if err := os.MkdirAll(filepath.Dir(stash), 0o700); err != nil {
		return fmt.Errorf("create the adoption stash: %w", err)
	}

	if err := os.Rename(record.Provider, stash); err != nil {
		return fmt.Errorf("move %s into the vault stash (the vault and the surface must share a filesystem): %w", record.Provider, err)
	}

	return nil
}

// unstashAdoptions rolls the moves back in reverse order (best effort).
func (e *Engine) unstashAdoptions(records []state.Adoption) {
	for _, record := range slices.Backward(records) {
		if err := e.stashBack(record); err != nil {
			e.log.Error(context.Background(), "roll back an adoption", "provider", record.Provider, "err", err)
		}
	}
}

// restashAdoptions returns the restored originals to the stash when saving
// the state failed.
func (e *Engine) restashAdoptions(records []state.Adoption) {
	for _, record := range slices.Backward(records) {
		if err := e.stashAdoption(record); err != nil {
			e.log.Error(context.Background(), "undo a restore", "provider", record.Provider, "err", err)
		}
	}
}

// stashBack moves a stashed original back to its surface.
func (e *Engine) stashBack(record state.Adoption) error {
	from := e.adoptionStashPath(record.Host, record.Name)

	if err := os.Rename(from, record.Provider); err != nil {
		return fmt.Errorf("move %s back to %s: %w", from, record.Provider, err)
	}

	return nil
}

// adoptionStashPath is the vault path holding the adopted original of one
// host and skill.
func (e *Engine) adoptionStashPath(host, name string) string {
	return filepath.Join(e.vault.AdoptionsDir(), host, name)
}

// adoptionRecords lists the adoption records of one skill, optionally for
// one host.
func (e *Engine) adoptionRecords(st *state.State, name, hostID string) []state.Adoption {
	var records []state.Adoption

	for _, record := range st.Adoptions {
		if record.Name != name {
			continue
		}

		if hostID != "" && record.Host != hostID {
			continue
		}

		records = append(records, record)
	}

	return records
}

// restorePlan is one adopted original ready to go back: the record, the
// beadle-owned copy to clear first, and the symlink to recreate when the
// stash is gone.
type restorePlan struct {
	record  state.Adoption
	remove  string
	symlink string
}

// restorable validates one rollback: the slot may be cleared only when it
// holds beadle's own unmodified delivery, and the original must still exist
// either in the stash or at its recorded symlink target.
func (e *Engine) restorable(record state.Adoption, st *state.State) (restorePlan, string, bool) {
	plan := restorePlan{record: record}

	if entryExists(record.Provider) {
		ok, note := e.clearBeadleCopy(record, st)
		if !ok {
			return plan, note, false
		}

		plan.remove = record.Provider
	}

	stash := e.adoptionStashPath(record.Host, record.Name)

	switch {
	case entryExists(stash):
		return plan, "", true
	case record.Target != "" && entryExists(record.Target):
		plan.symlink = record.Target

		return plan, "the vault stash is gone; recreating the symlink to " + record.Target, true
	default:
		return plan, fmt.Sprintf("the original copy of %s is gone (no stash at %s and no live target); the record stays for manual review", record.Name, stash), false
	}
}

// clearBeadleCopy reports whether the slot can be cleared before the original
// goes back: only beadle's own unmodified delivery is removed, anything else
// is somebody's work and stays.
func (e *Engine) clearBeadleCopy(record state.Adoption, st *state.State) (bool, string) {
	a := agent.ByID(e.agents, record.Host)
	if a == nil {
		return false, fmt.Sprintf("unknown host %s; restore the copy manually", record.Host)
	}

	info, err := os.Lstat(record.Provider)
	if err != nil {
		return true, ""
	}

	if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Sprintf("the slot %s holds something beadle did not deliver; move it away manually", record.Provider)
	}

	if !e.ownCopyMatchesBase(a, record.Provider, record.Name, st) {
		return false, fmt.Sprintf("the copy at %s is not beadle's unmodified delivery; move it away manually", record.Provider)
	}

	return true, ""
}

// ownCopyMatchesBase reports that the copy at path is exactly what the host
// base recorded: beadle's own unmodified delivery.
func (e *Engine) ownCopyMatchesBase(a *agent.Agent, path, name string, st *state.State) bool {
	base, ok := st.Base(kind.Skills, a.ID)
	if !ok || !baseOwns(base, kind.Skills, name) {
		return false
	}

	items, err := e.loadBase(st, kind.Skills, a.ID)
	if err != nil {
		return false
	}

	tree, err := skill.ReadTree(path)
	if err != nil {
		return false
	}

	snapshot := skill.Flatten(map[string]skill.Tree{name: tree})

	return groupMatchesBase(snapshot, items, name, groupFiles(snapshot, name))
}

// applyRestore puts one original back on its surface.
func (e *Engine) applyRestore(plan restorePlan) error {
	if plan.remove != "" {
		if err := os.RemoveAll(plan.remove); err != nil {
			return fmt.Errorf("remove the beadle copy at %s: %w", plan.remove, err)
		}
	}

	if plan.symlink != "" {
		if err := os.Symlink(plan.symlink, plan.record.Provider); err != nil {
			return fmt.Errorf("recreate the symlink %s: %w", plan.record.Provider, err)
		}

		return nil
	}

	return e.stashBack(plan.record)
}

// entryExists reports whether a filesystem entry is there; unlike
// fsutil.Exists it does not follow symlinks, so a stashed dangling link still
// counts.
func entryExists(path string) bool {
	_, err := os.Lstat(path)

	return err == nil
}
