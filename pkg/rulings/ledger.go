package rulings

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"time"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

const FileName = "rulings.json"

const (
	StateObserved  = "observed"
	StateSuggested = "suggested"
	StateTrusted   = "trusted"

	SourceHuman = "human"
	SourceModel = "model"

	RulingTakeVault = "take-vault"
	RulingTakeAgent = "take-agent"
)

type Ruling struct {
	Signature     Signature `json:"signature"`
	Ruling        string    `json:"ruling"`
	State         string    `json:"state"`
	Source        string    `json:"source"`
	Confirmations int       `json:"confirmations"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	LastAppliedAt time.Time `json:"last_applied_at,omitzero"`
	Hits          int       `json:"hits"`
}

type Ledger struct {
	Rulings map[string]Ruling `json:"rulings"`
}

func New() *Ledger {
	return &Ledger{Rulings: map[string]Ruling{}}
}

func Load(path string) (*Ledger, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is the vault rulings file
	if errors.Is(err, fs.ErrNotExist) {
		return New(), nil
	}

	if err != nil {
		return nil, fmt.Errorf("read rulings: %w", err)
	}

	ledger := New()
	if err := json.Unmarshal(data, ledger); err != nil {
		return nil, fmt.Errorf("parse rulings: %w", err)
	}

	if ledger.Rulings == nil {
		ledger.Rulings = map[string]Ruling{}
	}

	return ledger, nil
}

func (l *Ledger) Save(path string) error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("encode rulings: %w", err)
	}

	return fsutil.WriteFileAtomic(path, append(data, '\n'), 0o600)
}

func (l *Ledger) All() []Ruling {
	sorted := make([]Ruling, 0, len(l.Rulings))

	for _, ruling := range l.Rulings {
		sorted = append(sorted, ruling)
	}

	slices.SortFunc(sorted, func(a, b Ruling) int {
		return cmp.Or(
			cmp.Compare(a.Signature.Kind, b.Signature.Kind),
			cmp.Compare(a.Signature.Target, b.Signature.Target),
			cmp.Compare(a.Signature.Divergence, b.Signature.Divergence),
			cmp.Compare(a.Signature.Scope, b.Signature.Scope),
		)
	})

	return sorted
}

func (l *Ledger) Get(sig Signature) (Ruling, bool) {
	ruling, ok := l.Rulings[sig.Hash()]

	return ruling, ok
}

func (l *Ledger) Put(sig Signature, ruling Ruling) {
	l.Rulings[sig.Hash()] = ruling
}

func (l *Ledger) Forget(hash string) bool {
	if _, ok := l.Rulings[hash]; !ok {
		return false
	}

	delete(l.Rulings, hash)

	return true
}

func (l *Ledger) Trust(sig Signature, scope, ruling string, at time.Time) Ruling {
	existing, ok := l.Get(sig)
	if ok {
		sig = existing.Signature
	} else {
		existing = Ruling{CreatedAt: at, Source: SourceHuman}
	}

	existing.Signature = sig
	existing.Ruling = ruling
	existing.State = StateTrusted
	existing.Source = SourceHuman
	existing.UpdatedAt = at

	l.Put(sig, existing)

	return existing
}

func (l *Ledger) Observe(sig Signature, ruling string, at time.Time) Ruling {
	existing, ok := l.Get(sig)
	if !ok {
		existing = Ruling{
			Signature: sig,
			Ruling:    ruling,
			State:     StateObserved,
			Source:    SourceModel,
			CreatedAt: at,
			UpdatedAt: at,
		}

		l.Put(sig, existing)

		return existing
	}

	if existing.Ruling != ruling || existing.State == StateTrusted {
		existing.Ruling = ruling
		existing.UpdatedAt = at

		l.Put(sig, existing)
	}

	return existing
}

func (l *Ledger) Confirm(sig Signature, ruling string, at time.Time) Ruling {
	existing, ok := l.Get(sig)
	if !ok {
		return l.Observe(sig, ruling, at)
	}

	if existing.Ruling == ruling {
		existing.Confirmations++
		existing.UpdatedAt = at

		l.Put(sig, existing)

		return existing
	}

	return l.Demote(sig, at)
}

func (l *Ledger) Demote(sig Signature, at time.Time) Ruling {
	existing, ok := l.Get(sig)
	if !ok {
		return Ruling{}
	}

	switch existing.State {
	case StateTrusted:
		existing.State = StateSuggested
	case StateSuggested, StateObserved:
		existing.State = StateObserved
	}

	existing.Confirmations = 0
	existing.UpdatedAt = at

	l.Put(sig, existing)

	return existing
}

func (l *Ledger) Applied(sig Signature, at time.Time) Ruling {
	existing, ok := l.Get(sig)
	if !ok {
		return Ruling{}
	}

	existing.Hits++
	existing.LastAppliedAt = at
	existing.UpdatedAt = at

	l.Put(sig, existing)

	return existing
}

type Match struct {
	Ruling Ruling
	Exact  bool
	Found  bool
}

func (l *Ledger) Match(sig Signature) Match {
	for _, scope := range scopeChain(sig.Scope) {
		candidate := sig
		candidate.Scope = scope

		if ruling, ok := l.Get(candidate); ok {
			return Match{Ruling: ruling, Exact: true, Found: true}
		}
	}

	for _, scope := range scopeChain(sig.Scope) {
		if ruling, ok := l.relaxed(sig.Kind, sig.Target, sig.Divergence, scope); ok {
			return Match{Ruling: ruling, Exact: false, Found: true}
		}
	}

	return Match{}
}

func (l *Ledger) relaxed(k kind.ID, target, divergence, scope string) (Ruling, bool) {
	prefix, _, _ := cutTarget(target)

	for _, hash := range slices.Sorted(maps.Keys(l.Rulings)) {
		ruling := l.Rulings[hash]

		if ruling.Signature.Kind != k || ruling.Signature.Divergence != divergence || ruling.Signature.Scope != scope {
			continue
		}

		candidatePrefix, _, _ := cutTarget(ruling.Signature.Target)
		if candidatePrefix != "" && candidatePrefix == prefix {
			return ruling, true
		}
	}

	return Ruling{}, false
}

func scopeChain(scope string) []string {
	trimmed := normalizeScope(scope)

	if trimmed == ScopeGlobal {
		return []string{ScopeGlobal}
	}

	return []string{trimmed, ScopeGlobal}
}

func cutTarget(target string) (string, string, bool) {
	for i := range len(target) {
		if target[i] == '.' || target[i] == '/' {
			return target[:i], target[i+1:], true
		}
	}

	return target, "", false
}
