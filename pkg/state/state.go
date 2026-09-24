package state

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

const FileName = "state.json"

const CurrentVersion = 2

const MaxSnapshots = 30

// MaxRefusals bounds the refusal journal kept in state.json.
const MaxRefusals = 50

const (
	RefusalStaleConflict   = "stale-conflict"
	RefusalUnknownConflict = "unknown-conflict"
	RefusalRiskyChange     = "risky-change"
	RefusalInvalidContent  = "invalid-content"
	RefusalAmbiguous       = "ambiguous"
)

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

const (
	// VerifyExecuted marks a bundle the host CLI confirmed.
	VerifyExecuted = "executed"
	// VerifyUnverifiable marks a bundle rendered from the documented
	// contracts but not confirmed, because the host CLI is missing.
	VerifyUnverifiable = "unverifiable"
	// VerifyFailed marks a bundle the host rejected or could not confirm.
	VerifyFailed = "failed"
)

// WithdrawnItem records one canon element the bundle enable pass removed
// from a host file surface, so disable can materialize it again.
type WithdrawnItem struct {
	Kind   kind.ID   `json:"kind"`
	Name   string    `json:"name"`
	Digest cas.Hash  `json:"digest,omitempty"`
	At     time.Time `json:"at"`
}

// AutoAttempt records the single unattended bundle enable attempt made for a
// host and rendered version, so a failed or unverifiable probe is not retried
// on every sync; the retry is explicit (`beadle bundles enable <host>`).
type AutoAttempt struct {
	Version string    `json:"version"`
	At      time.Time `json:"at"`
	Tier    string    `json:"tier"`
}

// PendingRefresh records a rendered bundle version the host has not taken
// yet because the syncing process could not reach the host CLI (a service
// runs without the user's shell PATH). Since is the first deferral of that
// version, so the warning is emitted once.
type PendingRefresh struct {
	Version string    `json:"version"`
	Since   time.Time `json:"since"`
}

// BundleState records one host's native bundle registration. Version,
// VerifyTier and ProbeNote describe what the host serves; Pending is the
// rendered version still waiting for a run that reaches the host CLI.
type BundleState struct {
	Enabled           bool                    `json:"enabled"`
	Version           string                  `json:"version,omitempty"`
	Registered        bool                    `json:"registered,omitempty"`
	VerifyTier        string                  `json:"verify_tier,omitempty"`
	ProbeNote         string                  `json:"probe_note,omitempty"`
	Pending           *PendingRefresh         `json:"pending,omitempty"`
	PendingWithdrawal bool                    `json:"pending_withdrawal,omitempty"`
	Withdrawn         []WithdrawnItem         `json:"withdrawn,omitempty"`
	SavedModes        map[kind.ID]config.Mode `json:"saved_modes,omitempty"`
	AutoAttempt       *AutoAttempt            `json:"auto_attempt,omitempty"`
}

// Verified reports whether the host confirmed the bundle, or the contracts
// were at least validated while the host CLI was unavailable.
func (b BundleState) Verified() bool {
	return b.Registered && (b.VerifyTier == VerifyExecuted || b.VerifyTier == VerifyUnverifiable)
}

// Serves reports whether the host reads the bundle at all: a registered
// bundle keeps serving its last installed copy even when a later probe
// failed. Verified gates what beadle writes; Serves answers what the host
// sees, so diagnostics use it and the write path never does.
func (b BundleState) Serves() bool {
	return b.Registered
}

// Refusal records one conflict resolution the engine declined to apply.
type Refusal struct {
	At      time.Time `json:"at"`
	ID      string    `json:"id"`
	Kind    kind.ID   `json:"kind"`
	Agent   string    `json:"agent"`
	Key     string    `json:"key"`
	Code    string    `json:"code"`
	Message string    `json:"message"`
}

// Adoption records one foreign skill copy beadle moved aside on a host
// surface, so unadopt can bring the original back.
type Adoption struct {
	Host     string    `json:"host"`
	Name     string    `json:"name"`
	Provider string    `json:"provider"`
	Target   string    `json:"target,omitempty"`
	Digest   cas.Hash  `json:"digest,omitempty"`
	At       time.Time `json:"at"`
}

// SkillTree caches one skill tree's digest. Fingerprint is the listing of the
// tree the digest was computed from (relative path, size, modification time),
// Latest is the newest file mtime in that listing and Stamp the moment the
// digest was computed: a scan trusts the digest only while the listing is
// unchanged and the newest file is older than Stamp minus the racy window.
type SkillTree struct {
	Digest      cas.Hash  `json:"digest"`
	Fingerprint cas.Hash  `json:"fingerprint"`
	Latest      time.Time `json:"latest,omitzero"`
	Stamp       time.Time `json:"stamp"`
}

type State struct {
	Version   int                         `json:"version"`
	Bases     map[kind.ID]map[string]Base `json:"bases,omitempty"`
	Conflicts []Conflict                  `json:"conflicts,omitempty"`
	Snapshots map[kind.ID][]Snapshot      `json:"snapshots,omitempty"`
	Renders   map[string]Render           `json:"renders,omitempty"`
	Drift     map[string]Drift            `json:"drift,omitempty"`
	Bundles   map[string]BundleState      `json:"bundles,omitempty"`
	Refusals  []Refusal                   `json:"refusals,omitempty"`
	Adoptions []Adoption                  `json:"adoptions,omitempty"`
	// SkillTrees caches skill tree digests by absolute root path, so the
	// coverage and shadow scans read only the trees that changed. It is
	// written on the passes that save the state and never on read-only
	// commands.
	SkillTrees map[string]SkillTree `json:"skill_trees,omitempty"`
	// HookRenders records the hook commands beadle rendered into each host's
	// user-level hooks file, so a later sync replaces or removes exactly its
	// own entries and leaves foreign hooks alone.
	HookRenders map[string][]string `json:"hook_renders,omitempty"`
	// BundleOptOut lists hosts the user disabled explicitly: the unattended
	// attempt leaves them alone until `beadle bundles enable <host>`.
	BundleOptOut []string `json:"bundle_opt_out,omitempty"`
}

// BundleOptedOut reports that the user disabled this host's bundle by hand.
func (s *State) BundleOptedOut(host string) bool {
	return slices.Contains(s.BundleOptOut, host)
}

// OptOutBundle records the explicit disable of one host bundle.
func (s *State) OptOutBundle(host string) {
	if s.BundleOptedOut(host) {
		return
	}

	s.BundleOptOut = append(s.BundleOptOut, host)
	slices.Sort(s.BundleOptOut)
}

// ClearBundleOptOut forgets the explicit disable, so the host may be enabled
// again (by hand or by the unattended attempt).
func (s *State) ClearBundleOptOut(host string) {
	s.BundleOptOut = slices.DeleteFunc(s.BundleOptOut, func(entry string) bool { return entry == host })

	if len(s.BundleOptOut) == 0 {
		s.BundleOptOut = nil
	}
}

func New() *State {
	return &State{
		Version:   CurrentVersion,
		Bases:     map[kind.ID]map[string]Base{},
		Snapshots: map[kind.ID][]Snapshot{},
		Renders:   map[string]Render{},
		Drift:     map[string]Drift{},
		Bundles:   map[string]BundleState{},
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

	if st.Bundles == nil {
		st.Bundles = map[string]BundleState{}
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

// SkillTreeFor returns the cached digest entry of one skill root.
func (s *State) SkillTreeFor(root string) (SkillTree, bool) {
	entry, ok := s.SkillTrees[root]

	return entry, ok
}

// SetSkillTree records one skill tree's cached digest.
func (s *State) SetSkillTree(root string, entry SkillTree) {
	if s.SkillTrees == nil {
		s.SkillTrees = map[string]SkillTree{}
	}

	s.SkillTrees[root] = entry
}

// DropSkillTrees removes the cache entries the predicate matches and reports
// how many were dropped.
func (s *State) DropSkillTrees(drop func(root string) bool) int {
	before := len(s.SkillTrees)

	maps.DeleteFunc(s.SkillTrees, func(root string, _ SkillTree) bool {
		return drop(root)
	})

	return before - len(s.SkillTrees)
}

// AdoptionFor returns the adoption record of one host and skill.
func (s *State) AdoptionFor(host, name string) (Adoption, bool) {
	for _, record := range s.Adoptions {
		if record.Host == host && record.Name == name {
			return record, true
		}
	}

	return Adoption{}, false
}

// RemoveAdoption drops the adoption record of one host and skill and reports
// whether it existed.
func (s *State) RemoveAdoption(host, name string) bool {
	before := len(s.Adoptions)

	s.Adoptions = slices.DeleteFunc(s.Adoptions, func(record Adoption) bool {
		return record.Host == host && record.Name == name
	})

	return len(s.Adoptions) != before
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

// AddRefusal appends one refusal and keeps only the last MaxRefusals records.
func (s *State) AddRefusal(r Refusal) {
	s.Refusals = append(s.Refusals, r)

	if len(s.Refusals) > MaxRefusals {
		s.Refusals = s.Refusals[len(s.Refusals)-MaxRefusals:]
	}
}

// LastRefusal returns the most recent refusal, if any.
func (s *State) LastRefusal() (Refusal, bool) {
	if len(s.Refusals) == 0 {
		return Refusal{}, false
	}

	return s.Refusals[len(s.Refusals)-1], true
}
