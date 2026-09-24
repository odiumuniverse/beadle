package agent

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

	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/skill"
)

type skillsSurface struct {
	dir         string
	ignoreUnder []string
	alsoReads   []string // shadowing tells whether the host collapses same-name copies of its
	// read directories into one visible skill. Undeclared hosts read every
	// copy (conservative: duplicates stay visible and are never hidden).
	shadowing bool
	// readOrder lists the read directories from the highest precedence to
	// the lowest; it is only consulted when shadowing is set. Empty means
	// the scan order (the own directory first).
	readOrder []string
	// namespacedBundle tells whether the host shows bundle skills under a
	// separate namespace (Claude: beadle-canon:<name>), so a bundle copy
	// never collapses with and never shadows a file copy.
	namespacedBundle bool
	// flatSkills tells whether the host reads flat `<name>.md` copies from
	// its own directory as skills (OpenCode, Pi, DSH): the name is the
	// basename.
	flatSkills bool
	// codec checks and maps the canon trees into the host's frontmatter
	// dialect on write. Nil stores the trees verbatim.
	codec  skillTreeCodec
	traits Traits
}

// skillTreeCodec checks and maps one host's skill trees. A host whose skill
// dialect is the canon dialect needs validation only, so toHost returns the
// tree unchanged; a host with its own dialect renders it.
type skillTreeCodec interface {
	// toHost renders one canon skill tree in the host dialect, refusing a
	// name or tree the host cannot read.
	toHost(name string, tree skill.Tree) (skill.Tree, error)
}

// SkillCaps describes how an agent composes same-name skill copies from its
// read area. It is declared next to alsoReads: hosts without documented
// precedence leave everything visible, so beadle never hides a copy it
// cannot prove shadowed.
type SkillCaps struct {
	// Shadowing reports that the host collapses same-name copies between
	// the read directories.
	Shadowing bool
	// ReadOrder lists the read directories from the highest precedence to
	// the lowest; consulted only when Shadowing is set.
	ReadOrder []string
	// NamespacedBundle reports that bundle skills are visible in addition
	// to the file copies instead of replacing them (Claude).
	NamespacedBundle bool
}

// SkillCapsSurface declares the skill visibility caps of a surface.
type SkillCapsSurface interface {
	SkillCaps() SkillCaps
}

func (s *skillsSurface) SkillCaps() SkillCaps {
	return SkillCaps{Shadowing: s.shadowing, ReadOrder: s.readOrder, NamespacedBundle: s.namespacedBundle}
}

// ReadDirs lists the directories the surface reads skills from, in scan
// order (the own directory first).
func (s *skillsSurface) ReadDirs() []string {
	return slices.Concat([]string{s.dir}, s.alsoReads)
}

// SkillRef points to one readable copy of a skill.
type SkillRef struct {
	Dir  string
	Name string
	Root string
	// File is the flat `<name>.md` path when the copy is a flat file; empty
	// for a directory copy. Root points at the file the host reads (the
	// resolved target for a symlink).
	File string
}

// SkillReader lists the skill copies an agent can read.
type SkillReader interface {
	ReadableSkills() ([]SkillRef, error)
}

// SkillReadArea lists the directories a skills surface reads from, in scan
// order (the own directory first).
type SkillReadArea interface {
	ReadDirs() []string
}

// SkillFlatReader lists the flat `<name>.md` copies a skills surface reads
// from its own directory: readable names → file path, plus the names a
// same-name skill directory shadows. Every skills surface implements it; a
// host that does not read flat skills reports empty maps.
type SkillFlatReader interface {
	FlatSkillRefs() (readable, shadowed map[string]string, err error)
}

func (s *skillsSurface) ReadableSkills() ([]SkillRef, error) {
	var refs []SkillRef

	for _, dir := range s.ReadDirs() {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			return nil, fmt.Errorf("read skills directory %s: %w", dir, err)
		}

		claimed := map[string]struct{}{}

		for _, entry := range entries {
			if !skill.ValidName(entry.Name()) {
				continue
			}

			root, _, ok := s.skillRootAt(dir, entry)
			if !ok {
				continue
			}

			claimed[entry.Name()] = struct{}{}

			refs = append(refs, SkillRef{Dir: dir, Name: entry.Name(), Root: root})
		}

		// Flat copies are read from the host's own directory only: Pi
		// ignores root .md files in ~/.agents/skills, and the other read
		// areas have no verified flat semantics.
		if !s.flatSkills || dir != s.dir {
			continue
		}

		for _, entry := range entries {
			name, ok := skill.FlatName(entry.Name())
			if !ok {
				continue
			}

			if _, taken := claimed[name]; taken {
				continue
			}

			file := filepath.Join(dir, entry.Name())

			root, ok := s.flatRoot(file)
			if !ok {
				continue
			}

			refs = append(refs, SkillRef{Dir: dir, Name: name, Root: root, File: file})
		}
	}

	return refs, nil
}

// flatRoot resolves a flat copy to the regular file the host reads: the file
// itself, or a symlink target. Copies pointing into an ignored area (plugin
// caches) are not readable, mirroring the symlinked directory rule.
func (s *skillsSurface) flatRoot(file string) (string, bool) {
	info, err := os.Lstat(file)
	if err != nil {
		return "", false
	}

	if info.Mode()&fs.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(file)
		if err != nil || s.ignored(target) {
			return "", false
		}

		info, err = os.Stat(target)
		if err != nil || !info.Mode().IsRegular() {
			return "", false
		}

		return target, true
	}

	if !info.Mode().IsRegular() {
		return "", false
	}

	return file, true
}

func (s *skillsSurface) FlatSkillRefs() (map[string]string, map[string]string, error) {
	readable := map[string]string{}
	shadowed := map[string]string{}

	if !s.flatSkills {
		return readable, shadowed, nil
	}

	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return readable, shadowed, nil
	}

	if err != nil {
		return nil, nil, fmt.Errorf("read skills directory %s: %w", s.dir, err)
	}

	claimed := map[string]struct{}{}

	for _, entry := range entries {
		if !skill.ValidName(entry.Name()) {
			continue
		}

		if _, _, ok := s.skillRoot(entry); ok {
			claimed[entry.Name()] = struct{}{}
		}
	}

	for _, entry := range entries {
		name, ok := skill.FlatName(entry.Name())
		if !ok {
			continue
		}

		file := filepath.Join(s.dir, entry.Name())

		if _, taken := claimed[name]; taken {
			if _, ok := s.flatRoot(file); !ok {
				continue
			}

			shadowed[name] = file

			continue
		}

		if _, ok := s.flatRoot(file); !ok {
			continue
		}

		readable[name] = file
	}

	return readable, shadowed, nil
}

func (s *skillsSurface) Kind() kind.ID { return kind.Skills }

func (s *skillsSurface) Path() string { return s.dir }

func (s *skillsSurface) WatchPaths() []string { return []string{s.dir} }

func (s *skillsSurface) Traits() Traits { return s.traits }

func (s *skillsSurface) Read(context.Context) (Snapshot, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return Snapshot{Items: kind.Items{}}, nil
	}

	if err != nil {
		return Snapshot{}, fmt.Errorf("read skills directory %s: %w", s.dir, err)
	}

	snap := Snapshot{Items: kind.Items{}, Present: true, ReadOnly: map[string]string{}, Unreadable: map[string]string{}}

	claimed := map[string]struct{}{}

	for _, entry := range entries {
		name := entry.Name()
		if !skill.ValidName(name) {
			continue
		}

		if entry.Type()&fs.ModeSymlink != 0 {
			path := filepath.Join(s.dir, name)

			if _, err := filepath.EvalSymlinks(path); err != nil {
				snap.ReadOnly[name] = "broken symlink: " + path
				snap.Unreadable[name] = "broken symlink " + path

				continue
			}
		}

		root, reason, ok := s.skillRoot(entry)
		if !ok {
			continue
		}

		claimed[name] = struct{}{}

		tree, err := skill.ReadTree(root)
		if err != nil {
			return Snapshot{}, err
		}

		if reason != "" {
			snap.ReadOnly[name] = reason
		}

		for rel, data := range tree {
			snap.Items[name+"/"+rel] = data
		}
	}

	if err := s.readFlat(entries, claimed, &snap); err != nil {
		return Snapshot{}, err
	}

	return snap, nil
}

// readFlat adds the flat `<name>.md` copies of the own directory. A copy
// shadowed by a same-name skill directory stays on disk untouched: the host
// keeps one copy per name, and the directory wins.
func (s *skillsSurface) readFlat(entries []fs.DirEntry, claimed map[string]struct{}, snap *Snapshot) error {
	if !s.flatSkills {
		return nil
	}

	for _, entry := range entries {
		name, ok := skill.FlatName(entry.Name())
		if !ok {
			continue
		}

		file := filepath.Join(s.dir, entry.Name())

		if _, taken := claimed[name]; taken {
			// Only a real flat copy is shadowed: a directory or a symlink to
			// a directory named `<name>.md` is not a skill at all.
			if _, ok := s.flatRoot(file); !ok {
				continue
			}

			snap.Warnings = append(snap.Warnings, fmt.Sprintf(
				"flat skill %s is shadowed by %s/%s; the file is left untouched", entry.Name(), name, skill.FileName))

			continue
		}

		if entry.Type()&fs.ModeSymlink == 0 && !entry.Type().IsRegular() {
			// A directory or a device named `<name>.md` is not a flat copy.
			continue
		}

		root, reason, ok := s.flatCopy(entry, name, snap)
		if !ok {
			continue
		}

		data, err := os.ReadFile(root) //nolint:gosec // G304: skill paths are resolved by the tool
		if err != nil {
			return fmt.Errorf("read flat skill %s: %w", root, err)
		}

		if reason != "" {
			snap.ReadOnly[name] = reason
		}

		snap.Items[name+"/"+skill.FileName] = data
	}

	return nil
}

// flatCopy resolves one flat entry to the file the host reads: the file
// itself, or a symlink target. A broken symlink is recorded as unreadable; a
// symlink into an ignored area (plugin caches) is skipped, mirroring the
// symlinked directory rule.
func (s *skillsSurface) flatCopy(entry fs.DirEntry, name string, snap *Snapshot) (string, string, bool) {
	path := filepath.Join(s.dir, entry.Name())

	if entry.Type()&fs.ModeSymlink == 0 {
		return path, "", true
	}

	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		snap.ReadOnly[name] = "broken symlink: " + path
		snap.Unreadable[name] = "broken symlink " + path

		return "", "", false
	}

	if s.ignored(target) {
		return "", "", false
	}

	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", false
	}

	return target, "symlink to " + target, true
}

func (s *skillsSurface) skillRoot(entry fs.DirEntry) (string, string, bool) {
	return s.skillRootAt(s.dir, entry)
}

func (s *skillsSurface) skillRootAt(dir string, entry fs.DirEntry) (string, string, bool) {
	path := filepath.Join(dir, entry.Name())

	if entry.Type()&fs.ModeSymlink == 0 {
		if !entry.IsDir() || isStubDir(path) || !skill.HasRoot(path) {
			return "", "", false
		}

		return path, "", true
	}

	target, err := filepath.EvalSymlinks(path)
	if err != nil || s.ignored(target) || isStubDir(target) || !skill.HasRoot(target) {
		return "", "", false
	}

	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		return "", "", false
	}

	return target, "symlink to " + target, true
}

func (s *skillsSurface) ignored(target string) bool {
	for _, dir := range s.ignoreUnder {
		resolved := RealPath(dir)
		if target == resolved || strings.HasPrefix(target, resolved+string(filepath.Separator)) {
			return true
		}
	}

	return false
}

// SkillIgnoreRoots lets the engine mark read-area roots whose copies never
// enter the canon: plugin install areas hold host-native plugin payloads the
// farm presents as symlinks.
type SkillIgnoreRoots interface {
	AddSkillIgnoreRoots(dirs ...string)
}

// AddSkillIgnoreRoots appends plugin install roots to the ignored list of the
// surface; an existing entry is not duplicated.
func (s *skillsSurface) AddSkillIgnoreRoots(dirs ...string) {
	for _, dir := range dirs {
		if dir == "" || slices.Contains(s.ignoreUnder, dir) {
			continue
		}

		s.ignoreUnder = append(s.ignoreUnder, dir)
	}
}

func (s *skillsSurface) Write(ctx context.Context, desired kind.Items) error {
	current, err := s.Read(ctx)
	if err != nil {
		return err
	}

	want := skill.Group(desired)

	if want, err = s.renderTrees(want); err != nil {
		return err
	}

	have := skill.Group(current.Items)

	for _, name := range slices.Sorted(maps.Keys(have)) {
		if _, keep := want[name]; keep || current.ReadOnly[name] != "" {
			continue
		}

		if err := s.removeCopy(name); err != nil {
			return fmt.Errorf("remove skill %s: %w", name, err)
		}
	}

	for _, name := range slices.Sorted(maps.Keys(want)) {
		path := filepath.Join(s.dir, name)
		if current.ReadOnly[name] != "" || isSymlink(path) || isStubDir(path) {
			continue
		}

		if maps.EqualFunc(have[name], want[name], equalBytes) {
			continue
		}

		if err := skill.SyncTree(s.dir, name, want[name]); err != nil {
			return err
		}
	}

	return nil
}

// renderTrees maps the wanted canon trees into the host dialect. Without a
// codec the trees travel verbatim. With one, the trees are rendered in name
// order through the codec; a name or tree the host dialect cannot express
// refuses the write with a diagnostic instead of being dropped silently by
// skill.Group.
func (s *skillsSurface) renderTrees(want map[string]skill.Tree) (map[string]skill.Tree, error) {
	if s.codec == nil {
		return want, nil
	}

	out := make(map[string]skill.Tree, len(want))

	for _, name := range slices.Sorted(maps.Keys(want)) {
		rendered, err := s.codec.toHost(name, want[name])
		if err != nil {
			return nil, err
		}

		out[name] = rendered
	}

	return out, nil
}

// removeCopy deletes the copies of a name that left the canon: the directory
// and the flat `<name>.md` file. A shadowed flat file would otherwise be
// re-adopted by the next sync.
func (s *skillsSurface) removeCopy(name string) error {
	if err := os.RemoveAll(filepath.Join(s.dir, name)); err != nil {
		return err
	}

	flat := skill.FlatPath(s.dir, name)
	if info, err := os.Lstat(flat); err == nil && info.Mode().IsRegular() {
		return os.Remove(flat)
	}

	return nil
}

func isSymlink(path string) bool {
	info, err := os.Lstat(path)

	return err == nil && info.Mode()&fs.ModeSymlink != 0
}

func isStubDir(path string) bool {
	_, stub := skill.IsStubDir(path)

	return stub
}

func equalBytes(a, b []byte) bool {
	return string(a) == string(b)
}
