package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/vault"
)

const (
	DefaultLabel = "com.beadle.watch"
	brewLabel    = "homebrew.mxcl.beadle"
	brewShLabel  = "sh.brew.beadle"
	osDarwin     = "darwin"
)

// IdentityEnvKeys are the environment variables the watcher resolves its home
// and vault from: a unit must pin them, and doctor compares exactly these.
// PATH is pinned too (the watcher needs git and friends), but it is a snapshot
// of the installing shell rather than an identity, so doctor ignores it.
var IdentityEnvKeys = []string{"HOME", "BEADLE_HOME", "XDG_CONFIG_HOME", "DSH_HOME"}

type Spec struct {
	Label      string
	Binary     string
	Args       []string
	Home       string
	LogPath    string
	ErrLogPath string
	FileLimit  int
	// Env is the explicit environment the unit carries: the platform manager
	// starts a unit with its own environment, so without it the watcher would
	// resolve a different home or vault than the CLI.
	Env map[string]string
}

// UnitEnv builds the environment a service unit must pin from the invoking
// environment: HOME and the vault are always explicit (BEADLE_HOME only when
// it differs from the default), XDG_CONFIG_HOME/DSH_HOME/PATH travel when set.
func UnitEnv(home, vaultRoot string) map[string]string {
	env := map[string]string{"HOME": home}

	if value := os.Getenv("XDG_CONFIG_HOME"); value != "" {
		env["XDG_CONFIG_HOME"] = value
	}

	if value := os.Getenv("DSH_HOME"); value != "" {
		env["DSH_HOME"] = value
	}

	if vaultRoot != "" && vaultRoot != filepath.Join(home, vault.DefaultDirName) {
		env["BEADLE_HOME"] = vaultRoot
	}

	if value := os.Getenv("PATH"); value != "" {
		env["PATH"] = value
	}

	return env
}

// TemporaryHome reports whether a path lives under a temporary root ($TMPDIR,
// /tmp, /private/var/folders). A unit installed for such a home would outlive
// the temporary tree, so installers skip it instead of pinning a vanishing
// environment.
func TemporaryHome(path string) bool {
	if path == "" {
		return false
	}

	resolved := resolvePath(path)

	for _, root := range temporaryRoots() {
		if resolved == root || strings.HasPrefix(resolved, root+string(filepath.Separator)) {
			return true
		}
	}

	return false
}

func temporaryRoots() []string {
	var out []string

	for _, root := range []string{os.Getenv("TMPDIR"), "/tmp", "/private/tmp", "/private/var/folders", "/var/folders"} {
		if root == "" {
			continue
		}

		resolved := resolvePath(root)
		if !slices.Contains(out, resolved) {
			out = append(out, resolved)
		}
	}

	return out
}

// resolvePath resolves the symlinks of the longest existing ancestor and
// appends the rest, so a not-yet-created home under a symlinked temporary root
// (/var → /private/var, /tmp → /private/tmp) still matches.
func resolvePath(path string) string {
	cleaned := filepath.Clean(path)
	rest := ""

	for current := cleaned; ; {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(resolved, rest)
		}

		parent := filepath.Dir(current)
		if parent == current {
			return cleaned
		}

		rest = filepath.Join(filepath.Base(current), rest)
		current = parent
	}
}

// EnvPairs renders the pinned environment in a deterministic order.
func EnvPairs(env map[string]string) [][2]string {
	out := make([][2]string, 0, len(env))

	for _, key := range slices.Sorted(maps.Keys(env)) {
		out = append(out, [2]string{key, env[key]})
	}

	return out
}

type Runner func(ctx context.Context, name string, args ...string) error

func Render(spec Spec) (string, string, error) {
	switch runtime.GOOS {
	case osDarwin:
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
	case osDarwin:
		return registerLaunchd(ctx, spec, run)
	case "linux":
		return registerSystemd(ctx, spec, run)
	default:
		return fmt.Errorf("daemon: unsupported platform %s", runtime.GOOS)
	}
}

func unregister(ctx context.Context, spec Spec, run Runner) error {
	switch runtime.GOOS {
	case osDarwin:
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
