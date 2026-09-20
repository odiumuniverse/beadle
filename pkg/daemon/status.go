package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/secret"
)

// Status describes the background watcher service of one home directory.
type Status struct {
	Path      string
	Installed bool
	Loaded    bool
}

// UnitPath returns the service file path Render would write for this home.
func UnitPath(home, label string) string {
	if runtime.GOOS == osDarwin {
		return filepath.Join(home, launchdSubdir, labelOrDefault(Spec{Label: label})+".plist")
	}

	return filepath.Join(home, systemdSubdir, unitName(labelOrDefault(Spec{Label: label})))
}

// Check inspects the service without changing anything: whether the unit file
// exists and whether the platform manager already runs it. On macOS a
// Homebrew-managed watcher (brew services) counts as installed as well.
func Check(home, label string, run secret.Runner) (Status, error) {
	name := labelOrDefault(Spec{Label: label})

	for _, candidate := range append([]string{name}, brewCandidates()...) {
		path := UnitPath(home, candidate)

		found, err := unitExists(path)
		if err != nil {
			return Status{Path: path}, err
		}

		if !found {
			continue
		}

		return Status{Path: path, Installed: true, Loaded: loaded(candidate, run)}, nil
	}

	return Status{Path: UnitPath(home, name)}, nil
}

func brewCandidates() []string {
	if runtime.GOOS == osDarwin {
		return []string{brewLabel}
	}

	return nil
}

func unitExists(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}

		return false, fmt.Errorf("inspect %s: %w", path, err)
	}

	return true, nil
}

func loaded(label string, run secret.Runner) bool {
	if runtime.GOOS == osDarwin {
		_, code, err := run.Run("launchctl", []string{"print", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)}, nil)

		return err == nil && code == 0
	}

	out, code, err := run.Run("systemctl", []string{"--user", "is-active", unitName(label)}, nil)
	if err != nil || code != 0 {
		return false
	}

	return strings.TrimSpace(string(out)) == "active"
}
