package state

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/odiumuniverse/agents-sync/pkg/cas"
	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
)

const FileName = "state.json"

const CurrentVersion = 2

const MaxSnapshots = 30

const (
	ReasonModified   = "modified"
	ReasonDeleted    = "deleted"
	ReasonAdded      = "added"
	ReasonMassDelete = "mass-delete"
	ReasonHidden     = "hidden"
)

type Base map[string]cas.Hash

type Conflict struct {
	Kind     kind.ID   `json:"kind"`
	Agent    string    `json:"agent"`
	Key      string    `json:"key"`
	VaultKey string    `json:"vault_key,omitempty"`
	Reason   string    `json:"reason"`
	Base     cas.Hash  `json:"base,omitempty"`
	Vault    cas.Hash  `json:"vault,omitempty"`
	Local    cas.Hash  `json:"local,omitempty"`
	Since    time.Time `json:"since"`
}

func (c Conflict) ID() string {
	sum := sha256.Sum256([]byte(string(c.Kind) + "\x00" + c.Agent + "\x00" + c.Key))

	return hex.EncodeToString(sum[:])[:8]
}

func (c Conflict) TargetKey() string {
	return cmp.Or(c.VaultKey, c.Key)
}

type Snapshot struct {
	At       time.Time `json:"at"`
	Manifest cas.Hash  `json:"manifest"`
}

// Render records the last digest block written into one project file.
type Render struct {
	BlockHash  cas.Hash  `json:"block_hash"`
	InputsHash cas.Hash  `json:"inputs_hash,omitempty"`
	Notes      int       `json:"notes"`
	Omitted    int       `json:"omitted,omitempty"`
	At         time.Time `json:"at"`
}

// Drift counts consecutive syncs that found manual edits inside a digest block.
type Drift struct {
	Count int       `json:"count"`
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
}

type State struct {
	Version   int                         `json:"version"`
	Bases     map[kind.ID]map[string]Base `json:"bases,omitempty"`
	Conflicts []Conflict                  `json:"conflicts,omitempty"`
	Snapshots map[kind.ID][]Snapshot      `json:"snapshots,omitempty"`
	Renders   map[string]Render           `json:"renders,omitempty"`
	Drift     map[string]Drift            `json:"drift,omitempty"`
}

func New() *State {
	return &State{
		Version:   CurrentVersion,
		Bases:     map[kind.ID]map[string]Base{},
		Snapshots: map[kind.ID][]Snapshot{},
		Renders:   map[string]Render{},
		Drift:     map[string]Drift{},
	}
}

func Load(path string) (*State, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is the vault state file
	if errors.Is(err, fs.ErrNotExist) {
		return New(), nil
	}

	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}

	st := New()
	if err := json.Unmarshal(data, st); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}

	if st.Version != CurrentVersion {
		return nil, fmt.Errorf("unsupported state version %d in %s (expected %d)", st.Version, path, CurrentVersion)
	}

	if st.Bases == nil {
		st.Bases = map[kind.ID]map[string]Base{}
	}

	if st.Snapshots == nil {
		st.Snapshots = map[kind.ID][]Snapshot{}
	}

	if st.Renders == nil {
		st.Renders = map[string]Render{}
	}

	if st.Drift == nil {
		st.Drift = map[string]Drift{}
	}

	return st, nil
}

func (s *State) Save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}

	if err := fsutil.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write state: %w", err)
	}

	return nil
}

func (s *State) Base(k kind.ID, agent string) (Base, bool) {
	base, ok := s.Bases[k][agent]

	return base, ok
}

func (s *State) SetBase(k kind.ID, agent string, base Base) {
	if s.Bases[k] == nil {
		s.Bases[k] = map[string]Base{}
	}

	if base == nil {
		base = Base{}
	}

	s.Bases[k][agent] = base
}

func (s *State) Hashes() []cas.Hash {
	seen := map[cas.Hash]struct{}{}

	for _, agents := range s.Bases {
		for _, base := range agents {
			for _, hash := range base {
				seen[hash] = struct{}{}
			}
		}
	}

	for _, c := range s.Conflicts {
		for _, hash := range []cas.Hash{c.Base, c.Vault, c.Local} {
			if hash != "" {
				seen[hash] = struct{}{}
			}
		}
	}

	for _, snapshots := range s.Snapshots {
		for _, snap := range snapshots {
			seen[snap.Manifest] = struct{}{}
		}
	}

	hashes := make([]cas.Hash, 0, len(seen))
	for hash := range seen {
		hashes = append(hashes, hash)
	}

	slices.Sort(hashes)

	return hashes
}

func (s *State) OpenConflicts() []Conflict {
	out := slices.Clone(s.Conflicts)

	slices.SortFunc(out, func(a, b Conflict) int {
		return cmp.Or(
			cmp.Compare(a.Kind, b.Kind),
			cmp.Compare(a.Key, b.Key),
			cmp.Compare(a.Agent, b.Agent),
		)
	})

	return out
}

func (s *State) ConflictsOf(k kind.ID, agent string) []Conflict {
	var out []Conflict

	for _, c := range s.Conflicts {
		if c.Kind == k && c.Agent == agent {
			out = append(out, c)
		}
	}

	return out
}

func (s *State) ReplaceConflicts(k kind.ID, agent string, conflicts []Conflict) {
	since := map[string]time.Time{}

	kept := s.Conflicts[:0]

	for _, c := range s.Conflicts {
		if c.Kind == k && c.Agent == agent {
			since[c.ID()] = c.Since

			continue
		}

		kept = append(kept, c)
	}

	for _, c := range conflicts {
		if previous, ok := since[c.ID()]; ok {
			c.Since = previous
		}

		kept = append(kept, c)
	}

	s.Conflicts = kept
}

func (s *State) Conflict(id string) (Conflict, error) {
	var found []Conflict

	for _, c := range s.Conflicts {
		if strings.HasPrefix(c.ID(), id) {
			found = append(found, c)
		}
	}

	switch len(found) {
	case 0:
		return Conflict{}, fmt.Errorf("no open conflict %q", id)
	case 1:
		return found[0], nil
	default:
		return Conflict{}, fmt.Errorf("conflict id %q is ambiguous (%d matches)", id, len(found))
	}
}

func (s *State) RemoveConflict(id string) {
	s.Conflicts = slices.DeleteFunc(s.Conflicts, func(c Conflict) bool {
		return c.ID() == id
	})
}

func (s *State) AddSnapshot(k kind.ID, snap Snapshot) {
	history := s.Snapshots[k]

	if n := len(history); n > 0 && history[n-1].Manifest == snap.Manifest {
		return
	}

	history = append(history, snap)
	if len(history) > MaxSnapshots {
		history = history[len(history)-MaxSnapshots:]
	}

	s.Snapshots[k] = history
}

func (s *State) History(k kind.ID) []Snapshot {
	return slices.Clone(s.Snapshots[k])
}
