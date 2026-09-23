package agent

import (
	"context"
	"errors"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

var ErrNotConfigured = errors.New("agent config not found")

type Agent struct {
	ID       string
	Name     string
	OptIn    bool
	Detect   func() (bool, error)
	Surfaces []Surface
}

func (a *Agent) Surface(k kind.ID) Surface {
	for _, s := range a.Surfaces {
		if s.Kind() == k {
			return s
		}
	}

	return nil
}

func (a *Agent) SurfacesOf(k kind.ID) []Surface {
	var out []Surface

	for _, s := range a.Surfaces {
		if s.Kind() == k {
			out = append(out, s)
		}
	}

	return out
}

type Snapshot struct {
	Items      kind.Items
	Present    bool
	ReadOnly   map[string]string
	Unreadable map[string]string
	// Warnings carries per-run diagnostics the surface produced while
	// reading (skipped files, duplicate definitions, unsupported layouts).
	Warnings []string
	// NeedsRewrite marks a snapshot whose host files are readable but whose
	// on-disk form the surface must normalize on the next write (for example
	// frontmatter keys the host schema does not know). The engine must not
	// treat such a view as already synced.
	NeedsRewrite bool
}

type Surface interface {
	Kind() kind.ID
	Path() string
	WatchPaths() []string
	Read(ctx context.Context) (Snapshot, error)
	Write(ctx context.Context, desired kind.Items) error
	Traits() Traits
}

type Traits struct {
	DefaultMode config.Mode
	Creatable   bool
	ReloadHint  string
	Note        string
}

type Projector interface {
	Project(key string, value []byte) (pkey string, pvalue []byte, ok bool)
}

type ProjectFile interface {
	ProjectRel() string
}

// ProjectRootRel marks a project surface whose ProjectRel is relative to the
// project root (the git checkout root) instead of the process working
// directory. The DSH instruction chain keeps root-relative rels because it
// spans the directories above cwd.
type ProjectRootRel interface {
	ProjectRootRel()
}

// ProjectDirectory marks a project surface that holds a directory of files
// (per-file rules): the single-file target gate does not apply to it.
type ProjectDirectory interface {
	ProjectDirectory()
}

type ProjectSkeleton interface {
	Skeleton() []byte
}

type ProjectRemover interface {
	Remove() error
}

func All(home, cwd string) []*Agent {
	return []*Agent{
		ClaudeCode(home, cwd),
		OpenCode(home, cwd),
		GeminiCLI(home, cwd),
		AntigravityCLI(home, cwd),
		Cursor(home, cwd),
		Codex(home, cwd),
		Pi(home, cwd),
		Kilo(home, cwd),
		// DSH comes after SharedSkills: when DSH_HOME points at ~/.agents the
		// two share the skills directory, and the read-view dedup gives the
		// path to the first surface that is present — a pull-only surface must
		// not shadow the writer.
		SharedSkills(home),
		DSH(home, cwd),
	}
}

func ByID(agents []*Agent, id string) *Agent {
	for _, a := range agents {
		if a.ID == id {
			return a
		}
	}

	return nil
}
