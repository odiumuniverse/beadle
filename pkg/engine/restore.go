package engine

import (
	"context"
	"fmt"

	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
)

func (e *Engine) History(k kind.ID) ([]state.Snapshot, error) {
	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		return nil, err
	}

	return st.History(k), nil
}

func (e *Engine) Restore(ctx context.Context, k kind.ID, index int) (*Report, error) {
	release, err := e.lock(ctx)
	if err != nil {
		return nil, err
	}

	defer release()

	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		return nil, err
	}

	history := st.History(k)

	if index < 0 {
		index += len(history)
	}

	if index < 0 || index >= len(history) {
		return nil, fmt.Errorf("%s has %d snapshot(s): no snapshot at that position", k, len(history))
	}

	items, err := e.loadSnapshot(history[index])
	if err != nil {
		return nil, err
	}

	if err := e.saveVault(k, items); err != nil {
		return nil, err
	}

	return e.sync(ctx, SyncOptions{})
}
