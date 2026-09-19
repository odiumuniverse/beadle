package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

type projectMCPSurface struct {
	dir string
	rel string
	id  string
}

func (s *projectMCPSurface) Kind() kind.ID { return kind.Projects }

func (s *projectMCPSurface) Path() string { return filepath.Join(s.dir, filepath.FromSlash(s.rel)) }

func (s *projectMCPSurface) WatchPaths() []string { return []string{s.Path()} }

func (s *projectMCPSurface) ProjectRel() string { return s.rel }

func (s *projectMCPSurface) Traits() Traits {
	return Traits{
		DefaultMode: config.ModeSync,
		Creatable:   true,
		Note:        "project MCP servers in " + s.rel,
	}
}

func (s *projectMCPSurface) key() string { return s.id + "/" + s.rel }

func (s *projectMCPSurface) Skeleton() []byte {
	data, err := kind.CanonicalJSON([]byte(`{"mcpServers":{}}`))
	if err != nil {
		return []byte(`{"mcpServers":{}}`)
	}

	return data
}

func (s *projectMCPSurface) Project(key string, value []byte) (string, []byte, bool) {
	group, rel, ok := strings.Cut(key, "/")
	if !ok || group != s.id || rel != s.rel {
		return "", nil, false
	}

	return key, value, true
}

func (s *projectMCPSurface) Read(context.Context) (Snapshot, error) {
	path := s.Path()

	if _, err := projectTargetInfo(path); err != nil {
		return Snapshot{}, err
	}

	data, present, err := readFile(path)
	if err != nil {
		return Snapshot{}, err
	}

	if !present {
		return Snapshot{Items: kind.Items{}}, nil
	}

	return Snapshot{Items: kind.Items{s.key(): data}, Present: true}, nil
}

func (s *projectMCPSurface) Write(_ context.Context, desired kind.Items) error {
	value, wanted := desired[s.key()]
	if !wanted {
		return removeProjectFile(s.Path())
	}

	return writeProjectFile(s.Path(), value, 0o644)
}

func (s *projectMCPSurface) Remove() error {
	return removeProjectFile(s.Path())
}

func removeProjectFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	if !info.Mode().IsRegular() {
		return nil
	}

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove %s: %w", path, err)
	}

	return nil
}

func CheckProjectTarget(path string) error {
	_, err := projectTargetInfo(path)

	return err
}

func projectTargetInfo(path string) (fs.FileInfo, error) {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("stat %s: %w", path, err)
	case info.Mode()&fs.ModeSymlink != 0:
		return nil, fmt.Errorf("%s: refusing to touch a symlink", path)
	case !info.Mode().IsRegular():
		return nil, fmt.Errorf("%s: refusing to touch a non-regular file", path)
	case linkCount(info) != 1:
		return nil, fmt.Errorf("%s: refusing to touch a file with several hard links", path)
	}

	return info, nil
}

func writeProjectFile(path string, data []byte, defaultPerm fs.FileMode) error {
	return updateProjectFile(path, defaultPerm, func([]byte, bool) ([]byte, bool, error) {
		return data, true, nil
	})
}

func updateProjectFile(path string, defaultPerm fs.FileMode, build func(data []byte, present bool) ([]byte, bool, error)) error {
	return updateFileMode(path, defaultPerm, build, true)
}
