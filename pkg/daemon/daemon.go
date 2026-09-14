package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const DefaultLabel = "com.agentsync.watch"

type Spec struct {
	Label      string
	Binary     string
	Args       []string
	Home       string
	LogPath    string
	ErrLogPath string
	FileLimit  int
}

type Runner func(ctx context.Context, name string, args ...string) error

func Render(spec Spec) (string, string, error) {
	switch runtime.GOOS {
	case "darwin":
		return RenderLaunchd(spec)
	case "linux":
		return RenderSystemd(spec)
	default:
		return "", "", fmt.Errorf("daemon: unsupported platform %s", runtime.GOOS)
	}
}

func Install(ctx context.Context, spec Spec, run Runner) (string, error) {
	path, content, err := Render(spec)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("create service directory: %w", err)
	}

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("write service file: %w", err)
	}

	if err := register(ctx, spec, run); err != nil {
		return "", err
	}

	return path, nil
}

func Uninstall(ctx context.Context, spec Spec, run Runner) (string, error) {
	path, _, err := Render(spec)
	if err != nil {
		return "", err
	}

	if err := unregister(ctx, spec, run); err != nil {
		return "", err
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("remove service file: %w", err)
	}

	return path, nil
}

func register(ctx context.Context, spec Spec, run Runner) error {
	switch runtime.GOOS {
	case "darwin":
		return registerLaunchd(ctx, spec, run)
	case "linux":
		return registerSystemd(ctx, spec, run)
	default:
		return fmt.Errorf("daemon: unsupported platform %s", runtime.GOOS)
	}
}

func unregister(ctx context.Context, spec Spec, run Runner) error {
	switch runtime.GOOS {
	case "darwin":
		return unregisterLaunchd(ctx, spec, run)
	case "linux":
		return unregisterSystemd(ctx, spec, run)
	default:
		return fmt.Errorf("daemon: unsupported platform %s", runtime.GOOS)
	}
}

func labelOrDefault(spec Spec) string {
	if spec.Label != "" {
		return spec.Label
	}

	return DefaultLabel
}

func argsFor(spec Spec) ([]string, error) {
	if spec.Binary == "" || !filepath.IsAbs(spec.Binary) {
		return nil, fmt.Errorf("daemon: binary path must be absolute: %q", spec.Binary)
	}

	if spec.Home == "" || !filepath.IsAbs(spec.Home) {
		return nil, fmt.Errorf("daemon: home directory must be absolute: %q", spec.Home)
	}

	return append([]string{spec.Binary}, spec.Args...), nil
}

func unitName(label string) string {
	return strings.ReplaceAll(label, ".", "-") + ".service"
}
