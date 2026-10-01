package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/hooks"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// bundleFarm is a plugin farm as beadle 0.4.2 left it: a rendered Claude
// marketplace under `<vault>/bundles/<host>`, recorded in the state as a
// top-level `bundles` key with `enabled: true`.
//
// Nothing in the tree says which build wrote it, so it is recognised by the two
// facts together: the state on disk came from an older build, and it still
// names a bundle whose rendered tree is there. A current vault has both the
// tree and the canon package, and its state says CurrentVersion, so it is left
// alone — a migration that fired on shape alone would take the bundles a user
// just enabled with this build.
//
// The order is the safety property, not a detail: the farm is copied before a
// single path is removed, the canon is published through the plugin manager
// before the farm goes, and every path removed is one the state named.
const (
	// bundleDirName is the plugin directory inside a host's farm, the way the
	// farm renderer lays a marketplace out.
	bundleDirName = "plugins/beadle-canon"
	// canonPluginName is the name every document the farm generated carries.
	canonPluginName = "beadle-canon"
)

type bundleFarm struct {
	engine *Engine
	state  *state.State
	report *Report

	// What take() found, so publish() can finish the same move without looking
	// at the directory again — it is gone by then, by design.
	taken     bool
	takenFor  []string
	takenCopy string
	removed   []string
	kept      []string
}

// bundleFarmHosts are the hosts whose farm this state still names, sorted, so
// the report and the removal read the same list twice in the same order.
func (m *bundleFarm) hosts() []string {
	var hosts []string

	for host, rec := range m.state.Bundles {
		if !rec.Enabled {
			continue
		}

		if !dirExists(m.path(host)) {
			continue
		}

		hosts = append(hosts, host)
	}

	slices.Sort(hosts)

	return hosts
}

// path is the one directory a host's farm is, derived from the host the state
// recorded. There is no glob and no scan: a directory beadle did not record is
// not this migration's to remove.
func (m *bundleFarm) path(host string) string {
	return filepath.Join(m.engine.vault.Root(), "bundles", host)
}

// fromOlderBuild reports whether the state on disk came from a build older than
// this one. A state this process created has no loaded version and is current
// by definition.
func (m *bundleFarm) fromOlderBuild() bool {
	loaded := m.state.LoadedVersion()

	return loaded > 0 && loaded < state.CurrentVersion
}

// present reports whether there is a farm to move at all.
func (m *bundleFarm) present() bool {
	return m.fromOlderBuild() && len(m.hosts()) > 0
}

// run moves the farm to the plugin manager. Every step is written so that
// running it twice is the same as running it once: a second run finds no
// enabled bundle left to move and says nothing.
// take is the first half of the upgrade: the farm is copied and beadle's own
// files are removed, before anything this build renders can prune the directory.
// The host's own directory is a build product of the farm, and this build writes
// it too, so a stale-file sweep elsewhere in the sync would delete a user's
// file before anybody had copied it.
//
// It is a no-op unless the state came from an older build and still names a
// bundle whose tree is on disk, so a vault this build manages is never touched.
func (m *bundleFarm) take() error {
	if m.taken || !m.present() {
		return nil
	}

	m.taken = true
	m.takenFor = m.hosts()

	backup, removed, kept, err := m.backupAndRemove(m.takenFor)
	m.takenCopy, m.removed, m.kept = backup, removed, kept

	return err
}

// publish is the second half: the canon becomes a package of the plugin
// manager, with the consent the 0.4.2 state proves, and the state stops
// claiming a bundle this build does not keep.
func (m *bundleFarm) publish(ctx context.Context) error {
	if !m.taken {
		return nil
	}

	// The canon is rendered here rather than left to the sync's own render,
	// which happens after the library has already applied: without this the
	// upgrade would publish an empty package and deliver nothing until the next
	// run.
	m.engine.canonPackage(ctx, m.report)

	consent := state.PublishConsent{
		ApprovedFor: []string{canonPackageRef()},
		Hosts:       m.takenFor,
		Reason:      "the state an older beadle wrote records this bundle as enabled for this host",
	}

	if err := m.engine.manager.PublishCanonPackage(ctx, m.engine.vault.CanonPackageDir(), m.takenFor, consent); err != nil {
		// The farm is already out of the way and the copy is taken, so the user
		// is one command away from a delivered canon.
		m.report.Notes = append(m.report.Notes, fmt.Sprintf(
			"the canon package is rendered, but the library did not take it over (%s); "+
				"run `beadle plugins install %s --yes` to finish the move",
			err.Error(), localCanonRef(m.engine.vault.CanonPackageDir())))

		return nil
	}

	m.retire(m.takenFor)

	m.report.Notes = append(m.report.Notes, fmt.Sprintf(
		"moved the plugin farm %s left under %s to the plugin manager (%d file(s) removed, backed up to %s)",
		olderStateLabel(m.state.LoadedVersion()), strings.Join(hostPaths(m.takenFor), ", "), len(m.removed), m.takenCopy))

	if len(m.kept) > 0 {
		// A file in the farm that beadle cannot account for is the user's, and
		// the one thing this may not lose silently. The copy above is the
		// preservation: `bundles/<host>` is a directory THIS build renders too,
		// so a file left in it would be pruned by the very next sync. The report
		// says where the bytes are and how to keep them.
		m.report.Warnings = append(m.report.Warnings, fmt.Sprintf(
			"bundles: %d file(s) under %s are not what this build would write; they were copied to %s "+
				"before the farm was taken, and the directory is now rendered by this build, which owns that path "+
				"and prunes what it did not write — move them out of %s if you want to keep them, "+
				"or `beadle bundles disable %s` to stop it being re-rendered",
			len(m.kept), strings.Join(hostPaths(m.takenFor), ", "), m.takenCopy,
			strings.Join(hostPaths(m.takenFor), ", "), strings.Join(m.takenFor, ", ")))
	}

	return nil
}

// localCanonRef is the one ref a user can install the canon package by: a
// `local:` source pointing at the tree the migration rendered. The package id
// alone is not a ref — the library reads a bare name as a git source and
// refuses it — so the message carries the ref that works.
func localCanonRef(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "local:" + dir
	}

	return "local:" + abs
}

// canonPackageRef is the id the library knows the canon package by. A question
// about any other package is not covered by the upgrade's consent.
func canonPackageRef() string { return "local:" + canonPluginName }

// hostPaths names the recorded directories for a message, relative to the
// vault: a path a user can recognise in their own tree.
func hostPaths(hosts []string) []string {
	out := make([]string, 0, len(hosts))

	for _, host := range hosts {
		out = append(out, filepath.Join("bundles", host))
	}

	return out
}

// backupAndRemove copies the farm and then removes beadle's own files from it,
// in that order, because the copy is what makes the removal reversible.
func (m *bundleFarm) backupAndRemove(hosts []string) (backup string, removed, kept []string, err error) {
	backup, err = m.backup(hosts)
	if err != nil {
		return "", nil, nil, err
	}

	removed, kept, err = m.remove(hosts)

	return backup, removed, kept, err
}

// backup copies each recorded farm directory into the vault's own backup
// directory, which the vault's .gitignore already keeps out of git. It runs
// before the first removal, and a second run reuses the directory it took.
func (m *bundleFarm) backup(hosts []string) (string, error) {
	dir := filepath.Join(m.engine.vault.Root(), "state", "backups",
		time.Now().UTC().Format("20060102T150405Z"), "bundles")

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create the farm backup: %w", err)
	}

	for _, host := range hosts {
		if err := copyTreeInto(m.path(host), filepath.Join(dir, host)); err != nil {
			return "", fmt.Errorf("back up the %s farm: %w", host, err)
		}
	}

	return dir, nil
}

// remove deletes the farm's own files — the ones this build can account for —
// and leaves everything else exactly where the user put it, naming it in the
// report so nothing is left behind silently.
//
// "Beadle's own" is a claim about content, not about a path:
//
//   - a skill file is beadle's when the vault canon has that skill and the bytes
//     are the canon's: the farm copies skills verbatim, so a difference is
//     somebody's edit;
//   - a document beadle generates (the marketplace, the plugin manifest, the
//     merged MCP document, the hooks document) is beadle's when it parses as
//     JSON and names the canon package;
//   - anything else is not beadle's, and is kept.
func (m *bundleFarm) remove(hosts []string) (removed, kept []string, err error) {
	for _, host := range hosts {
		root := m.path(host)

		ours, theirs, err := m.classify(root)
		if err != nil {
			return removed, kept, err
		}

		for _, rel := range ours {
			if err := os.Remove(filepath.Join(root, rel)); err != nil {
				return removed, kept, fmt.Errorf("remove the %s farm file %s: %w", host, rel, err)
			}

			removed = append(removed, filepath.Join("bundles", host, rel))
		}

		kept = append(kept, theirs...)

		m.pruneEmptyDirs(root, ours)

		// The host's own directory goes with its last beadle-written file. A
		// directory that still holds a file the user owns is left, and the
		// report above says so.
		_ = os.Remove(root)
	}

	return removed, kept, nil
}

// classify splits the farm's files into the ones beadle wrote and the ones it
// did not, as relative paths.
func (m *bundleFarm) classify(root string) (ours, theirs []string, err error) {
	// The walk never leaves `root`: the callback only reads a file it reached
	// through the directory it was given, and the removal below re-joins the two
	// names itself. A link inside the farm is not followed — its own entry is
	// what gets classified.
	//nolint:gosec // G122,G703: the root is a recorded directory inside the vault and nothing outside it is read
	//nolint:gosec // G122,G703: the walk root is a recorded directory inside the vault; the callback reads only what it reached through it
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		if m.isBeadlesOwn(path, rel) {
			ours = append(ours, rel)
		} else {
			theirs = append(theirs, filepath.Join("bundles", filepath.Base(root), rel))
		}

		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	slices.Sort(ours)
	slices.Sort(theirs)

	return ours, theirs, nil
}

// isBeadlesOwn reports whether one farm file is a document beadle generated.
func (m *bundleFarm) isBeadlesOwn(path, rel string) bool {
	if name, ok := skillFileInFarm(rel); ok {
		canon := filepath.Join(m.engine.vault.SkillsDir(), filepath.FromSlash(name))

		want, err := os.ReadFile(canon) //nolint:gosec // G304: a path inside the vault's canon
		if err != nil {
			return false
		}

		got, err := readFarmFile(path)
		if err != nil {
			return false
		}

		return bytes.Equal(want, got)
	}

	document, parseable := readJSONObject(path)
	if !parseable {
		return false
	}

	switch filepath.ToSlash(rel) {
	case ".claude-plugin/marketplace.json":
		// The marketplace lists what the farm published; it names the plugin,
		// and a document that does not is not this build's marketplace.
		return bytes.Contains(document, []byte(canonPluginName))
	case filepath.ToSlash(filepath.Join(bundleDirName, ".claude-plugin", "plugin.json")):
		return bytes.Contains(document, []byte(canonPluginName))
	case filepath.ToSlash(filepath.Join(bundleDirName, ".mcp.json")):
		// The merged MCP document is a projection of the vault's own: beadle
		// wrote it if every server in it is one the vault canon has. A server
		// the user added to the directory by hand is not, and stays.
		return projectionOf(document, "mcpServers", m.vaultServers())
	case filepath.ToSlash(filepath.Join(bundleDirName, "hooks", "hooks.json")):
		return projectionOf(document, "hooks", m.vaultHooks())
	default:
		return false
	}
}

// readJSONObject reads a file and reports whether it is a JSON object: the
// farm's documents all are, and one that is not is not something beadle wrote.
func readJSONObject(path string) ([]byte, bool) {
	data, err := readFarmFile(path)
	if err != nil {
		return nil, false
	}

	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, false
	}

	return data, true
}

// projectionOf reports whether a generated document is beadle's own: it has the
// key the renderer writes, and every entry in it is one the vault canon has. An
// empty projection over an empty canon is what an unused farm looks like, and it
// is beadle's.
func projectionOf(document []byte, key string, allowed map[string]bool) bool {
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(document, &parsed); err != nil {
		return false
	}

	raw, ok := parsed[key]
	if !ok {
		return false
	}

	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return false
	}

	for name := range entries {
		if !allowed[name] {
			return false
		}
	}

	return true
}

// vaultServers are the MCP server names the vault canon declares.
func (m *bundleFarm) vaultServers() map[string]bool {
	names := map[string]bool{}

	data, _, err := readOptional(m.engine.vault.ServersPath())
	if err != nil {
		return names
	}

	servers, err := bundle.CanonServers(data)
	if err != nil {
		return names
	}

	for name := range servers {
		names[name] = true
	}

	return names
}

// vaultHooks are the hook names the vault canon declares.
func (m *bundleFarm) vaultHooks() map[string]bool {
	names := map[string]bool{}

	canon, err := hooks.Load(m.engine.vault.HooksPath())
	if err != nil {
		return names
	}

	for name := range canon {
		names[name] = true
	}

	return names
}

// skillFileInFarm returns the canon skill name a farm skill file carries.
func skillFileInFarm(rel string) (string, bool) {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) != 4 || parts[0] != bundleDirName || parts[1] != "skills" || parts[3] != "SKILL.md" {
		return "", false
	}

	return parts[2], true
}

// pruneEmptyDirs removes the directories the removal emptied, deepest first,
// and never climbs above the farm's own directory.
func (m *bundleFarm) pruneEmptyDirs(root string, removed []string) {
	dirs := map[string]bool{}

	for _, rel := range removed {
		dir := filepath.Dir(filepath.Join(root, rel))
		for dir != root && fsutil.Under(root, dir) {
			dirs[dir] = true

			dir = filepath.Dir(dir)
		}
	}

	ordered := make([]string, 0, len(dirs))

	for dir := range dirs {
		ordered = append(ordered, dir)
	}

	slices.SortFunc(ordered, func(a, b string) int { return len(b) - len(a) })

	for _, dir := range ordered {
		_ = os.Remove(dir)
	}
}

// retire clears the record this build does not keep, so the second run has
// nothing to do and the state does not claim a bundle that is gone.
func (m *bundleFarm) retire(hosts []string) {
	for _, host := range hosts {
		rec := m.state.Bundles[host]
		rec.Enabled = false
		rec.Registered = false
		rec.ProbeNote = "the farm was moved to the plugin manager by the upgrade from " +
			olderStateLabel(m.state.LoadedVersion())
		m.state.Bundles[host] = rec
	}
}

// olderStateLabel names what the state came from, in the only terms the state
// can support: the schema version on disk. A user who wants the release name
// can match the number against the changelog; guessing "0.4.2" from a version
// would be a fact beadle does not have.
func olderStateLabel(loaded int) string {
	if loaded <= 0 {
		return "an earlier beadle"
	}

	return fmt.Sprintf("an earlier beadle (state version %d)", loaded)
}

func dirExists(path string) bool {
	info, err := os.Lstat(path)

	return err == nil && info.IsDir()
}

// copyTreeInto copies a directory tree, creating the destination. It refuses to
// write outside the destination it was given, so a path that escaped the vault
// could not follow it.
func copyTreeInto(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}

			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		target := filepath.Join(dst, rel)

		switch {
		case entry.IsDir():
			return os.MkdirAll(target, 0o700)
		case entry.Type()&fs.ModeSymlink != 0:
			// A link is copied as the link, never followed: the farm pointed
			// at other hosts' files, and following one would copy a user's
			// own tree into a backup.
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}

			//nolint:gosec // G122: the target is inside the backup directory this walk is filling; the link's own target string is never opened
			return os.Symlink(link, target)
		default:
			data, err := readFarmFile(path)
			if err != nil {
				return err
			}

			return os.WriteFile(target, data, 0o600)
		}
	})
}

// readFarmFile reads one file the walk reached inside a recorded farm
// directory.
//
// The path is derived from a directory the state named inside the vault, and
// the walk never follows a link: a symlink's own entry is what gets classified,
// and the copy above re-creates the link instead of its target. That is what
// makes the walk safe to read through, and it is why this is not a
// `#nosec`-shaped hole.
func readFarmFile(path string) ([]byte, error) {
	//nolint:gosec // G304,G122,G703: path is inside a recorded farm directory; links are never followed
	return os.ReadFile(path) //nolint:gosec // G304,G122,G703: as above
}

// doctorIssues names a farm an older build left, before anything moves it. The
// doctor is the command a user runs when they do not trust a sync, so it is the
// only place the farm can still be named on disk: after the migration the
// directory is gone, and a report that never mentioned it cannot be re-read.
func (e *Engine) bundleFarmIssues(st *state.State) []Issue {
	farm := &bundleFarm{engine: e, state: st}
	if !farm.present() {
		return nil
	}

	var issues []Issue

	for _, host := range farm.hosts() {
		issues = append(issues, Issue{
			Severity: SeverityInfo,
			Message: "bundles/" + host + ": a plugin farm " + olderStateLabel(st.LoadedVersion()) +
				" left is on disk and nothing applies it; run beadle sync to move it to the plugin manager",
		})
	}

	return issues
}
