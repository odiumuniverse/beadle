package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/odiumuniverse/agents-sync/pkg/cas"
	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
)

const currentVersion = 1

type Entry struct {
	Base     cas.Hash            `json:"base,omitempty"`
	Vault    cas.Hash            `json:"vault,omitempty"`
	Agents   map[string]cas.Hash `json:"agents,omitempty"`
	Conflict bool                `json:"conflict,omitempty"`
	Deleted  bool                `json:"deleted,omitempty"`
}

type State struct {
	Version   int               `json:"version"`
	Resources map[string]*Entry `json:"resources,omitempty"`
}

func New() *State {
	return &State{Version: currentVersion, Resources: map[string]*Entry{}}
}

func Load(path string) (*State, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: reading the vault registry path is the intended function
	if errors.Is(err, fs.ErrNotExist) {
		return New(), nil
	}

	if err != nil {
		return nil, fmt.Errorf("read registry: %w", err)
	}

	state := New()
	if err := json.Unmarshal(data, state); err != nil {
		return nil, fmt.Errorf("parse registry: %w", err)
	}

	if state.Resources == nil {
		state.Resources = map[string]*Entry{}
	}

	return state, nil
}

func (s *State) Save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode registry: %w", err)
	}

	if err := fsutil.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write registry: %w", err)
	}

	return nil
}

func (s *State) Conflicts() int {
	n := 0

	for _, entry := range s.Resources {
		if entry.Conflict {
			n++
		}
	}

	return n
}
