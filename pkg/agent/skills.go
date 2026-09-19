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

	"github.com/odiumuniverse/agents-sync/pkg/kind"
	"github.com/odiumuniverse/agents-sync/pkg/skill"
)

type skillsSurface struct {
	dir         string
	ignoreUnder []string
	alsoReads   []string
	traits      Traits
}

// SkillRef points to one readable copy of a skill.
type SkillRef struct {
	Dir  string
	Name string
	Root string
}

// SkillReader lists the skill copies an agent can read.
type SkillReader interface {
	ReadableSkills() ([]SkillRef, error)
}

func (s *skillsSurface) ReadableSkills() ([]SkillRef, error) {
	var refs []SkillRef

	for _, dir := range slices.Concat([]string{s.dir}, s.alsoReads) {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			return nil, fmt.Errorf("read skills directory %s: %w", dir, err)
		}

		for _, entry := range entries {
			if !skill.ValidName(entry.Name()) {
				continue
			}

			root, _, ok := s.skillRootAt(dir, entry)
			if !ok {
				continue
			}

			refs = append(refs, SkillRef{Dir: dir, Name: entry.Name(), Root: root})
		}
	}

	return refs, nil
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

	snap := Snapshot{Items: kind.Items{}, Present: true, ReadOnly: map[string]string{}}

	for _, entry := range entries {
		name := entry.Name()
		if !skill.ValidName(name) {
			continue
		}

		root, reason, ok := s.skillRoot(entry)
		if !ok {
			continue
		}

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

	return snap, nil
}

func (s *skillsSurface) skillRoot(entry fs.DirEntry) (string, string, bool) {
	return s.skillRootAt(s.dir, entry)
}

func (s *skillsSurface) skillRootAt(dir string, entry fs.DirEntry) (string, string, bool) {
	path := filepath.Join(dir, entry.Name())

	if entry.Type()&fs.ModeSymlink == 0 {
		if !entry.IsDir() || isStubDir(path) {
			return "", "", false
		}

		return path, "", true
	}

	target, err := filepath.EvalSymlinks(path)
	if err != nil || s.ignored(target) || isStubDir(target) {
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

func (s *skillsSurface) Write(ctx context.Context, desired kind.Items) error {
	current, err := s.Read(ctx)
	if err != nil {
		return err
	}

	want := skill.Group(desired)
	have := skill.Group(current.Items)

	for _, name := range slices.Sorted(maps.Keys(have)) {
		if _, keep := want[name]; keep || current.ReadOnly[name] != "" {
			continue
		}

		if err := os.RemoveAll(filepath.Join(s.dir, name)); err != nil {
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
