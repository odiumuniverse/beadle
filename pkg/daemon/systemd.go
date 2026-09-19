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

	content := `[Unit]
Description=Beadle watcher
After=default.target

[Service]
Type=simple
ExecStart=` + strings.Join(args, " ") + `
Restart=on-failure
RestartSec=2
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=no
MemoryMax=128M
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=default.target
`

	return path, content, nil
}

func registerSystemd(ctx context.Context, spec Spec, run Runner) error {
	if err := run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}

	if err := run(ctx, "systemctl", "--user", "enable", "--now", unitName(labelOrDefault(spec))); err != nil {
		return fmt.Errorf("systemctl enable: %w", err)
	}

	return nil
}

func unregisterSystemd(ctx context.Context, spec Spec, run Runner) error {
	name := unitName(labelOrDefault(spec))

	_ = run(ctx, "systemctl", "--user", "disable", "--now", name)
	_ = run(ctx, "systemctl", "--user", "daemon-reload")

	return nil
}
