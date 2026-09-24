package daemon

import "context"

// RegisterSystemdForTest exposes the systemd registration sequence: the
// registrar itself is platform-neutral (it only shells out through the runner),
// so the call order is testable on any host.
func RegisterSystemdForTest(ctx context.Context, spec Spec, run Runner) error {
	return registerSystemd(ctx, spec, run)
}
