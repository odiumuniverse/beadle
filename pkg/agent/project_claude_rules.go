//nolint:dupl // the per-file surface intentionally mirrors cursorRulesSurface (same gates/perms/prune, different dir and pattern)
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

const claudeRulesDir = ".claude/rules"

var claudeRuleName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.md$`)

type claudeRulesSurface struct {
	dir string
	id  string
}

func (s *claudeRulesSurface) Kind() kind.ID { return kind.Projects }

func (s *claudeRulesSurface) Path() string {
	return filepath.Join(s.dir, filepath.FromSlash(claudeRulesDir))
}

func (s *claudeRulesSurface) WatchPaths() []string { return []string{s.Path()} }

func (s *claudeRulesSurface) ProjectRel() string { return claudeRulesDir }

func (s *claudeRulesSurface) ProjectDirectory() {}

func (s *claudeRulesSurface) Traits() Traits {
	return Traits{
		DefaultMode: config.ModeSync,
		Creatable:   true,
		Note:        "per-file Claude project rules in " + claudeRulesDir,
	}
}

func (s *claudeRulesSurface) key(name string) string { return s.id + "/" + claudeRulesDir + "/" + name }

func (s *claudeRulesSurface) rel(key string) (string, bool) {
	group, name, ok := strings.Cut(key, "/"+claudeRulesDir+"/")
	if !ok || group != s.id || !claudeRuleName.MatchString(name) {
		return "", false
	}

	return name, true
}

func (s *claudeRulesSurface) Project(key string, value []byte) (string, []byte, bool) {
	if _, ok := s.rel(key); !ok {
		return "", nil, false
	}

	return key, value, true
}

func (s *claudeRulesSurface) Read(context.Context) (Snapshot, error) {
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
		if !claudeRuleName.MatchString(name) {
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

func (s *claudeRulesSurface) Remove() error {
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

func (s *claudeRulesSurface) Write(_ context.Context, desired kind.Items) error {
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
