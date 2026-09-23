//nolint:dupl // mirrored by claudeRulesSurface (.claude/rules, A-32): the two per-file surfaces intentionally share the gates/perms/prune shape
package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

const cursorRulesDir = ".cursor/rules"

var cursorRuleName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.mdc$`)

type cursorRulesSurface struct {
	dir string
	id  string
}

func (s *cursorRulesSurface) Kind() kind.ID { return kind.Projects }

func (s *cursorRulesSurface) Path() string {
	return filepath.Join(s.dir, filepath.FromSlash(cursorRulesDir))
}

func (s *cursorRulesSurface) WatchPaths() []string { return []string{s.Path()} }

func (s *cursorRulesSurface) ProjectRel() string { return cursorRulesDir }

func (s *cursorRulesSurface) ProjectDirectory() {}

func (s *cursorRulesSurface) Traits() Traits {
	return Traits{
		DefaultMode: config.ModeSync,
		Creatable:   true,
		Note:        "per-file Cursor project rules in " + cursorRulesDir,
	}
}

func (s *cursorRulesSurface) key(name string) string { return s.id + "/" + cursorRulesDir + "/" + name }

func (s *cursorRulesSurface) rel(key string) (string, bool) {
	group, name, ok := strings.Cut(key, "/"+cursorRulesDir+"/")
	if !ok || group != s.id || !cursorRuleName.MatchString(name) {
		return "", false
	}

	return name, true
}

func (s *cursorRulesSurface) Project(key string, value []byte) (string, []byte, bool) {
	if _, ok := s.rel(key); !ok {
		return "", nil, false
	}

	return key, value, true
}

func (s *cursorRulesSurface) Read(context.Context) (Snapshot, error) {
	dir := s.Path()

	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return Snapshot{Items: kind.Items{}}, nil
	}

	if err != nil {
		return Snapshot{}, fmt.Errorf("read %s: %w", dir, err)
	}

	items := kind.Items{}

	for _, entry := range entries {
		name := entry.Name()
		if !cursorRuleName.MatchString(name) {
			continue
		}

		path := filepath.Join(dir, name)

		if _, err := projectTargetInfo(path); err != nil {
			return Snapshot{}, err
		}

		data, present, err := readFile(path)
		if err != nil {
			return Snapshot{}, err
		}

		if present {
			items[s.key(name)] = data
		}
	}

	return Snapshot{Items: items, Present: true}, nil
}

func (s *cursorRulesSurface) Remove() error {
	snap, err := s.Read(context.Background())
	if err != nil {
		return err
	}

	for key := range snap.Items {
		name, _ := s.rel(key)
		if err := removeProjectFile(filepath.Join(s.Path(), name)); err != nil {
			return err
		}
	}

	return nil
}

func (s *cursorRulesSurface) Write(_ context.Context, desired kind.Items) error {
	dir := s.Path()

	current, err := s.Read(context.Background())
	if err != nil {
		return err
	}

	for _, key := range current.Items.Keys() {
		if _, keep := desired[key]; keep {
			continue
		}

		name, _ := s.rel(key)

		if err := removeProjectFile(filepath.Join(dir, name)); err != nil {
			return err
		}
	}

	names := make([]string, 0, len(desired))

	for key := range desired {
		if name, ok := s.rel(key); ok {
			names = append(names, name)
		}
	}

	if len(names) == 0 {
		return nil
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	slices.Sort(names)

	for _, name := range names {
		if err := writeProjectFile(filepath.Join(dir, name), desired[s.key(name)], 0o644); err != nil {
			return err
		}
	}

	return nil
}
