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
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/odiumuniverse/beadle/pkg/agentid"
	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

const FileName = "state.json"

// CurrentVersion is the schema version this build reads and writes. Version 3
// gave every agent the canonical id beadle and verger share, so the per-agent
// bases, the conflicts, the refusals and the adoptions are keyed by them.
const CurrentVersion = 3

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
	// Complement lists, per kind, the canon names beadle delivers through the
	// host file while the bundle manages the kind (the servers a bundle cannot
	// carry). Only these copies may leave the file again.
	Complement map[kind.ID][]string `json:"complement,omitempty"`
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

// HookModule is one hook module beadle copied into a host's module directory:
// the digest of the bytes it wrote and the plugin key they came from.
type HookModule struct {
	Digest cas.Hash `json:"digest"`
	Source string   `json:"source,omitempty"`
	Phase  string   `json:"phase,omitempty"`
	Name   string   `json:"name,omitempty"`
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
	// HookModules records the hook module files beadle copied into each host's
	// module directory, keyed by the absolute target path: the digest of the
	// bytes it wrote and the plugin key they came from. A file whose digest no
	// longer matches the record is foreign: it is never overwritten or removed,
	// and a withdrawal removes exactly the recorded modules.
	HookModules map[string]HookModule `json:"hook_modules,omitempty"`
	// BundleOptOut lists hosts the user disabled explicitly: the unattended
	// attempt leaves them alone until `beadle bundles enable <host>`.
	BundleOptOut []string `json:"bundle_opt_out,omitempty"`
	// Home records the home directory the vault was last synced from. The
	// plugin readers walk the CURRENT home, so a run from a foreign home (a
	// container, CI, a temp HOME) sees no plugins at all: without this record
	// the retirement pass would delete the pivots of a vault that belongs to
	// another machine. The record is written on the first sync and only
	// compared afterwards.
	Home string `json:"home,omitempty"`
	// FarmMigration records the one-time move of the plugin farm to the plugin
	// manager: the stage it reached, the backup it took, the keys it adopted
	// and the exact paths it removed. It is the resume point, not a log.
	FarmMigration *FarmMigration `json:"farm_migration,omitempty"`
	// migration carries the one-time renames Load applied, so the caller can
	// report them; it is never serialized.
	migration []string
	// migrated records that Load applied the renames in memory only. Save
	// clears it, so a mutating command can persist the migrated state even
	// after the notes have already been rendered.
	migrated bool
	// loaded is the version the file on disk carried, before any in-memory
	// migration. A migration that recognises a layout an older build wrote
	// cannot ask the migrated Version: by the time anything reads it, every
	// state says CurrentVersion.
	loaded int
}

// MigrationNotes lists the one-time renames Load applied. It is empty for a
// state that was already current.
func (s *State) MigrationNotes() []string {
	return slices.Clone(s.migration)
}

// LoadedVersion is the version the state file carried when it was read, before
// any in-memory migration. It is 0 for a state this process created, and for a
// file that was already current it equals CurrentVersion. A migration that has
// to recognise a layout only an older build wrote asks this, never Version.
func (s *State) LoadedVersion() int { return s.loaded }

// Migrated reports that the state was migrated in memory and not yet
// persisted; the next Save writes the renamed state to the vault.
func (s *State) Migrated() bool {
	return s.migrated
}

// TakeMigrationNotes returns the pending migration notes and clears them, so
// every rename is reported exactly once.
func (s *State) TakeMigrationNotes() []string {
	notes := s.migration
	s.migration = nil

	return notes
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

	if st.Version > CurrentVersion {
		return nil, &SchemaNewerError{Path: path, Found: st.Version, Supported: CurrentVersion}
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

	// The version the file carried, kept before the in-memory migration so a
	// caller can still tell a state an older build wrote from a current one.
	st.loaded = st.Version

	// The migration stays in memory: a read-only command must not write the
	// vault. The first command that saves the state persists it and reports
	// every rename.
	notes, changed := st.migrate()
	st.migration = notes
	st.migrated = changed

	// The skill-tree cache is rekeyed on every read, not only on an older
	// schema: a state at the current version can still carry the absolute keys
	// an earlier build of this same version wrote, and those keys are what make
	// a synced vault machine-specific.
	if rekeyed := st.rekeySkillTrees(); len(rekeyed) > 0 {
		st.migration = append(st.migration, rekeyed...)
		st.migrated = true
	}

	if rekeyed := st.rekeyHookModules(); len(rekeyed) > 0 {
		st.migration = append(st.migration, rekeyed...)
		st.migrated = true
	}

	return st, nil
}

// SchemaNewerError reports a state.json written by a newer beadle. It is a
// type, not a formatted string, because the caller above pkg/state — the
// exit-code classifier — has to recognise the situation without matching on
// prose: "this file is from the future" is exit class 7, and a script must be
// able to tell that from a crash.
type SchemaNewerError struct {
	Path      string
	Found     int
	Supported int
}

// Error implements error. The message is the one a user must act on, so it
// names the document, both versions and the way out: a file written by a
// newer beadle is never rewritten by an older one, and the only fix is to
// upgrade.
func (e *SchemaNewerError) Error() string {
	return fmt.Sprintf("state %s: written by a newer beadle (version %d > %d) — upgrade beadle to read it",
		e.Path, e.Found, e.Supported)
}

// IsSchemaNewer reports whether err is, or wraps, a state written by a newer
// beadle, and returns it when it is.
func IsSchemaNewer(err error) (*SchemaNewerError, bool) {
	return errors.AsType[*SchemaNewerError](err)
}

// migrate applies the one-time flips of every older state schema and reports
// each one. It returns changed=true whenever the stored schema version is
// older, even when no flip happened.
func (s *State) migrate() ([]string, bool) {
	if s.Version >= CurrentVersion {
		return nil, false
	}

	notes := s.renameAgentIDs()
	s.Version = CurrentVersion

	return notes, true
}

// renameAgentIDs rewrites every per-agent key and field by the canonical id
// the historical id was renamed to, and reports one note per id. A canonical
// id that is already taken wins: two entries cannot be merged without guessing
// which base the user kept.
func (s *State) renameAgentIDs() []string {
	renamed := map[string]bool{}

	for k, perAgent := range s.Bases {
		for old, canonical := range agentid.Aliases() {
			base, ok := perAgent[old]
			if !ok {
				continue
			}

			if _, taken := perAgent[canonical]; !taken {
				delete(perAgent, old)
				perAgent[canonical] = base
			}

			renamed[old] = true
		}

		s.Bases[k] = perAgent
	}

	for i := range s.Conflicts {
		recordRename(renamed, &s.Conflicts[i].Agent)
	}

	for i := range s.Refusals {
		recordRename(renamed, &s.Refusals[i].Agent)
	}

	for i := range s.Adoptions {
		recordRename(renamed, &s.Adoptions[i].Host)
	}

	return renameNotes(renamed)
}

// HomePrefix opens a key that stands for a path inside the home directory. It
// is spelled the way a shell spells it, and it is the only portable spelling:
// "/Users/a/.claude/skills" and "/home/a/.claude/skills" are one file on two
// machines and two different strings.
const HomePrefix = "~/"

// HomeKey is the machine-independent key for one path under the home
// directory: the path relative to home, spelled with a leading "~/". A path
// outside home keeps its absolute form, because there is nothing portable to
// say about it and a key that pretended otherwise would collide.
//
// The state travels between machines, and a key is what beadle looks a thing
// up by. An absolute key is a name only the machine that wrote it can read: the
// record is not lost, it is inert, and the next sync writes a second one beside
// it. Every map in the state whose key is a path under home goes through here.
func HomeKey(path, home string) string {
	if home == "" {
		return path
	}

	rel, err := filepath.Rel(filepath.Clean(home), filepath.Clean(path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}

	return HomePrefix + filepath.ToSlash(rel)
}

// HomePath is the inverse of HomeKey: it turns a stored key back into a path
// on this machine. A key that is already absolute comes back unchanged, which
// is what lets a pre-migration state stay usable.
func HomePath(key, home string) string {
	if !strings.HasPrefix(key, HomePrefix) {
		return key
	}

	return filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(key, HomePrefix)))
}

// rekeySkillTrees rewrites cache keys a machine wrote into the portable form,
// and reports one note when it moved any.
//
// It runs against s.Home - the home the vault was last synced from - because
// that is the only way to read an absolute key written elsewhere: on the second
// machine the current home is different, and "/Users/a/..." is not under it. A
// key under no recorded home is left alone rather than guessed at; it keeps
// working, it is just not portable yet.
func (s *State) rekeySkillTrees() []string {
	moved := rekeyHomeKeys(s.SkillTrees, s.Home, HomeKey)
	if moved == 0 {
		return nil
	}

	return []string{fmt.Sprintf("skill tree cache: %d key(s) made machine-independent", moved)}
}

// rekeyHookModules rewrites the recorded hook module keys the same way, and for
// a worse reason than the cache: a module record is the only proof beadle has
// that a file it is about to overwrite is its own. A key the machine cannot
// read turns beadle's own file into a stranger's, and a stranger's file is left
// alone forever.
func (s *State) rekeyHookModules() []string {
	moved := rekeyHomeKeys(s.HookModules, s.Home, HomeKey)
	if moved == 0 {
		return nil
	}

	return []string{fmt.Sprintf("hook module records: %d key(s) made machine-independent", moved)}
}

// rekeyHomeKeys rewrites every key of one path-keyed map into its portable
// form and reports how many moved. A canonical key already present wins: it is
// what the current machine would read, and two entries for one file cannot both
// be right.
func rekeyHomeKeys[V any](entries map[string]V, home string, key func(string, string) string) int {
	if len(entries) == 0 || home == "" {
		return 0
	}

	moved := 0

	for stored, value := range entries {
		canonical := key(stored, home)
		if canonical == stored {
			continue
		}

		if _, taken := entries[canonical]; !taken {
			entries[canonical] = value
		}

		delete(entries, stored)

		moved++
	}

	return moved
}

// recordRename rewrites one agent-id field in place and records the historical
// id it held, so the user is told about the rename once.
func recordRename(renamed map[string]bool, field *string) {
	old := *field
	*field = agentid.Canonical(old)

	if old != *field {
		renamed[old] = true
	}
}

// renameNotes reports one flip per renamed id, once each: a vault holds a base
// for the same agent in several kinds, and the user sees one rename.
func renameNotes(renamed map[string]bool) []string {
	if len(renamed) == 0 {
		return nil
	}

	notes := make([]string, 0, len(renamed))
	for _, old := range slices.Sorted(maps.Keys(renamed)) {
		notes = append(notes, fmt.Sprintf("agent %s is now named %s", old, agentid.Canonical(old)))
	}

	return notes
}

func (s *State) Save(path string) error {
	s.Version = CurrentVersion
	s.migrated = false

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

// PackageResult is one package the vault's spec named, and what happened to it
// on this machine. The words are the user's (§1): `delivered` is what the
// library wrote, `skipped` is what it left alone, and the note says why.
type PackageResult struct {
	Package string `json:"package"`
	Host    string `json:"host"`
	State   string `json:"state"`
	Note    string `json:"note,omitempty"`
}

// PackagesReport is the section `beadle sync` prints for the vault's package
// spec. It is always printed, so a consumer can tell "applied nothing" from
// "never looked".
type PackagesReport struct {
	// Spec is true when the vault carries a spec at all.
	Spec    bool            `json:"spec"`
	Results []PackageResult `json:"results"`
	Error   string          `json:"error,omitempty"`
	// LookedAt names the spec file this run read. A "no spec" answer without
	// it is the one message a user cannot act on: the spec may be there, in a
	// vault the reader did not look at.
	LookedAt string `json:"looked_at,omitempty"`
}
