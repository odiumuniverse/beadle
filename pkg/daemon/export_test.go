package daemon

import "context"

// RegisterSystemdForTest exposes the systemd registration sequence: the
// registrar itself is platform-neutral (it only shells out through the runner),
// so the call order is testable on any host.
func RegisterSystemdForTest(ctx context.Context, spec Spec, run Runner) error {
	return registerSystemd(ctx, spec, run)
}

// LaunchdEnvForTest exposes the plist parser: UnitEnvFromFile only reaches it
// on darwin, so tests cover it explicitly on every host.
func LaunchdEnvForTest(content string) map[string]string {
	return launchdEnv(content)
}

// SystemdEnvFromUnitForTest exposes the unit-file parser for the same reason.
func SystemdEnvFromUnitForTest(content string) map[string]string {
	return systemdEnvFromUnit(content)
}
