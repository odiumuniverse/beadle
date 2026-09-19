package lock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

var ErrBusy = errors.New("vault is locked by another beadle process")

const retryDelay = 100 * time.Millisecond

func Acquire(ctx context.Context, path string) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}

	fileLock := flock.New(path)

	locked, err := fileLock.TryLockContext(ctx, retryDelay)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, fmt.Errorf("%w: %w", ErrBusy, err)
		}

		return nil, fmt.Errorf("lock %s: %w", path, err)
	}

	if !locked {
		return nil, ErrBusy
	}

	return fileLock.Unlock, nil
}
