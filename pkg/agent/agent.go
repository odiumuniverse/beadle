package agent

import (
	"context"
	"errors"

	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
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

type Snapshot struct {
	Items    kind.Items
	Present  bool
	ReadOnly map[string]string
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

func All(home, cwd string) []*Agent {
	return []*Agent{
		ClaudeCode(home),
		OpenCode(home, cwd),
		GeminiCLI(home, cwd),
		AntigravityCLI(home),
		Cursor(home),
		SharedSkills(home),
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
