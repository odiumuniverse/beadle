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

	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
	"github.com/odiumuniverse/agents-sync/pkg/skill"
)

type HealResult struct {
	Key      string `json:"key"`
	Stubs    int    `json:"stubs,omitempty"`
	Migrated int    `json:"migrated,omitempty"`
	Cleaned  int    `json:"cleaned,omitempty"`
	Retired  int    `json:"retired,omitempty"`
	Note     string `json:"note,omitempty"`
}

func (e *Engine) Heal(ctx context.Context, dryRun bool) ([]HealResult, error) {
	release, err := e.lock(ctx)
	if err != nil {
		return nil, err
	}

	defer release()

	ledger, _, err := loadPluginLedger(e.vault.PluginsLedgerPath())
	if err != nil {
		return nil, err
	}

	migrations, migrateWarns := e.migrateHosts(ctx, dryRun)
	if len(migrateWarns) > 0 {
		return mergeMigrations(nil, migrations), errors.New(strings.Join(migrateWarns, "; "))
	}

	var results []HealResult

	changed := false

	for _, key := range slices.Sorted(maps.Keys(ledger.Plugins)) {
		rec := ledger.Plugins[key]
		if rec.QuarantinedAt.IsZero() {
			continue
		}

		result, healed := e.healPlugin(ctx, key, dryRun)

		results = append(results, result)

		if !healed {
			continue
		}

		rec.QuarantinedAt = time.Time{}
		rec.Target = ""
		rec.RetiredAt = e.now()

		ledger.Plugins[key] = rec

		changed = true
	}

	results = mergeMigrations(results, migrations)

	if !changed {
		return results, nil
	}

	if err := e.vault.EnsureGitIgnore(); err != nil {
		return results, err
	}

	if err := ledger.save(e.vault.PluginsLedgerPath()); err != nil {
		return results, err
	}

	return results, nil
}

func mergeMigrations(results []HealResult, migrations []MigrateResult) []HealResult {
	merged := map[string]*HealResult{}

	for _, result := range results {
		entry := result
		merged[result.Key] = &entry
	}

	for _, migration := range migrations {
		entry, ok := merged[migration.Key]
		if !ok {
			entry = &HealResult{Key: migration.Key}
			merged[migration.Key] = entry
		}

		entry.Migrated += migration.Migrated
		entry.Note = joinNotes([]string{entry.Note, migration.Note})
	}

	var out []HealResult

	for _, key := range slices.Sorted(maps.Keys(merged)) {
		entry := merged[key]
		if entry.Stubs == 0 && entry.Migrated == 0 && entry.Cleaned == 0 && entry.Retired == 0 {
			continue
		}

		out = append(out, *entry)
	}

	return out
}

func (e *Engine) healPlugin(ctx context.Context, key string, dryRun bool) (HealResult, bool) {
	result := HealResult{Key: key}

	dirs, err := e.skillsDirs(ctx)
	if err != nil {
		result.Note = err.Error()

		return result, false
	}

	stubs, stubNotes := healStubs(dirs, key, dryRun)
	result.Stubs = stubs

	cleaned, artifactNote := e.dropQuarantine(key, dryRun)
	if cleaned {
		result.Cleaned = 1
	}

	result.Retired = 1
	result.Note = joinNotes(append(stubNotes, artifactNote))

	return result, !dryRun
}

func (e *Engine) skillsDirs(ctx context.Context) ([]string, error) {
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
		if surface == nil || e.config.ModeFor(a.ID, kind.Skills, surface.Traits().DefaultMode) == config.ModeOff {
			continue
		}

		dirs = append(dirs, surface.Path())
	}

	return dirs, nil
}

func healStubs(dirs []string, key string, dryRun bool) (int, []string) {
	count := 0

	var notes []string

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}

			notes = append(notes, fmt.Sprintf("cannot read %s: %v", dir, err))

			continue
		}

		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())

			stubKey, ok := skill.IsStubDir(path)
			if !ok || stubKey != key {
				continue
			}

			count++

			if dryRun {
				continue
			}

			if err := os.RemoveAll(path); err != nil {
				notes = append(notes, fmt.Sprintf("cannot remove %s: %v", path, err))
			}
		}
	}

	return count, notes
}

func (e *Engine) dropQuarantine(key string, dryRun bool) (bool, string) {
	marketplace, name, ok := strings.Cut(key, "/")
	if !ok || !validPluginKey(marketplace, name) {
		return false, "cannot clean the quarantine artifact: invalid plugin key " + key
	}

	current := filepath.Join(e.vault.PluginsDir(), quarantineDirName, marketplace, name, farmPivotName)

	info, err := os.Lstat(current)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, ""
	case err != nil:
		return false, fmt.Sprintf("cannot clean the quarantine artifact: %v", err)
	case info.Mode()&fs.ModeSymlink == 0:
		return false, fmt.Sprintf("quarantine artifact %s is not a symlink; left in place", current)
	}

	if dryRun {
		return true, ""
	}

	if err := os.Remove(current); err != nil {
		return false, fmt.Sprintf("cannot clean the quarantine artifact: %v", err)
	}

	return true, dropQuarantineDirs(filepath.Dir(current), filepath.Join(e.vault.PluginsDir(), quarantineDirName))
}

func dropQuarantineDirs(dir, stop string) string {
	for {
		if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Sprintf("quarantine directory %s is not empty; left in place", dir)
		}

		if dir == stop {
			return ""
		}

		dir = filepath.Dir(dir)
	}
}

func joinNotes(notes []string) string {
	return strings.Join(slices.DeleteFunc(notes, func(note string) bool { return note == "" }), "; ")
}
