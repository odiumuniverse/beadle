package memory

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
)

const noteSuffix = ".md"

// Tree maps note file names to their content.
type Tree map[string][]byte

// Slug converts a path into the Claude project slug: the absolute path with
// every separator replaced by a dash.
func Slug(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}

	return strings.ReplaceAll(abs, "/", "-")
}

// ValidSlug reports whether name can be a project slug directory: a single
// path element that is local, non-hidden and non-empty.
func ValidSlug(name string) bool {
	return filepath.IsLocal(name) && !strings.ContainsRune(name, '/') && !strings.HasPrefix(name, ".")
}

// ValidNote reports whether name can be a memory note file: a local, non-hidden
// markdown file name without path separators.
func ValidNote(name string) bool {
	return filepath.IsLocal(name) && !strings.ContainsRune(name, '/') &&
		!strings.HasPrefix(name, ".") && strings.HasSuffix(name, noteSuffix) && len(name) > len(noteSuffix)
}

// ReadDir reads the flat two-level layout under root: slug directories holding
// regular markdown notes. Missing roots, symlinks, hidden names (at any level),
// nested directories and foreign files are skipped.
func ReadDir(root string) (map[string]Tree, error) {
	info, err := os.Lstat(root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return map[string]Tree{}, nil
	case err != nil:
		return nil, fmt.Errorf("read memory directory: %w", err)
	case info.Mode()&fs.ModeSymlink != 0:
		return nil, fmt.Errorf("memory path %s is a symlink", root)
	case !info.IsDir():
		return nil, fmt.Errorf("read memory directory %s: not a directory", root)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read memory directory: %w", err)
	}

	slugs := make(map[string]Tree)

	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 {
			continue
		}

		slug := entry.Name()
		if !ValidSlug(slug) {
			continue
		}

		tree, err := readTree(filepath.Join(root, slug))
		if err != nil {
			return nil, err
		}

		if tree == nil {
			continue
		}

		slugs[slug] = tree
	}

	return slugs, nil
}

func readTree(slugDir string) (Tree, error) {
	entries, err := os.ReadDir(slugDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read memory directory: %w", err)
	}

	tree := Tree{}

	for _, entry := range entries {
		if !entry.Type().IsRegular() || !ValidNote(entry.Name()) {
			continue
		}

		path := filepath.Join(slugDir, entry.Name())

		data, err := os.ReadFile(path) //nolint:gosec // G304: memory paths are resolved by the tool
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}

		tree[entry.Name()] = data
	}

	return tree, nil
}

// Flatten turns slug trees into item keys of the form "<slug>/<note>".
func Flatten(slugs map[string]Tree) map[string][]byte {
	items := map[string][]byte{}

	for slug, tree := range slugs {
		for note, data := range tree {
			items[slug+"/"+note] = data
		}
	}

	return items
}

// Group turns item keys of the form "<slug>/<note>" into slug trees, dropping
// keys that are not a valid slug and note pair.
func Group(items map[string][]byte) map[string]Tree {
	slugs := map[string]Tree{}

	for key, data := range items {
		slug, note, ok := strings.Cut(key, "/")
		if !ok || !ValidSlug(slug) || !ValidNote(note) {
			continue
		}

		if slugs[slug] == nil {
			slugs[slug] = Tree{}
		}

		slugs[slug][note] = data
	}

	return slugs
}

// SyncTree writes the notes of one slug under root, creating the slug directory
// as needed. A symlink at any level is an error rather than a place to write
// through.
func SyncTree(root, slug string, tree Tree) error {
	if !ValidSlug(slug) {
		return fmt.Errorf("invalid memory slug %q", slug)
	}

	for note := range tree {
		if !ValidNote(note) {
			return fmt.Errorf("invalid memory note %q", note)
		}
	}

	if len(tree) == 0 {
		return nil
	}

	if err := checkNotSymlink(root); err != nil {
		return err
	}

	dir := filepath.Join(root, slug)

	if err := checkNotSymlink(dir); err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create memory directory: %w", err)
	}

	paths := make(map[string]string, len(tree))

	for note := range tree {
		path := filepath.Join(dir, note)

		if err := checkNotSymlink(path); err != nil {
			return err
		}

		paths[note] = path
	}

	for note, data := range tree {
		if err := fsutil.WriteFileAtomic(paths[note], data, 0o644); err != nil {
			return fmt.Errorf("write memory note: %w", err)
		}
	}

	return nil
}

func checkNotSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}

		return fmt.Errorf("inspect memory path %s: %w", path, err)
	}

	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("memory path %s is a symlink", path)
	}

	return nil
}
