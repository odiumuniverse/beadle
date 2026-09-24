package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

const systemdSubdir = ".config/systemd/user"

func RenderSystemd(spec Spec) (string, string, error) {
	args, err := argsFor(spec)
	if err != nil {
		return "", "", err
	}

	label := labelOrDefault(spec)
	name := unitName(label)
	path := filepath.Join(spec.Home, systemdSubdir, name)

	var b strings.Builder

	b.WriteString(`[Unit]
Description=Beadle watcher
After=default.target

[Service]
Type=simple
ExecStart=` + systemdCommand(args) + `
Restart=on-failure
RestartSec=2
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=no
MemoryMax=128M
StandardOutput=journal
StandardError=journal
`)

	for _, pair := range EnvPairs(spec.Env) {
		b.WriteString("Environment=" + systemdEnv(pair[0], pair[1]) + "\n")
	}

	b.WriteString(`
[Install]
WantedBy=default.target
`)

	return path, b.String(), nil
}

// systemdCommand renders an ExecStart command line: systemd splits unquoted
// words on spaces, so every argument is quoted (a path with a space — the
// binary or the vault — must survive the unit).
func systemdCommand(args []string) string {
	quoted := make([]string, 0, len(args))

	for _, arg := range args {
		quoted = append(quoted, `"`+systemdEscape(arg)+`"`)
	}

	return strings.Join(quoted, " ")
}

// systemdEscape escapes one systemd-quoted value: quotes and backslashes
// escape with a backslash.
func systemdEscape(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
}

// systemdEnv renders one Environment= assignment.
func systemdEnv(key, value string) string {
	return `"` + key + `=` + systemdEscape(value) + `"`
}

func registerSystemd(ctx context.Context, spec Spec, run Runner) error {
	name := unitName(labelOrDefault(spec))

	if err := run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}

	if err := run(ctx, "systemctl", "--user", "enable", "--now", name); err != nil {
		return fmt.Errorf("systemctl enable: %w", err)
	}

	// enable --now is a no-op on an already active unit, so a reinstall would
	// keep the previous environment: restart applies the rewritten unit, the
	// same way the launchd path bootouts and bootstraps.
	if err := run(ctx, "systemctl", "--user", "restart", name); err != nil {
		return fmt.Errorf("systemctl restart: %w", err)
	}

	return nil
}

func unregisterSystemd(ctx context.Context, spec Spec, run Runner) error {
	name := unitName(labelOrDefault(spec))

	_ = run(ctx, "systemctl", "--user", "disable", "--now", name)
	_ = run(ctx, "systemctl", "--user", "daemon-reload")

	return nil
}
