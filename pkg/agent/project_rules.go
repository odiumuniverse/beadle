package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/digest"
	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
)

type projectRulesSurface struct {
	dir  string
	file string
	slug string
}

func (s *projectRulesSurface) Kind() kind.ID { return kind.Projects }

func (s *projectRulesSurface) Path() string { return filepath.Join(s.dir, s.file) }

func (s *projectRulesSurface) WatchPaths() []string { return []string{s.Path()} }

func (s *projectRulesSurface) Traits() Traits {
	return Traits{
		DefaultMode: config.ModeSync,
		Creatable:   true,
		Note:        "project rules with the agent-sync memory digest block at the top",
	}
}

func (s *projectRulesSurface) key() string { return s.slug + "/" + s.file }

func (s *projectRulesSurface) active() bool {
	return fsutil.Exists(filepath.Join(s.dir, ".git")) || fsutil.Exists(s.Path())
}

func (s *projectRulesSurface) Project(key string, value []byte) (string, []byte, bool) {
	slug, file, ok := strings.Cut(key, "/")
	if !ok || slug != s.slug || file != s.file || !s.active() {
		return "", nil, false
	}

	return key, value, true
}

func (s *projectRulesSurface) Read(context.Context) (Snapshot, error) {
	if !s.active() {
		return Snapshot{Items: kind.Items{}}, nil
	}

	data, present, err := readFile(s.Path())
	if err != nil {
		return Snapshot{}, err
	}

	if !present {
		return Snapshot{Items: kind.Items{}, Present: true}, nil
	}

	body, _, _, err := digest.Strip(data)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%s: %w", s.Path(), err)
	}

	snap := Snapshot{Items: kind.Items{}, Present: true}

	if len(body) > 0 {
		snap.Items[s.key()] = body
	}

	return snap, nil
}

func (s *projectRulesSurface) Write(_ context.Context, desired kind.Items) error {
	if !s.active() {
		return nil
	}

	value, wanted := desired[s.key()]
	if !wanted {
		return s.removeCanonicalFile()
	}

	return s.writeBody(value)
}

func (s *projectRulesSurface) WriteFenced(_ context.Context, block []byte) error {
	if !s.active() {
		return nil
	}

	return s.updateFenced(func(body, fence []byte) ([]byte, bool, error) {
		if bytes.Equal(fence, block) {
			return nil, false, nil
		}

		out, err := digest.Splice(body, block)
		if err != nil {
			return nil, false, err
		}

		return out, true, nil
	})
}

func (s *projectRulesSurface) writeBody(value []byte) error {
	return s.updateFenced(func(body, fence []byte) ([]byte, bool, error) {
		if bytes.Equal(body, value) {
			return nil, false, nil
		}

		out, err := digest.Splice(value, fence)
		if err != nil {
			return nil, false, err
		}

		return out, true, nil
	})
}

func (s *projectRulesSurface) updateFenced(build func(body, fence []byte) ([]byte, bool, error)) error {
	return updateFile(s.Path(), 0o644, func(data []byte, present bool) ([]byte, bool, error) {
		var body, fence []byte

		if present {
			stripped, existing, _, err := digest.Strip(data)
			if err != nil {
				return nil, false, fmt.Errorf("%s: %w", s.Path(), err)
			}

			body, fence = stripped, terminated(existing)
		}

		return build(body, fence)
	})
}

func terminated(fence []byte) []byte {
	if len(fence) == 0 || fence[len(fence)-1] == '\n' {
		return fence
	}

	return append(slices.Clone(fence), '\n')
}

func (s *projectRulesSurface) removeCanonicalFile() error {
	info, err := os.Lstat(s.Path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("stat %s: %w", s.Path(), err)
	}

	if !info.Mode().IsRegular() {
		return nil
	}

	data, _, err := readFile(s.Path())
	if err != nil {
		return err
	}

	body, _, _, err := digest.Strip(data)
	if err != nil {
		return fmt.Errorf("%s: %w", s.Path(), err)
	}

	if len(body) == 0 {
		return nil
	}

	if err := os.Remove(s.Path()); err != nil {
		return fmt.Errorf("remove %s: %w", s.Path(), err)
	}

	return nil
}
