package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/rulings"
	"github.com/odiumuniverse/beadle/pkg/state"
)

type RulingEvent struct {
	Signature rulings.Signature `json:"signature"`
	Ruling    string            `json:"ruling"`
	Kind      kind.ID           `json:"kind"`
	Agent     string            `json:"agent"`
	Key       string            `json:"key"`
}

func (e *Engine) loadRulings() *rulings.Ledger {
	ledger, err := rulings.Load(e.vault.RulingsPath())
	if err != nil {
		return rulings.New()
	}

	return ledger
}

func (e *Engine) beginRulings(opts SyncOptions) {
	if opts.DryRun {
		return
	}

	e.rulings = e.loadRulings()
}

func (e *Engine) saveRulingsWhenDirty(report *Report) error {
	if e.rulings == nil || !e.rulingsDirty {
		return nil
	}

	dir := e.vault.RulingsDir()

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	if err := e.rulings.Save(filepath.Join(dir, rulings.FileName)); err != nil {
		return err
	}

	e.rulingsDirty = false

	return nil
}

func (e *Engine) finishRulings(report *Report) error {
	err := e.saveRulingsWhenDirty(report)

	e.rulings = nil
	e.rulingsDirty = false

	return err
}

func (e *Engine) liftRulings(report *Report) {
	for _, kr := range report.Kinds {
		report.RulingsApplied = append(report.RulingsApplied, kr.RulingsApplied...)
		report.RulingSuggestions = append(report.RulingSuggestions, kr.RulingSuggestions...)
	}
}

func (e *Engine) conflictScope(c state.Conflict) string {
	if c.Kind == kind.Projects {
		if id := e.projectIdentity().ID; id != "" {
			return "project:" + id
		}
	}

	return "host:" + c.Agent
}

func (e *Engine) signatureFor(c state.Conflict, base, vault, local []byte) (rulings.Signature, error) {
	return rulings.ComputeSignature(c.Kind, c.Reason, c.Key, c.VaultKey, e.conflictScope(c), base, vault, local)
}

func (e *Engine) saveLedger(ledger *rulings.Ledger) error {
	dir := e.vault.RulingsDir()

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	return ledger.Save(filepath.Join(dir, rulings.FileName))
}

func (e *Engine) updateRulingAfterResolve(ledger *rulings.Ledger, c state.Conflict, res Resolution, at time.Time) (bool, *RulingEvent) {
	base, vault, local, err := e.ConflictValues(c)
	if err != nil {
		return false, nil
	}

	sig, err := e.signatureFor(c, base, vault, local)
	if err != nil {
		return false, nil
	}

	ruling := effectiveRuling(res, vault, local)

	before, existed := ledger.Get(sig)

	if !existed {
		ledger.Observe(sig, ruling, at)

		return true, nil
	}

	after := ledger.Confirm(sig, ruling, at)

	if before.State == after.State {
		return true, nil
	}

	return true, &RulingEvent{Signature: sig, Ruling: before.Ruling, Kind: c.Kind, Agent: c.Agent, Key: c.Key}
}

func effectiveRuling(res Resolution, vault, local []byte) string {
	switch res.Take {
	case TakeAgent:
		return rulings.RulingTakeAgent
	case TakeVault:
		return rulings.RulingTakeVault
	case TakeFile, TakeContent:
		switch {
		case same(res.Content, local):
			return rulings.RulingTakeAgent
		case same(res.Content, vault):
			return rulings.RulingTakeVault
		default:
			return ""
		}
	default:
		return ""
	}
}

func (e *Engine) rulingIssues() []Issue {
	ledger, err := rulings.Load(e.vault.RulingsPath())
	if err != nil {
		return nil
	}

	trusted := 0

	var issues []Issue

	now := e.now()

	for _, ruling := range ledger.All() {
		if ruling.State != rulings.StateTrusted {
			continue
		}

		trusted++

		if !ruling.LastAppliedAt.IsZero() && now.Sub(ruling.LastAppliedAt) > 90*24*time.Hour {
			issues = append(issues, Issue{
				Severity: SeverityWarn,
				Kind:     ruling.Signature.Kind,
				Message: fmt.Sprintf("trusted ruling %s for %s has not applied in over 90 days; consider beadle rulings forget %s",
					ruling.Signature.Hash(), ruling.Signature.Target, ruling.Signature.Hash()),
			})
		}
	}

	if trusted > 0 {
		issues = append(issues, Issue{Severity: SeverityInfo, Message: fmt.Sprintf("%d trusted ruling(s)", trusted)})
	}

	return issues
}
