package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/skill"
)

const (
	farmPivotName = "current"
	farmSkillsDir = "skills"
	farmSkillFile = "SKILL.md"
	farmNoteLimit = 5
)

// The farm delivered plugin skills, agents and commands into the hosts by
// symlinking them out of a pivot under <vault>/plugins. It is gone: the
// plugin library presents a package's own files, and beadle does not decide
// what a host reads. What is left here is the knowledge of where the farm
// used to put things, which the one-time migration and the id rename still
// have to resolve - nothing here writes, links or prunes anything.

type farmPlan struct {
	Parked      map[string][]string
	Quarantined map[string]pluginLedgerRec
	Owner       map[string]string
	Desired     map[string]struct{}
	Canon       map[string]struct{}
	// LoserHosts maps a plugin key to the hosts that read their own native
	// copy of it: the plugin's own source host (it loads the plugin's skills
	// natively) plus every host whose copy lost the dedup or the same-key
	// source conflict. None of them may receive a farm link.
	LoserHosts map[string]map[string]bool
	// SkillDirs maps a plugin key to the plugin-relative directory its skills
	// live in (a manifest can move them).
	SkillDirs map[string]string
}

func (e *Engine) buildFarmPlan(ledger pluginLedger) (farmPlan, []string) {
	plan := farmPlan{
		Parked:      map[string][]string{},
		Quarantined: map[string]pluginLedgerRec{},
		Owner:       map[string]string{},
		Desired:     map[string]struct{}{},
		Canon:       map[string]struct{}{},
		SkillDirs:   map[string]string{},
	}

	warns := e.scanLedgerPlan(&plan, ledger)

	for _, key := range slices.Sorted(maps.Keys(plan.Parked)) {
		for _, skillName := range plan.Parked[key] {
			if owner, taken := plan.Owner[skillName]; taken {
				warns = append(warns, fmt.Sprintf("plugin farm: skill %s of %s is already provided by %s", skillName, key, owner))

				continue
			}

			plan.Owner[skillName] = key
			plan.Desired[skillName] = struct{}{}
		}
	}

	canon, canonWarns := e.canonSkillNames()

	plan.Canon = canon

	warns = append(warns, canonWarns...)

	for _, skillName := range slices.Sorted(maps.Keys(plan.Desired)) {
		if _, shadowed := canon[normSkillName(skillName)]; !shadowed {
			continue
		}

		warns = append(warns, fmt.Sprintf("plugin farm: skill %s is shadowed by the vault canon", skillName))

		delete(plan.Desired, skillName)
	}

	return plan, warns
}

func (e *Engine) scanLedgerPlan(plan *farmPlan, ledger pluginLedger) []string {
	var warns []string

	dedup := e.pluginDedup()

	plan.LoserHosts = dedup.LoserHosts

	for _, key := range slices.Sorted(maps.Keys(ledger.Plugins)) {
		rec := ledger.Plugins[key]

		// A retired record outranks a leftover quarantine flag: the plugin is
		// gone from the registry, so nothing is presented any more.
		if !rec.RetiredAt.IsZero() {
			continue
		}

		if !rec.QuarantinedAt.IsZero() {
			marketplace, name, ok := strings.Cut(key, "/")
			if ok && validPluginKey(marketplace, name) {
				plan.Quarantined[key] = rec
			}

			continue
		}

		if _, covered := dedup.Suppressed[key]; covered {
			// The same plugin is already presented from another host.
			continue
		}

		// The host that installed this plugin reads its own cache's skills
		// natively: presenting them in that host's skills directory would
		// make every skill discoverable twice. Every other host still
		// receives the farm link. Agents and commands keep their own
		// host/source skip rules (farmFileSpec.Skip), so this native host is
		// recorded for the skills plan only.
		if plan.LoserHosts[key] == nil {
			plan.LoserHosts[key] = map[string]bool{}
		}

		plan.LoserHosts[key][recSource(rec)] = true

		names, dir, ok, pluginWarns := e.parkedPluginSkills(key, rec)

		for _, warn := range pluginWarns {
			warns = append(warns, "plugin farm: "+warn)
		}

		if !ok {
			continue
		}

		plan.Parked[key] = names
		plan.SkillDirs[key] = dir
	}

	return warns
}

func (e *Engine) parkedPluginSkills(key string, rec pluginLedgerRec) ([]string, string, bool, []string) {
	marketplace, name, ok := strings.Cut(key, "/")
	if !ok || !validPluginKey(marketplace, name) {
		return nil, "", false, nil
	}

	root, warns, ok := e.pluginTargetRoot(key, rec)
	if !ok {
		return nil, "", false, warns
	}

	dir := farmKindDir(recSource(rec), rec.Target, kind.Skills)

	names, ok, scanWarns := e.scanPluginSkills(key, rec.Target, dir, root)

	return names, dir, ok, append(warns, scanWarns...)
}

func (e *Engine) pluginTargetRoot(key string, rec pluginLedgerRec) (string, []string, bool) {
	if !isDir(rec.Target) {
		return "", nil, false
	}

	// The canon is a package that delivers itself: beadle renders it at
	// <vault>/bundle and the plugin manager installs it from there. The farm
	// must never pivot, link or adopt it — that would deliver beadle's own
	// output back into the hosts through a second path with its own rules.
	// The guard is by path, not by name: a plugin that happens to be called
	// beadle-canon and lives in a host cache is an ordinary plugin.
	if samePath(rec.Target, e.vault.CanonPackageDir()) {
		return "", []string{fmt.Sprintf("plugin %s is the beadle canon package; the farm does not manage it", key)}, false
	}

	resolved, err := filepath.EvalSymlinks(rec.Target)
	if err != nil {
		return "", []string{fmt.Sprintf("plugin %s install path resolves outside the plugin cache: %s", key, rec.Target)}, false
	}

	for _, candidate := range e.pluginSourceRoots(recSource(rec)) {
		root, err := filepath.EvalSymlinks(candidate)
		if err != nil || !underDir(resolved, root) {
			continue
		}

		return root, nil, true
	}

	return "", []string{fmt.Sprintf("plugin %s install path is outside the plugin cache: %s", key, rec.Target)}, false
}

func (e *Engine) scanPluginSkills(key, target, dir, root string) ([]string, bool, []string) {
	entries, err := os.ReadDir(filepath.Join(target, dir))
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, true, nil
	}

	if err != nil {
		return nil, false, []string{fmt.Sprintf("plugin %s skills cannot be read: %v", key, err)}
	}

	var (
		names []string
		warns []string
	)

	for _, entry := range entries {
		path := filepath.Join(target, dir, entry.Name())
		if !isDir(path) || !skill.HasRoot(path) {
			continue
		}

		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || !underDir(resolved, root) {
			warns = append(warns, fmt.Sprintf("plugin %s skill %s resolves outside the plugin cache", key, entry.Name()))

			continue
		}

		names = append(names, entry.Name())
	}

	return names, true, warns
}

func underDir(path, root string) bool {
	return fsutil.Under(root, path)
}

func (e *Engine) canonSkillNames() (map[string]struct{}, []string) {
	names := map[string]struct{}{}

	entries, err := os.ReadDir(e.vault.SkillsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return names, nil
	}

	if err != nil {
		return names, []string{"plugin farm: " + err.Error()}
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		if !skill.HasRoot(filepath.Join(e.vault.SkillsDir(), entry.Name())) {
			continue
		}

		names[normSkillName(entry.Name())] = struct{}{}
	}

	return names, nil
}

func normSkillName(name string) string {
	return norm.NFC.String(strings.ToLower(name))
}

// pluginInstallDir resolves where a plugin is installed on this machine. The
// farm used to answer with a pivot under the vault - a symlink to exactly
// this directory - and a version pin chose a different pivot. The plugin
// manager owns the install, so the registry answers it, and a pin no longer
// changes anything the host reads.
func (e *Engine) pluginInstallDir(key string) (string, bool) {
	marketplace, name, ok := strings.Cut(key, "/")
	if !ok || !validPluginKey(marketplace, name) {
		return "", false
	}

	rec, installed := e.hostPluginLedger().Plugins[pluginKey(marketplace, name)]
	if !installed || rec.Target == "" || !isDir(rec.Target) {
		return "", false
	}

	return rec.Target, true
}

func (e *Engine) agentSkillTarget(agentID, key, skillName string) (string, bool) {
	return e.agentSkillTargetIn(agentID, key, farmSkillsDir, skillName)
}

// agentSkillTargetIn resolves one skill of a plugin in the payload directory
// the plugin manifest declares (defaults to skills/).
func (e *Engine) agentSkillTargetIn(agentID, key, dir, skillName string) (string, bool) {
	if dir == "" {
		dir = farmSkillsDir
	}

	install, ok := e.pluginInstallDir(key)
	if !ok || !filepath.IsLocal(skillName) || !filepath.IsLocal(dir) {
		return "", false
	}

	return filepath.Join(install, dir, skillName), true
}

func (e *Engine) farmOwned(link string) bool {
	return strings.HasPrefix(link, filepath.Clean(e.vault.PluginsDir())+string(filepath.Separator))
}

func summarizeFarmNames(names []string) string {
	sorted := slices.Compact(slices.Sorted(slices.Values(names)))

	if len(sorted) > farmNoteLimit {
		return strings.Join(sorted[:farmNoteLimit], ", ") + fmt.Sprintf(" +%d more", len(sorted)-farmNoteLimit)
	}

	return strings.Join(sorted, ", ")
}
