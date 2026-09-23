package skill

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
)

type Tree map[string][]byte

const skillFileName = "SKILL.md"

// FileName is the skill root file every host reads inside a skill directory.
const FileName = skillFileName

const flatSuffix = ".md"

var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func ValidName(name string) bool {
	return namePattern.MatchString(name)
}

// FlatName returns the skill name of a flat copy: a file called `<slug>.md`
// (the skill root file itself, SKILL.md, is not a flat name).
func FlatName(fileName string) (string, bool) {
	name, ok := strings.CutSuffix(fileName, flatSuffix)
	if !ok || !ValidName(name) {
		return "", false
	}

	return name, true
}

// FlatPath returns the flat copy path of a skill name under dir.
func FlatPath(dir, name string) string {
	return filepath.Join(dir, name+flatSuffix)
}

// HasRoot reports whether dir is a skill root: it holds a regular SKILL.md
// directly. The name match is case-sensitive even on a case-insensitive
// filesystem, and a symlink to a regular file counts.
func HasRoot(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}

	for _, entry := range entries {
		if entry.Name() != skillFileName {
			continue
		}

		if entry.Type()&fs.ModeSymlink != 0 {
			info, err := os.Stat(filepath.Join(dir, entry.Name()))

			return err == nil && info.Mode().IsRegular()
		}

		return entry.Type().IsRegular()
	}

	return false
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

		root := filepath.Join(dir, name)
		if !HasRoot(root) {
			continue
		}

		tree, err := ReadTree(root)
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

// ReadTree reads a skill root: a directory tree, or a flat `<name>.md` file
// read as the single-file tree `{"SKILL.md": bytes}`.
func ReadTree(root string) (Tree, error) {
	if info, err := os.Lstat(root); err == nil && info.Mode().IsRegular() {
		data, err := os.ReadFile(root) //nolint:gosec // G304: skill paths are resolved by the tool
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", root, err)
		}

		return Tree{skillFileName: data}, nil
	}

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

// TreeDigest hashes a skill tree deterministically: sorted paths, each line
// carrying the path and the file hash.
func TreeDigest(tree Tree) cas.Hash {
	var builder strings.Builder

	for _, rel := range slices.Sorted(maps.Keys(tree)) {
		builder.WriteString(rel)
		builder.WriteByte(0)
		builder.WriteString(string(cas.HashOf(tree[rel])))
		builder.WriteByte('\n')
	}

	return cas.HashOf([]byte(builder.String()))
}

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
