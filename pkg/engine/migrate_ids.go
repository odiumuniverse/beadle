package engine

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/agentid"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/rulings"
)

// migrateAgentIDs moves every on-disk location in the vault that is keyed by a
// historical agent id onto the canonical id it was renamed to: the adoption
// stash directories, the plugin pivots and the farm and quarantine areas, the
// plugin ledger keys and sources, and the host scopes of stored rulings.
//
// It runs on every non-dry-run sync. Every step is a rename that is skipped
// when the canonical target already exists, so a run interrupted between the
// directory moves and the ledger writes is finished by the next one. A dry run
// writes nothing, like every other vault write.
//
// The bundle tree is deliberately untouched: ~/.beadle/bundles is keyed by the
// bundle host name (claude, gemini, antigravity, omp), not by the agent id, so
// the marketplace, extension and plugin registrations the host CLIs keep
// pointing at stay valid.
func (e *Engine) migrateAgentIDs(report *Report, opts SyncOptions) {
	if opts.DryRun {
		return
	}

	renamed := e.renameVaultDirs()
	renamed = append(renamed, e.migratePluginLedger()...)
	renamed = append(renamed, e.migrateRulings()...)

	// A vault carries one rename in several places at once (a stash, a pivot, a
	// ledger key), and the user is told about each id once.
	unique := map[string]bool{}
	for _, old := range renamed {
		unique[old] = true
	}

	for _, old := range slices.Sorted(maps.Keys(unique)) {
		report.Warnings = append(report.Warnings, fmt.Sprintf("agent %s is now named %s", old, agentid.Canonical(old)))
	}
}

// renameVaultDirs renames the vault directories whose path segment is an agent
// id, each move flushed to disk before it counts: a crash can only leave a
// rename that already happened (and the next run skips it) or one that did
// not (and the next run repeats it). A target that already exists is left
// alone: the canonical directory is the live one, and merging two trees is not
// a rename.
func (e *Engine) renameVaultDirs() []string {
	plugins := e.vault.PluginsDir()

	parents := []string{
		e.vault.AdoptionsDir(),
		plugins,
		filepath.Join(plugins, farmRenderDirName),
		filepath.Join(plugins, quarantineDirName),
	}

	var renamed []string

	for old, canonical := range agentid.Aliases() {
		for _, parent := range parents {
			from, to := filepath.Join(parent, old), filepath.Join(parent, canonical)
			if !fsutil.Exists(from) || fsutil.Exists(to) {
				continue
			}

			if err := fsutil.RenamePath(from, to); err == nil {
				renamed = append(renamed, old)
			}
		}
	}

	return renamed
}

// migratePluginLedger rekeys the plugin ledger by the canonical origin and
// rewrites the source ids it recorded. The directory moves already ran, so a
// crash in between leaves a ledger whose keys no longer resolve; the next run
// rewrites them again.
func (e *Engine) migratePluginLedger() []string {
	path := e.vault.PluginsLedgerPath()

	ledger, _, err := loadPluginLedger(path)
	if err != nil {
		return nil
	}

	migrated := pluginLedger{Version: ledger.Version, Plugins: make(map[string]pluginLedgerRec, len(ledger.Plugins))}

	var renamed []string

	for key, rec := range ledger.Plugins {
		origin, name, ok := strings.Cut(key, "/")
		if !ok {
			origin, name = key, ""
		}

		canonicalOrigin := renameAgentIDField(origin, &renamed)
		rec.Source = renameAgentIDField(rec.Source, &renamed)

		for i, source := range rec.Overridden {
			rec.Overridden[i] = renameAgentIDField(source, &renamed)
		}

		migrated.Plugins[canonicalOrigin+"/"+name] = rec
	}

	if len(renamed) == 0 || migrated.save(path) != nil {
		return nil
	}

	return renamed
}

// migrateRulings renames the host scope of every stored ruling and rekeys it
// under the hash its new scope hashes to, so a trust decision the user already
// made keeps applying to the same agent.
func (e *Engine) migrateRulings() []string {
	path := e.vault.RulingsPath()

	ledger, err := rulings.Load(path)
	if err != nil {
		return nil
	}

	migrated := rulings.New()

	var renamed []string

	for _, record := range ledger.Rulings {
		if scope, ok := strings.CutPrefix(record.Signature.Scope, "host:"); ok {
			record.Signature.Scope = "host:" + renameAgentIDField(scope, &renamed)
		}

		migrated.Put(record.Signature, record)
	}

	if len(renamed) == 0 || migrated.Save(path) != nil {
		return nil
	}

	return renamed
}

// renameAgentIDField rewrites one agent-id field and records the historical id
// it held.
func renameAgentIDField(value string, renamed *[]string) string {
	canonical := agentid.Canonical(value)
	if canonical != value {
		*renamed = append(*renamed, value)
	}

	return canonical
}
