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

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/skill"
)

const forkMigratingSuffix = ".migrating"

// skillsDirs lists the host skills directories this sync may touch. modeGated
// skips the surfaces the user turned off (the doctor asks that way); the
// migrations pass false, because pruning must run whatever the mode.
func (e *Engine) skillsDirs(ctx context.Context, modeGated bool) ([]string, error) {
	if e.home == "" {
		return nil, nil
	}

	active, err := e.activeAgents(ctx)
	if err != nil {
		return nil, err
	}

	var dirs []string

	for _, a := range active {
		surface := a.Surface(kind.Skills)
		if surface == nil {
			continue
		}

		if modeGated && e.config.ModeFor(a.ID, kind.Skills, surface.Traits().DefaultMode) == config.ModeOff {
			continue
		}

		dirs = append(dirs, surface.Path())
	}

	return dirs, nil
}

// joinNotes folds the notes of one row into the single line a report prints.
func joinNotes(notes []string) string {
	return strings.Join(slices.DeleteFunc(notes, func(note string) bool { return note == "" }), "; ")
}

// linkReason explains why a cache link can or cannot be repointed to its vault
// pivot.
type linkReason int

const (
	linkMigratable linkReason = iota
	linkPluginNotParked
	linkOwnerTaken
	linkSkillGone
)

type MigrateResult struct {
	Key      string `json:"key"`
	Migrated int    `json:"migrated,omitempty"`
	Note     string `json:"note,omitempty"`
}

type migrateState struct {
	engine *Engine
	plan   farmPlan
	ledger pluginLedger
	agent  string
	dryRun bool
	moved  map[string]int
	notes  map[string][]string
}

func cacheSkillTarget(home, target string) (string, string, bool) {
	target = filepath.Clean(strings.TrimSuffix(target, "/"))

	root := filepath.Join(home, ".claude", "plugins", "cache")

	rest, ok := strings.CutPrefix(target, root+string(filepath.Separator))
	if !ok {
		return "", "", false
	}

	parts := strings.Split(rest, "/")
	if len(parts) != 5 || parts[2] == "" || parts[3] != farmSkillsDir {
		return "", "", false
	}

	marketplace, name, version, skillName := parts[0], parts[1], parts[2], parts[4]
	if !validPluginKey(marketplace, name) || !filepath.IsLocal(version) || !filepath.IsLocal(skillName) {
		return "", "", false
	}

	return pluginKey(marketplace, name), skillName, true
}

// cacheLinkKey reads a link target that points into a host's plugin cache. The
// path shape is the whole test: a link into a cache whose plugin is not
// installed any more is exactly the case a user needs reported, so the key is
// recognised whether or not anything is registered under it. Whether the
// plugin is parked is the plan's answer, not this one's.
func (e *Engine) cacheLinkKey(link string) (string, string, bool) {
	return cacheSkillTarget(e.home, link)
}

// skillsDirAgents maps every active host skills directory to its agent.
// modeGated is true for the callers that must leave mode-off surfaces alone
// (doctor); pruning runs regardless of the skills mode, so heal and migrate
// pass false.
func (e *Engine) skillsDirAgents(ctx context.Context, modeGated bool) (map[string]string, error) {
	if e.home == "" {
		return nil, nil
	}

	active, err := e.activeAgents(ctx)
	if err != nil {
		return nil, err
	}

	out := map[string]string{}

	for _, a := range active {
		surface := a.Surface(kind.Skills)
		if surface == nil {
			continue
		}

		if modeGated && e.config.ModeFor(a.ID, kind.Skills, surface.Traits().DefaultMode) == config.ModeOff {
			continue
		}

		out[surface.Path()] = a.ID
	}

	return out, nil
}

// MigrateHostSkills moves the host skills that are symlinks into a plugin
// cache into the vault: the canon keeps the content, the link points at the
// canon, and a link chain or a fork the canon cannot hold is left exactly as
// it is. It was `beadle heal`'s job while the farm existed and the farm is
// gone, so it is the engine's own operation now.
//
// dryRun reports what would move without touching anything.
func (e *Engine) MigrateHostSkills(ctx context.Context, dryRun bool) ([]MigrateResult, error) {
	release, err := e.lock(ctx)
	if err != nil {
		return nil, err
	}

	defer release()

	results, warns := e.migrateHosts(ctx, dryRun)
	if len(warns) > 0 {
		return results, errors.New(joinNotes(warns))
	}

	return results, nil
}

func (e *Engine) migrateHosts(ctx context.Context, dryRun bool) ([]MigrateResult, []string) {
	if e.home == "" {
		return nil, nil
	}

	// The links this migration follows point into a host's plugin cache, so
	// the host registries are the record of what is installed. The farm's
	// ledger used to answer that and nothing writes it any more.
	ledger := e.hostPluginLedger()

	plan, _ := e.buildFarmPlan(ledger)

	dirs, err := e.skillsDirs(ctx, false)
	if err != nil {
		return nil, []string{"plugin migrate: " + err.Error()}
	}

	agents, err := e.skillsDirAgents(ctx, false)
	if err != nil {
		return nil, []string{"plugin migrate: " + err.Error()}
	}

	state := migrateState{
		engine: e,
		plan:   plan,
		ledger: ledger,
		dryRun: dryRun,
		moved:  map[string]int{},
		notes:  map[string][]string{},
	}

	var warns []string

	for _, dir := range dirs {
		state.agent = agents[dir]

		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}

			warns = append(warns, fmt.Sprintf("plugin migrate: cannot read %s: %v", dir, err))

			continue
		}

		for _, entry := range entries {
			name := entry.Name()
			path := filepath.Join(dir, name)

			switch {
			case entry.Type()&fs.ModeSymlink != 0:
				state.link(path, name)
			case entry.IsDir():
				state.fork(path, name)
			}
		}
	}

	return state.results(), warns
}

func (m *migrateState) link(path, name string) {
	link, err := os.Readlink(path)
	if err != nil {
		return
	}

	key, skillName, ok := m.engine.cacheLinkKey(link)
	if !ok {
		return
	}

	if reason := m.engine.linkReason(m.agent, m.plan, key, skillName); reason != linkMigratable {
		m.noteUnmigratable(reason, key, skillName, name)

		return
	}

	target, _ := m.engine.agentSkillTarget(m.agent, key, skillName)

	// A link that already points at the plugin's install is done. Without this
	// the migration would report the same move on every run, because its
	// destination is the same cache path the link followed all along.
	if samePath(filepath.Clean(strings.TrimSuffix(link, "/")), target) {
		return
	}

	if m.dryRun {
		m.moved[key]++

		return
	}

	if err := fsutil.ReplaceSymlink(path, target); err != nil {
		m.note(key, "cannot repoint %s: %v", path, err)

		return
	}

	m.moved[key]++
}

func (m *migrateState) fork(path, name string) {
	if _, stub := skill.IsStubDir(path); stub {
		return
	}

	key, ok := m.plan.Owner[name]
	if !ok {
		return
	}

	lot, ok := m.engine.agentSkillTarget(m.agent, key, name)
	if !ok {
		m.note(key, "cannot resolve the pivot of %s; keeping the copy %s", key, name)

		return
	}

	if !m.engine.sameAsLot(path, lot) {
		m.note(key, "drifted copy of %s; keeping the local version", key)

		return
	}

	if !m.engine.canonAllowsFork(m.plan, name, lot) {
		m.note(key, "matches %s but the vault copy differs; keeping both", key)

		return
	}

	if m.dryRun {
		m.moved[key]++

		return
	}

	note, replaced := replaceFork(path, lot)
	if note != "" {
		m.note(key, "%s", note)
	}

	if !replaced {
		return
	}

	m.moved[key]++
}

// replaceFork swaps the fork directory at path for a symlink to lot without a
// window where neither exists: the fork is renamed aside first, the symlink is
// created at path, and only then the aside copy is removed. When the symlink
// cannot be created the fork is renamed back. It returns a note describing a
// leftover or a failure, and false when the swap failed and the fork stayed at
// path.
func replaceFork(path, lot string) (string, bool) {
	aside := path + forkMigratingSuffix

	if err := os.Rename(path, aside); err != nil {
		return fmt.Sprintf("cannot replace %s: %v", path, err), false
	}

	if err := fsutil.ReplaceSymlink(path, lot); err != nil {
		if restoreErr := os.Rename(aside, path); restoreErr != nil {
			return fmt.Sprintf("cannot replace %s: %v; the fork could not be restored: %v", path, err, restoreErr), false
		}

		return fmt.Sprintf("cannot replace %s: %v; the fork was restored", path, err), false
	}

	if err := os.RemoveAll(aside); err != nil {
		return fmt.Sprintf("the fork was replaced, but %s was left behind: %v", aside, err), true
	}

	return "", true
}

func (m *migrateState) noteUnmigratable(reason linkReason, key, skillName, name string) {
	m.note(key, "%s; %s left in place", m.engine.linkReasonText(m.plan, reason, key, skillName), name)
}

// results is one row per plugin the migration touched: the links and forks it
// moved, and the ones it could not move but has something to say about. A key
// that only has a note still gets a row - a fork the user was told about is
// better than a silence they have to notice themselves.
func (m *migrateState) results() []MigrateResult {
	keys := slices.Sorted(maps.Keys(m.moved))

	for key := range m.notes {
		if _, moved := m.moved[key]; !moved {
			keys = append(keys, key)
		}
	}

	slices.Sort(keys)

	var results []MigrateResult

	for _, key := range slices.Compact(keys) {
		notes := slices.Compact(slices.Sorted(slices.Values(m.notes[key])))

		results = append(results, MigrateResult{
			Key:      key,
			Migrated: m.moved[key],
			Note:     strings.Join(notes, "; "),
		})
	}

	return results
}

func (m *migrateState) note(key, format string, args ...any) {
	m.notes[key] = append(m.notes[key], fmt.Sprintf(format, args...))
}

// linkReason classifies why a cache link or fork can or cannot be pointed at
// the plugin's own install.
func (e *Engine) linkReason(agentID string, plan farmPlan, key, skillName string) linkReason {
	if _, parked := plan.Parked[key]; !parked {
		return linkPluginNotParked
	}

	if owner, taken := plan.Owner[skillName]; taken && owner != key {
		return linkOwnerTaken
	}

	lot, ok := e.agentSkillTarget(agentID, key, skillName)
	if !ok || !isDir(lot) {
		return linkSkillGone
	}

	return linkMigratable
}

// linkReasonText renders the shared reason clause used both in heal notes and
// in doctor issues for a non-migratable cache link.
func (e *Engine) linkReasonText(plan farmPlan, reason linkReason, key, skillName string) string {
	switch reason {
	case linkPluginNotParked:
		return fmt.Sprintf("plugin %s is not parked", key)
	case linkOwnerTaken:
		return fmt.Sprintf("skill %s is provided by %s", skillName, plan.Owner[skillName])
	default:
		return fmt.Sprintf("plugin %s no longer offers skill %s", key, skillName)
	}
}

func (e *Engine) canonAllowsFork(plan farmPlan, name, lot string) bool {
	if _, canon := plan.Canon[normSkillName(name)]; !canon {
		return true
	}

	return e.sameAsLot(filepath.Join(e.vault.SkillsDir(), name), lot)
}

func (e *Engine) sameAsLot(dir, lot string) bool {
	lotTree, err := skill.ReadTree(lot)
	if err != nil {
		return false
	}

	tree, err := skill.ReadTree(dir)
	if err != nil {
		return false
	}

	if !maps.Equal(skill.ManifestOf(tree), skill.ManifestOf(lotTree)) {
		return false
	}

	return !hasExtraEntries(dir, lotTree)
}

func hasExtraEntries(dir string, lot skill.Tree) bool {
	extra := false

	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			extra = true

			return fs.SkipAll
		}

		if path == dir {
			return nil
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			extra = true

			return fs.SkipAll
		}

		slashed := filepath.ToSlash(rel)

		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			extra = true

			return fs.SkipAll
		case entry.IsDir():
			if !lotHasUnder(lot, slashed+"/") {
				extra = true

				return fs.SkipAll
			}
		default:
			if _, ok := lot[slashed]; !ok {
				extra = true

				return fs.SkipAll
			}
		}

		return nil
	})
	if err != nil {
		return true
	}

	return extra
}

// pluginMigrationIssues reports what the host-skills migration would do: the
// links and forks that still point into a plugin cache, and the ones it would
// refuse. Everything comes from the host registries, the same record the
// migration itself uses - the vault's farm ledger no longer describes what is
// installed.
func (e *Engine) pluginMigrationIssues(ctx context.Context) []Issue {
	if e.home == "" {
		return nil
	}

	ledger := e.hostPluginLedger()

	plan, _ := e.buildFarmPlan(ledger)

	dirs, err := e.skillsDirs(ctx, true)
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Message: "plugin migration: " + err.Error()}}
	}

	agents, err := e.skillsDirAgents(ctx, true)
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Message: "plugin migration: " + err.Error()}}
	}

	var issues []Issue

	for _, dir := range dirs {
		agentID := agents[dir]

		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}

			issues = append(issues, Issue{Severity: SeverityWarn, Message: fmt.Sprintf("plugin migration: cannot read %s: %v", dir, err)})

			continue
		}

		for _, entry := range entries {
			name := entry.Name()
			path := filepath.Join(dir, name)

			switch {
			case entry.Type()&fs.ModeSymlink != 0:
				issues = append(issues, e.migrationLinkIssues(agentID, plan, path, name)...)
			case entry.IsDir():
				issues = append(issues, e.migrationForkIssues(agentID, plan, path, name)...)
			}
		}
	}

	return issues
}

// migrationLinkIssues reports a link into a plugin cache that the migration
// would refuse: a plugin that is not installed any more, a skill another
// plugin owns, or a skill the plugin no longer offers. A link that resolves to
// the plugin's own skill is exactly where it belongs - the plugin manager
// keeps the content there - so it is not reported.
func (e *Engine) migrationLinkIssues(agentID string, plan farmPlan, path, name string) []Issue {
	link, err := os.Readlink(path)
	if err != nil {
		return nil
	}

	key, skillName, ok := e.cacheLinkKey(link)
	if !ok {
		return nil
	}

	reason := e.linkReason(agentID, plan, key, skillName)
	if reason == linkMigratable {
		return nil
	}

	return []Issue{{Severity: SeverityWarn, Message: fmt.Sprintf("skill %s points into the plugin cache; %s (remove the stale link, or let the plugin offer the skill again)", name, e.linkReasonText(plan, reason, key, skillName))}}
}

func (e *Engine) migrationForkIssues(agentID string, plan farmPlan, path, name string) []Issue {
	if _, stub := skill.IsStubDir(path); stub {
		return nil
	}

	key, ok := plan.Owner[name]
	if !ok {
		return nil
	}

	lot, ok := e.agentSkillTarget(agentID, key, name)
	if !ok {
		return nil
	}

	matches := e.sameAsLot(path, lot)
	if matches && e.canonAllowsFork(plan, name, lot) {
		return nil
	}

	if matches {
		return []Issue{{Severity: SeverityWarn, Message: fmt.Sprintf("skill %s matches %s but the vault copy differs; keeping both", name, key)}}
	}

	return []Issue{{Severity: SeverityWarn, Message: fmt.Sprintf("skill %s looks like a drifted copy of %s; keeping the local version", name, key)}}
}

func lotHasUnder(lot skill.Tree, prefix string) bool {
	for key := range maps.Keys(lot) {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}

	return false
}
