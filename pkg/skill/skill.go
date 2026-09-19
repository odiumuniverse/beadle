package skill

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
)

type Tree map[string][]byte

var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func ValidName(name string) bool {
	return namePattern.MatchString(name)
}

func ReadDir(dir string) (map[string]Tree, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Tree{}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read skills directory: %w", err)
	}

	skills := make(map[string]Tree)

	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 {
			continue
		}

		name := entry.Name()
		if !ValidName(name) {
			continue
		}

		tree, err := ReadTree(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}

		skills[name] = tree
	}

	return skills, nil
}

var junkNames = map[string]struct{}{
	".DS_Store":    {},
	"Thumbs.db":    {},
	".git":         {},
	"node_modules": {},
	"__pycache__":  {},
}

func skipJunk(root, path string, entry fs.DirEntry) (bool, error) {
	if _, junk := junkNames[entry.Name()]; !junk || path == root {
		return false, nil
	}

	if entry.IsDir() {
		return true, filepath.SkipDir
	}

	return true, nil
}

func ReadTree(root string) (Tree, error) {
	tree := Tree{}

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if skip, walkErr := skipJunk(root, path, entry); skip {
			return walkErr
		}

		if entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		data, err := os.ReadFile(path) //nolint:gosec // G304: skill paths are resolved by the tool
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}

		tree[filepath.ToSlash(rel)] = data

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}

	return tree, nil
}

func WriteTree(dir, name string, tree Tree) error {
	if len(tree) == 0 {
		return nil
	}

	for rel, data := range tree {
		path, err := FilePath(dir, name, rel)
		if err != nil {
			return err
		}

		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return fmt.Errorf("create skill directory: %w", err)
		}

		if err := fsutil.WriteFileAtomic(path, data, 0o644); err != nil {
			return fmt.Errorf("write skill file: %w", err)
		}
	}

	return nil
}

func SyncTree(dir, name string, tree Tree) error {
	if err := WriteTree(dir, name, tree); err != nil {
		return err
	}

	root := filepath.Join(dir, name)
	stale := make([]string, 0)

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if skip, walkErr := skipJunk(root, path, entry); skip {
			return walkErr
		}

		if entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		if _, ok := tree[filepath.ToSlash(rel)]; !ok {
			stale = append(stale, path)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("sync skill tree: %w", err)
	}

	for _, path := range stale {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove stale skill file: %w", err)
		}
	}

	return nil
}

func Group(items map[string][]byte) map[string]Tree {
	trees := map[string]Tree{}

	for key, data := range items {
		name, rel, ok := strings.Cut(key, "/")
		if !ok {
			continue
		}

		if trees[name] == nil {
			trees[name] = Tree{}
		}

		trees[name][rel] = data
	}

	return trees
}

func Flatten(skills map[string]Tree) map[string][]byte {
	items := map[string][]byte{}

	for name, tree := range skills {
		for rel, data := range tree {
			items[name+"/"+rel] = data
		}
	}

	return items
}

func FilePath(dir, name, rel string) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("invalid skill name %q", name)
	}

	if !validRel(rel) {
		return "", fmt.Errorf("invalid skill path %q", rel)
	}

	return filepath.Join(dir, name, filepath.FromSlash(rel)), nil
}

type Manifest map[string]cas.Hash

func ManifestOf(tree Tree) Manifest {
	manifest := make(Manifest, len(tree))

	for rel, data := range tree {
		manifest[rel] = cas.HashOf(data)
	}

	return manifest
}

func (m Manifest) Marshal() ([]byte, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}

	return append(data, '\n'), nil
}

func (m Manifest) Hash() cas.Hash {
	data, err := m.Marshal()
	if err != nil {
		return ""
	}

	return cas.HashOf(data)
}

func ParseManifest(data []byte) (Manifest, error) {
	if len(data) == 0 {
		return Manifest{}, nil
	}

	manifest := Manifest{}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	return manifest, nil
}

func validRel(rel string) bool {
	if rel == "" || filepath.IsAbs(rel) {
		return false
	}

	for part := range strings.SplitSeq(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}

	return true
}
