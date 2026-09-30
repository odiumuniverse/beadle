package agent

import (
	"bytes"
	"context"
	"path/filepath"

	"github.com/odiumuniverse/verger/pkg/hostpath"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

// dshChainSurface is one instruction file of the DSH project chain: a file
// between the project root (the nearest .git) and the working directory. The
// rel stays root-relative, so the canon keeps the chain layout
// (<id>/AGENTS.md, <id>/sub/CLAUDE.md).
type dshChainSurface struct {
	path string
	rel  string
	id   string
}

func (s *dshChainSurface) Kind() kind.ID { return kind.Projects }

func (s *dshChainSurface) Path() string { return s.path }

func (s *dshChainSurface) WatchPaths() []string { return []string{s.path} }

func (s *dshChainSurface) ProjectRel() string { return s.rel }

func (s *dshChainSurface) ProjectRootRel() {}

func (s *dshChainSurface) Traits() Traits {
	return Traits{
		DefaultMode: config.ModePull,
		Note:        "DSH project instruction chain; pulled into the canon, written back only after an explicit mode flip",
	}
}

func (s *dshChainSurface) key() string { return s.id + "/" + s.rel }

func (s *dshChainSurface) Project(key string, value []byte) (string, []byte, bool) {
	if key != s.key() {
		return "", nil, false
	}

	return key, value, true
}

func (s *dshChainSurface) Read(context.Context) (Snapshot, error) {
	if _, err := projectTargetInfo(s.path); err != nil {
		return Snapshot{}, err
	}

	data, present, err := readFile(s.path)
	if err != nil {
		return Snapshot{}, err
	}

	if !present || len(bytes.TrimSpace(data)) == 0 {
		return Snapshot{Items: kind.Items{}, Present: true}, nil
	}

	return Snapshot{Items: kind.Items{s.key(): data}, Present: true}, nil
}

func (s *dshChainSurface) Write(_ context.Context, desired kind.Items) error {
	value, wanted := desired[s.key()]
	if !wanted {
		return removeProjectFile(s.path)
	}

	return writeProjectFile(s.path, value, 0o644)
}

// DSHChain builds one surface per instruction file candidate of the project
// chain: every AGENTS.md/CLAUDE.md between the nearest .git root (inclusive)
// and cwd (inclusive), root first. A candidate without a file stays as an
// inert surface, so deleting the file prunes its canon element. Identical
// contents render once: the engine collapses a file whose content an earlier
// enabled file of the chain already carries. No .git above cwd means no chain.
func DSHChain(cwd, id string) []Surface {
	root, ok := hostpath.ProjectChainRoot(cwd, fsutil.Exists)
	if !ok {
		return nil
	}

	var surfaces []Surface

	for _, path := range hostpath.DSHProjectChain(root, cwd) {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}

		surfaces = append(surfaces, &dshChainSurface{
			path: path,
			rel:  filepath.ToSlash(rel),
			id:   id,
		})
	}

	return surfaces
}
