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

	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/skill"
)

const forkMigratingSuffix = ".migrating"

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

func (e *Engine) cacheLinkKey(link string, ledger pluginLedger) (string, string, bool) {
	key, skillName, ok := cacheSkillTarget(e.home, link)
	if !ok {
		return "", "", false
	}

	if _, inLedger := ledger.Plugins[key]; !inLedger {
		return "", "", false
	}

	return key, skillName, true
}

func (e *Engine) migrateHosts(ctx context.Context, dryRun bool) ([]MigrateResult, []string) {
	if e.home == "" {
		return nil, nil
	}

	ledger, _, err := loadPluginLedger(e.vault.PluginsLedgerPath())
	if err != nil {
		return nil, []string{"plugin migrate: " + err.Error()}
	}

	plan, _ := e.buildFarmPlan(ledger)

	dirs, err := e.skillsDirs(ctx)
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

	key, skillName, ok := m.engine.cacheLinkKey(link, m.ledger)
	if !ok {
		return
	}

	if reason := m.engine.linkReason(m.plan, key, skillName); reason != linkMigratable {
		m.noteUnmigratable(reason, key, skillName, name)

		return
	}

	pivot, _ := m.engine.pivotSkillPath(key, skillName)

	if m.dryRun {
		m.moved[key]++

		return
	}

	if err := fsutil.ReplaceSymlink(path, pivot); err != nil {
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

	lot, ok := m.engine.pivotSkillPath(key, name)
	if !ok {
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

func (m *migrateState) note(key, format string, args ...any) {
	m.notes[key] = append(m.notes[key], fmt.Sprintf(format, args...))
}

func (m *migrateState) results() []MigrateResult {
	var results []MigrateResult

	for _, key := range slices.Sorted(maps.Keys(m.moved)) {
		notes := slices.Compact(slices.Sorted(slices.Values(m.notes[key])))

		results = append(results, MigrateResult{
			Key:      key,
			Migrated: m.moved[key],
			Note:     strings.Join(notes, "; "),
		})
	}

	return results
}

// linkReason classifies why a cache link target can or cannot be repointed to
// the vault pivot.
func (e *Engine) linkReason(plan farmPlan, key, skillName string) linkReason {
	if _, parked := plan.Parked[key]; !parked {
		return linkPluginNotParked
	}

	if owner, taken := plan.Owner[skillName]; taken && owner != key {
		return linkOwnerTaken
	}

	pivot, ok := e.pivotSkillPath(key, skillName)
	if !ok || !isDir(pivot) {
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

func (e *Engine) pivotSkillPath(key, skillName string) (string, bool) {
	marketplace, name, ok := strings.Cut(key, "/")
	if !ok || !validPluginKey(marketplace, name) || !filepath.IsLocal(skillName) {
		return "", false
	}

	return filepath.Join(e.vault.PluginsDir(), marketplace, name, farmPivotName, farmSkillsDir, skillName), true
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

func (e *Engine) pluginMigrationIssues(ctx context.Context, ledger pluginLedger) []Issue {
	if e.home == "" {
		return nil
	}

	plan, _ := e.buildFarmPlan(ledger)

	dirs, err := e.skillsDirs(ctx)
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Message: "plugin migration: " + err.Error()}}
	}

	var issues []Issue

	for _, dir := range dirs {
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
				issues = append(issues, e.migrationLinkIssues(plan, ledger, path, name)...)
			case entry.IsDir():
				issues = append(issues, e.migrationForkIssues(plan, path, name)...)
			}
		}
	}

	return issues
}

func (e *Engine) migrationLinkIssues(plan farmPlan, ledger pluginLedger, path, name string) []Issue {
	link, err := os.Readlink(path)
	if err != nil {
		return nil
	}

	key, skillName, ok := e.cacheLinkKey(link, ledger)
	if !ok {
		return nil
	}

	reason := e.linkReason(plan, key, skillName)
	if reason == linkMigratable {
		return []Issue{{Severity: SeverityWarn, Message: fmt.Sprintf("skill %s points into the plugin cache (%s); run beadle heal", name, link)}}
	}

	return []Issue{{Severity: SeverityWarn, Message: fmt.Sprintf("skill %s points into the plugin cache; %s (run beadle sync or remove the stale link)", name, e.linkReasonText(plan, reason, key, skillName))}}
}

func (e *Engine) migrationForkIssues(plan farmPlan, path, name string) []Issue {
	if _, stub := skill.IsStubDir(path); stub {
		return nil
	}

	key, ok := plan.Owner[name]
	if !ok {
		return nil
	}

	lot, ok := e.pivotSkillPath(key, name)
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
