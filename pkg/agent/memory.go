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

	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
	"github.com/odiumuniverse/agents-sync/pkg/memory"
)

const memoryDirName = "memory"

type memorySurface struct {
	projects string
}

func (s *memorySurface) Kind() kind.ID { return kind.Memory }

func (s *memorySurface) Path() string { return s.projects }

func (s *memorySurface) WatchPaths() []string {
	info, err := os.Lstat(s.projects)
	if err != nil || !info.IsDir() {
		return nil
	}

	entries, err := os.ReadDir(s.projects)
	if err != nil {
		return nil
	}

	var paths []string

	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 {
			continue
		}

		slug := entry.Name()
		if !memory.ValidSlug(slug) {
			continue
		}

		dir := filepath.Join(s.projects, slug, memoryDirName)
		if !realDir(dir) {
			continue
		}

		paths = append(paths, dir)
	}

	slices.Sort(paths)

	return paths
}

func (s *memorySurface) Traits() Traits {
	return Traits{
		DefaultMode: config.ModeSync,
		Note:        "Claude Code per-project memory: ~/.claude/projects/<cwd-slug>/memory/*.md",
	}
}

func (s *memorySurface) Project(key string, value []byte) (string, []byte, bool) {
	slug, _, ok := strings.Cut(key, "/")
	if !ok || !memory.ValidSlug(slug) || !realDir(filepath.Join(s.projects, slug)) {
		return "", nil, false
	}

	return key, value, true
}

func (s *memorySurface) Read(context.Context) (Snapshot, error) {
	info, err := os.Lstat(s.projects)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Snapshot{Items: kind.Items{}}, nil
	case err != nil:
		return Snapshot{}, fmt.Errorf("read projects directory %s: %w", s.projects, err)
	case info.Mode()&fs.ModeSymlink != 0:
		return Snapshot{}, fmt.Errorf("projects path %s is a symlink", s.projects)
	case !info.IsDir():
		return Snapshot{}, fmt.Errorf("projects path %s is not a directory", s.projects)
	}

	entries, err := os.ReadDir(s.projects)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read projects directory %s: %w", s.projects, err)
	}

	snap := Snapshot{Items: kind.Items{}, Present: true}

	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 {
			continue
		}

		slug := entry.Name()
		if !memory.ValidSlug(slug) {
			continue
		}

		tree, err := s.readSlug(slug)
		if err != nil {
			return Snapshot{}, err
		}

		for note, data := range tree {
			snap.Items[slug+"/"+note] = data
		}
	}

	return snap, nil
}

func (s *memorySurface) readSlug(slug string) (memory.Tree, error) {
	dir := filepath.Join(s.projects, slug, memoryDirName)

	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("read memory directory %s: %w", dir, err)
	case !info.IsDir():
		return nil, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read memory directory %s: %w", dir, err)
	}

	tree := memory.Tree{}

	for _, entry := range entries {
		if !entry.Type().IsRegular() || !memory.ValidNote(entry.Name()) {
			continue
		}

		path := filepath.Join(dir, entry.Name())

		data, err := os.ReadFile(path) //nolint:gosec // G304: memory paths are resolved by the tool
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}

		tree[entry.Name()] = data
	}

	return tree, nil
}

func (s *memorySurface) Write(ctx context.Context, desired kind.Items) error {
	current, err := s.Read(ctx)
	if err != nil {
		return err
	}

	want := memory.Group(desired)
	have := memory.Group(current.Items)

	for _, slug := range slices.Sorted(maps.Keys(have)) {
		dir := filepath.Join(s.projects, slug, memoryDirName)
		if !realDir(dir) {
			continue
		}

		for _, note := range slices.Sorted(maps.Keys(have[slug])) {
			if _, keep := want[slug][note]; keep {
				continue
			}

			path := filepath.Join(dir, note)

			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}

			if err := os.Remove(path); err != nil {
				return fmt.Errorf("remove memory note %s: %w", path, err)
			}
		}
	}

	for _, slug := range slices.Sorted(maps.Keys(want)) {
		if err := s.writeSlug(slug, want[slug], have[slug]); err != nil {
			return err
		}
	}

	return nil
}

func (s *memorySurface) writeSlug(slug string, tree, have memory.Tree) error {
	dir, err := s.slugMemoryDir(slug)
	if err != nil || dir == "" {
		return err
	}

	for _, note := range slices.Sorted(maps.Keys(tree)) {
		if err := writeMemoryNote(filepath.Join(dir, note), tree[note], have[note]); err != nil {
			return err
		}
	}

	return nil
}

func (s *memorySurface) slugMemoryDir(slug string) (string, error) {
	slugDir := filepath.Join(s.projects, slug)
	if !realDir(slugDir) {
		return "", nil
	}

	dir := filepath.Join(slugDir, memoryDirName)

	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return "", fmt.Errorf("create memory directory %s: %w", dir, err)
		}
	case err != nil:
		return "", fmt.Errorf("inspect memory directory %s: %w", dir, err)
	case !info.IsDir():
		return "", nil
	}

	return dir, nil
}

func writeMemoryNote(path string, data, have []byte) error {
	existing, err := os.Lstat(path)

	switch {
	case err == nil && !existing.Mode().IsRegular():
		return nil
	case err == nil && equalBytes(have, data):
		return nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("inspect memory note %s: %w", path, err)
	}

	if err := fsutil.WriteFileAtomic(path, data, 0o644); err != nil {
		return fmt.Errorf("write memory note %s: %w", path, err)
	}

	return nil
}

func realDir(path string) bool {
	info, err := os.Lstat(path)

	return err == nil && info.IsDir()
}
