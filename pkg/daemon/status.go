package daemon

import (
	"errors"
	"fmt"
	"html"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/secret"
)

// Status describes the background watcher service of one home directory.
type Status struct {
	Path string
	// Label is the service label the status was found under: the brew labels
	// mean the unit belongs to Homebrew, not to beadle.
	Label     string
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

		return Status{Path: path, Label: candidate, Installed: true, Loaded: loaded(candidate, run)}, nil
	}

	return Status{Path: UnitPath(home, name), Label: name}, nil
}

// UnitEnvFromFile reads the environment a service unit pins. A unit without an
// EnvironmentVariables block — an install from before env pinning — yields an
// empty map. Parsing is best-effort: the unit is machine-written by Install.
func UnitEnvFromFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the unit path is resolved from the home
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	if runtime.GOOS == osDarwin {
		return launchdEnv(string(data)), nil
	}

	return systemdEnvFromUnit(string(data)), nil
}

var (
	launchdEnvBlock = regexp.MustCompile(`(?s)<key>EnvironmentVariables</key>\s*<dict>(.*?)</dict>`)
	launchdEnvPair  = regexp.MustCompile(`(?s)<key>(.*?)</key>\s*<string>(.*?)</string>`)
)

// launchdEnv extracts the EnvironmentVariables pairs from a plist.
func launchdEnv(content string) map[string]string {
	block := launchdEnvBlock.FindStringSubmatch(content)
	if block == nil {
		return nil
	}

	env := map[string]string{}

	for _, pair := range launchdEnvPair.FindAllStringSubmatch(block[1], -1) {
		env[html.UnescapeString(pair[1])] = html.UnescapeString(pair[2])
	}

	return env
}

// systemdEnvFromUnit extracts the Environment= assignments of a unit file.
func systemdEnvFromUnit(content string) map[string]string {
	env := map[string]string{}

	for line := range strings.SplitSeq(content, "\n") {
		value, ok := strings.CutPrefix(strings.TrimSpace(line), "Environment=")
		if !ok {
			continue
		}

		value = strings.Trim(value, `"`)

		key, val, ok := strings.Cut(value, "=")
		if !ok {
			continue
		}

		env[key] = strings.NewReplacer(`\\`, `\`, `\"`, `"`).Replace(val)
	}

	return env
}

func brewCandidates() []string {
	if runtime.GOOS == osDarwin {
		return []string{brewLabel, brewShLabel}
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
