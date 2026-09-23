package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/frontmatter"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// fileCodec maps one host's files of a canonical file kind to the canonical
// document and back.
type fileCodec[T any] interface {
	// parse decodes a host file into a canonical document. ok is false when
	// the file is not a definition of this kind.
	parse(path string, data []byte) (T, bool, error)
	// fields renders the host frontmatter fields for a canonical document.
	// existing holds the current file bytes so the codec can preserve the
	// entries it does not manage.
	fields(doc T, existing []byte) ([]frontmatter.Field, string, error)
	// managed lists the frontmatter keys the codec owns.
	managed() []string
	// strayKeys lists frontmatter keys the host schema does not know and that
	// would flip the file to a legacy decoder. Writes drop them.
	strayKeys(data []byte) []string
	// audit lists what the host cannot express from a canonical document
	// (unmappable values, lossy fields). The doctor reports them.
	audit(doc T) []string
}

// filePullNoticer is implemented by codecs that report host-side hazards
// while reading a file: constructs the host does not expand but that become
// active on hosts that do.
type filePullNoticer interface {
	pullNotes(data []byte) []string
}

// fileWriter is implemented by codecs whose files are not markdown
// frontmatter: they render the whole file themselves.
type fileWriter[T any] interface {
	render(doc T, existing []byte) ([]byte, error)
}

// fileModel adapts a canonical document package to the shared file surface.
type fileModel[T any] interface {
	// parse decodes a canonical item; name is the item identity from its key.
	parse(name string, value []byte) (T, error)
	// render encodes the canonical item.
	render(doc T) []byte
	// name returns the canonical identity of a parsed document.
	name(doc T) string
	// validName reports whether an identity is a canonical slug.
	validName(name string) bool
}

// fileVisitor receives the files a file surface can read.
type fileVisitor struct {
	file    func(path string, data []byte)
	nested  func(dir string)
	symlink func(path string, broken bool)
}

// fileNameCheck decides when a host file name that differs from the canonical
// name is worth a warning.
type fileNameCheck int

const (
	// nameCheckWarn warns when the file name differs from the canonical name.
	nameCheckWarn fileNameCheck = iota
	// nameCheckNone accepts any file name (the host treats it as secondary).
	nameCheckNone
	// nameCheckNestedAgent also accepts the <name>/agent.md layout.
	nameCheckNestedAgent
)

// fileSurface presents a vault file canon to one host directory.
type fileSurface[T any] struct {
	kind       kind.ID
	label      string
	readDirs   []string
	writeDir   string
	recursive  bool
	nestedNote string
	// exts lists the file extensions the surface reads; the first one names
	// new files. An empty list means markdown (.md).
	exts []string
	// nameCheck selects the file-name-versus-identity rule.
	nameCheck fileNameCheck
	model     fileModel[T]
	codec     fileCodec[T]
	traits    Traits
}

func (s *fileSurface[T]) Kind() kind.ID { return s.kind }

func (s *fileSurface[T]) Path() string { return s.writeDir }

func (s *fileSurface[T]) WatchPaths() []string { return slices.Clone(s.readDirs) }

func (s *fileSurface[T]) Traits() Traits { return s.traits }

func (s *fileSurface[T]) Read(context.Context) (Snapshot, error) {
	snap := Snapshot{
		Items:      kind.Items{},
		ReadOnly:   map[string]string{},
		Unreadable: map[string]string{},
	}

	seen := map[string]string{}

	err := s.visit(fileVisitor{
		file: func(path string, data []byte) {
			s.addFile(path, data, &snap, seen)
		},
		nested: func(dir string) {
			if s.nestedNote == "" {
				return
			}

			if path, ok := s.firstHostFile(dir); ok {
				snap.Warnings = append(snap.Warnings, fmt.Sprintf("%s: %s", path, s.nestedNote))
			}
		},
		symlink: func(path string, broken bool) {
			base := filepath.Base(path)
			if !s.matchExt(base) {
				// A symlink the surface would not read (an extensionless
				// name or a directory link) owns no canonical name and must
				// not shadow one.
				return
			}

			key := s.trimExt(base) + ".md"

			if broken {
				snap.Unreadable[key] = path

				return
			}

			snap.ReadOnly[key] = "symlink to " + path
		},
	})
	if err != nil {
		return Snapshot{}, err
	}

	return snap, nil
}

func (s *fileSurface[T]) visit(visitor fileVisitor) error {
	for _, dir := range s.readDirs {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			return fmt.Errorf("read %s directory %s: %w", s.label, dir, err)
		}

		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())

			switch {
			case entry.IsDir():
				if s.recursive {
					if err := s.walkNested(path, visitor); err != nil {
						return err
					}
				} else {
					visitor.nested(path)
				}
			case entry.Type()&fs.ModeSymlink != 0:
				if visitor.symlink != nil {
					_, err := filepath.EvalSymlinks(path)
					visitor.symlink(path, err != nil)
				}
			case s.matchExt(entry.Name()):
				data, err := os.ReadFile(path) //nolint:gosec // host directories are resolved by the adapter
				if err != nil {
					return fmt.Errorf("read %s: %w", path, err)
				}

				visitor.file(path, data)
			}
		}
	}

	return nil
}

func (s *fileSurface[T]) walkNested(dir string, visitor fileVisitor) error {
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		if entry.Type()&fs.ModeSymlink != 0 {
			if visitor.symlink != nil {
				_, err := filepath.EvalSymlinks(path)
				visitor.symlink(path, err != nil)
			}

			return nil
		}

		if !s.matchExt(entry.Name()) {
			return nil
		}

		data, err := os.ReadFile(path) //nolint:gosec // host directories are resolved by the adapter
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}

		visitor.file(path, data)

		return nil
	})
}

// matchExt reports whether a host file name carries one of the surface
// extensions.
func (s *fileSurface[T]) matchExt(name string) bool {
	for _, ext := range s.exts {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}

	return len(s.exts) == 0 && strings.HasSuffix(name, ".md")
}

// trimExt strips the surface extension from a host file name.
func (s *fileSurface[T]) trimExt(name string) string {
	for _, ext := range s.exts {
		if trimmed, ok := strings.CutSuffix(name, ext); ok {
			return trimmed
		}
	}

	if len(s.exts) == 0 {
		return strings.TrimSuffix(name, ".md")
	}

	return name
}

// writeName names a new host file for a canonical item.
func (s *fileSurface[T]) writeName(name string) string {
	if len(s.exts) > 0 {
		return name + s.exts[0]
	}

	return name + ".md"
}

// nameMismatch reports whether a host file name deserves the mismatch
// warning, per the surface rule.
func (s *fileSurface[T]) nameMismatch(path, base, name string) bool {
	switch s.nameCheck {
	case nameCheckNone:
		return false
	case nameCheckNestedAgent:
		if base == "agent" && filepath.Base(filepath.Dir(path)) == name {
			return false
		}

		return base != name
	case nameCheckWarn:
		return base != name
	}

	return base != name
}

// itemName returns the canonical identity of an item key.
func itemName(key string) string {
	return strings.TrimSuffix(key, ".md")
}

func (s *fileSurface[T]) addFile(path string, data []byte, snap *Snapshot, seen map[string]string) {
	doc, ok, err := s.codec.parse(path, data)
	if err != nil {
		snap.Warnings = append(snap.Warnings, fmt.Sprintf("%s: %v; the file is left untouched", path, err))

		return
	}

	if !ok {
		return
	}

	name := s.model.name(doc)
	if !s.model.validName(name) {
		snap.Warnings = append(snap.Warnings, fmt.Sprintf("%s: %s name %q is not a lowercase slug; the file is left untouched", path, s.label, name))

		return
	}

	if s.nameMismatch(path, s.trimExt(filepath.Base(path)), name) {
		snap.Warnings = append(snap.Warnings, fmt.Sprintf("%s: the file name does not match the %s name %q; the file is kept in place", path, s.label, name))
	}

	if first, taken := seen[name]; taken {
		snap.Warnings = append(snap.Warnings, fmt.Sprintf("%s %q is defined in both %s and %s; the first is used", s.label, name, first, path))

		return
	}

	if noticer, ok := s.codec.(filePullNoticer); ok {
		for _, note := range noticer.pullNotes(data) {
			snap.Warnings = append(snap.Warnings, fmt.Sprintf("%s: %s", path, note))
		}
	}

	// Only the file that reaches the canon can be normalized: a shadowed
	// duplicate is never written, so its strays must not force a rewrite.
	if strays := s.codec.strayKeys(data); len(strays) > 0 {
		snap.NeedsRewrite = true

		snap.Warnings = append(snap.Warnings, fmt.Sprintf("%s: keys outside the host schema: %s; they are dropped on write", path, strings.Join(strays, ", ")))
	}

	seen[name] = path
	snap.Present = true
	snap.Items[name+".md"] = s.model.render(doc)
}

func (s *fileSurface[T]) Write(ctx context.Context, desired kind.Items) error {
	current, err := s.Read(ctx)
	if err != nil {
		return err
	}

	paths, err := s.locate()
	if err != nil {
		return err
	}

	for _, key := range current.Items.Keys() {
		name := itemName(key)
		if _, keep := desired[key]; keep || current.ReadOnly[key] != "" {
			continue
		}

		if err := os.Remove(paths[name]); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove %s %s: %w", s.label, key, err)
		}

		delete(paths, name)
	}

	for _, key := range desired.Keys() {
		if current.ReadOnly[key] != "" {
			continue
		}

		if err := s.writeOne(key, desired[key], paths); err != nil {
			return err
		}
	}

	return nil
}

// locate maps every readable canonical name to the file that holds it, so
// writes happen in place and never fork a duplicate definition.
func (s *fileSurface[T]) locate() (map[string]string, error) {
	paths := map[string]string{}

	err := s.visit(fileVisitor{file: func(path string, data []byte) {
		doc, ok, err := s.codec.parse(path, data)
		if err != nil || !ok {
			return
		}

		name := s.model.name(doc)
		if !s.model.validName(name) {
			return
		}

		if _, taken := paths[name]; !taken {
			paths[name] = path
		}
	}})
	if err != nil {
		return nil, err
	}

	return paths, nil
}

func (s *fileSurface[T]) writeOne(key string, value []byte, paths map[string]string) error {
	name := itemName(key)

	doc, err := s.model.parse(name, value)
	if err != nil {
		return fmt.Errorf("%s %s: %w", s.label, key, err)
	}

	if !s.model.validName(s.model.name(doc)) {
		// A documentation file next to the definitions is not an error; the
		// read side reports it and the item simply does not reach the host.
		return nil
	}

	path := paths[name]
	if path == "" {
		path = filepath.Join(s.writeDir, s.writeName(name))
	}

	if info, err := os.Lstat(path); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return nil
	}

	existing, err := os.ReadFile(path) //nolint:gosec // host directories are resolved by the adapter
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}

	out, err := s.render(doc, existing)
	if errors.Is(err, errCommandInexpressible) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("%s %s: %w", s.label, key, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}

	if err := fsutil.WriteFileAtomic(path, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}

// render patches the host file and drops the frontmatter keys the host
// schema does not know: they flip OpenCode to its legacy decoder, which
// ignores the v2 permissions this codec writes. The read side reports them
// before they are dropped.
func (s *fileSurface[T]) render(doc T, existing []byte) ([]byte, error) {
	if writer, ok := s.codec.(fileWriter[T]); ok {
		return writer.render(doc, existing)
	}

	fields, body, err := s.codec.fields(doc, existing)
	if err != nil {
		return nil, err
	}

	out, err := frontmatter.Patch(existing, s.codec.managed(), fields, body)
	if err != nil {
		return nil, err
	}

	strays := s.codec.strayKeys(out)
	if len(strays) == 0 {
		return out, nil
	}

	return frontmatter.Drop(out, strays)
}

// Project renders the host-expressible projection of a canonical document so
// the engine compares the agent against the form the host can keep.
func (s *fileSurface[T]) Project(key string, value []byte) (string, []byte, bool) {
	doc, err := s.model.parse(itemName(key), value)
	if err != nil {
		return key, value, true
	}

	rendered, err := s.render(doc, nil)
	if errors.Is(err, errCommandInexpressible) {
		// The host has no syntax for this item: it is hidden from the view
		// instead of being reported as a missing write.
		return "", nil, false
	}

	if err != nil {
		return key, value, true
	}

	back, ok, err := s.codec.parse(key, rendered)
	if err != nil || !ok {
		return "", nil, false
	}

	return key, s.model.render(back), true
}

// firstHostFile returns the first file of a directory tree whose extension
// the surface reads.
func (s *fileSurface[T]) firstHostFile(dir string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}

	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())

		if entry.IsDir() {
			if found, ok := s.firstHostFile(path); ok {
				return found, true
			}

			continue
		}

		if s.matchExt(entry.Name()) {
			return path, true
		}
	}

	return "", false
}

// noticer is implemented by file surfaces: it reports what the host cannot
// express from a canonical item.
type noticer interface {
	notices(key string, value []byte) []Notice
}

// notices lists the host-expressiveness diagnostics of one canonical item.
func (s *fileSurface[T]) notices(key string, value []byte) []Notice {
	doc, err := s.model.parse(itemName(key), value)
	if err != nil {
		return nil
	}

	var out []Notice

	for _, note := range s.codec.audit(doc) {
		out = append(out, Notice{Message: fmt.Sprintf("%s: %s", key, note)})
	}

	return out
}
