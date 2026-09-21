package vault

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/secret"
	"github.com/odiumuniverse/beadle/pkg/state"
)

const (
	DefaultDirName = ".beadle"
	EnvHome        = "BEADLE_HOME"
)

const gitIgnore = `# Beadle: credentials and machine-local state stay on this machine.
mcp/secrets.json
state/
state.json
objects/
conflicts/
plugins/
bundles/
# project rules stay out of git until the U-12 secret gate lands
projects/
`

const (
	legacyMemoryIgnoreLine    = "memory/"
	legacyMemoryIgnoreComment = "# memory notes stay out of git until the U-12 secret gate lands"
)

var dirs = []string{
	"bundles",
	"conflicts",
	"hooks",
	"mcp",
	"memory",
	"objects",
	"permissions",
	"plugins",
	"projects",
	"rules",
	"rulings",
	"skills",
	"state",
}

type Vault struct {
	root string
}

func ResolveRoot(flagPath, envHome string) (string, error) {
	if flagPath != "" {
		return filepath.Abs(flagPath)
	}

	if envHome != "" {
		return filepath.Abs(envHome)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}

	return filepath.Join(home, DefaultDirName), nil
}

func New(root string) *Vault {
	return &Vault{root: root}
}

func (v *Vault) Root() string {
	return v.root
}

func (v *Vault) ConfigPath() string {
	return filepath.Join(v.root, config.FileName)
}

func (v *Vault) StatePath() string {
	return filepath.Join(v.root, state.FileName)
}

func (v *Vault) LockPath() string {
	return filepath.Join(v.root, "state", "lock")
}

func (v *Vault) ObjectsDir() string {
	return filepath.Join(v.root, "objects")
}

func (v *Vault) ConflictsDir() string {
	return filepath.Join(v.root, "conflicts")
}

func (v *Vault) SecretsPath() string {
	return filepath.Join(v.root, "mcp", secret.FileName)
}

func (v *Vault) RulesPath() string {
	return filepath.Join(v.root, "rules", "base.md")
}

func (v *Vault) ServersPath() string {
	return filepath.Join(v.root, "mcp", "servers.json")
}

func (v *Vault) SkillsDir() string {
	return filepath.Join(v.root, "skills")
}

func (v *Vault) MemoryDir() string {
	return filepath.Join(v.root, "memory")
}

// ProjectsDir returns the vault directory holding the project rules canon.
func (v *Vault) ProjectsDir() string {
	return filepath.Join(v.root, "projects")
}

func (v *Vault) PermissionsPath() string {
	return filepath.Join(v.root, "permissions", "rules.json")
}

func (v *Vault) PluginsDir() string {
	return filepath.Join(v.root, "plugins")
}

func (v *Vault) PluginsLedgerPath() string {
	return filepath.Join(v.PluginsDir(), "ledger.json")
}

func (v *Vault) HooksPath() string {
	return filepath.Join(v.root, "hooks", "hooks.json")
}

func (v *Vault) BundlesDir() string {
	return filepath.Join(v.root, "bundles")
}

func (v *Vault) RulingsDir() string {
	return filepath.Join(v.root, "rulings")
}

// AdoptionsDir returns the machine-local directory holding the foreign skill
// copies beadle adopted. It lives inside the gitignored state/ tree, so the
// original bytes never enter the vault history.
func (v *Vault) AdoptionsDir() string {
	return filepath.Join(v.root, "state", "adoptions")
}

// MergetoolPath returns the machine-local file describing the active
// mergetool merge, if any.
func (v *Vault) MergetoolPath() string {
	return filepath.Join(v.root, "state", mergetoolFileName)
}

const mergetoolFileName = "mergetool.json"

func (v *Vault) RulingsPath() string {
	return filepath.Join(v.RulingsDir(), rulesFilename)
}

const rulesFilename = "rulings.json"

func (v *Vault) Initialized() bool {
	_, err := os.Stat(v.ConfigPath())

	return err == nil
}

func (v *Vault) Init() error {
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(v.root, dir), 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}

	if err := os.Chmod(v.root, 0o700); err != nil { //nolint:gosec // G302: a directory needs the execute bit; 0700 is owner-only
		return fmt.Errorf("restrict vault permissions: %w", err)
	}

	if err := v.EnsureGitIgnore(); err != nil {
		return err
	}

	if v.Initialized() {
		return nil
	}

	return config.Default().Save(v.ConfigPath())
}

func (v *Vault) EnsureGitIgnore() error {
	path := filepath.Join(v.root, ".gitignore")

	data, err := os.ReadFile(path) //nolint:gosec // G304: path is inside the vault root
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return writeGitIgnore(path, []byte(gitIgnore))
	case err != nil:
		return fmt.Errorf("read .gitignore: %w", err)
	}

	missing := missingGitIgnoreLines(data)
	if len(missing) == 0 {
		return nil
	}

	updated := slices.Concat(data, newlineIfMissing(data), []byte(strings.Join(missing, "\n")+"\n"))

	return writeGitIgnore(path, updated)
}

func writeGitIgnore(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil { //nolint:gosec // G703: path is inside the vault root
		return fmt.Errorf("write .gitignore: %w", err)
	}

	return nil
}

// MemoryIgnored reports whether the legacy memory/ gitignore line is present.
func (v *Vault) MemoryIgnored() (bool, error) {
	data, err := os.ReadFile(filepath.Join(v.root, ".gitignore"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("read .gitignore: %w", err)
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.TrimSpace(line) == legacyMemoryIgnoreLine {
			return true, nil
		}
	}

	return false, nil
}

// RemoveLegacyIgnore drops the obsolete memory gitignore lines from an
// existing vault. It is idempotent and writes the file atomically.
func (v *Vault) RemoveLegacyIgnore() error {
	path := filepath.Join(v.root, ".gitignore")

	data, err := os.ReadFile(path) //nolint:gosec // G304: path is inside the vault root
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("read .gitignore: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	kept := make([]string, 0, len(lines))
	removed := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == legacyMemoryIgnoreLine || trimmed == legacyMemoryIgnoreComment {
			removed = true

			continue
		}

		kept = append(kept, line)
	}

	if !removed {
		return nil
	}

	return writeGitIgnore(path, []byte(strings.Join(kept, "\n")))
}

func missingGitIgnoreLines(data []byte) []string {
	present := map[string]struct{}{}

	for line := range strings.SplitSeq(string(data), "\n") {
		present[strings.TrimSpace(line)] = struct{}{}
	}

	var missing []string

	for line := range strings.SplitSeq(gitIgnore, "\n") {
		entry := strings.TrimSpace(line)
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}

		if _, ok := present[entry]; ok {
			continue
		}

		missing = append(missing, entry)
	}

	return missing
}

func newlineIfMissing(data []byte) []byte {
	if len(data) > 0 && data[len(data)-1] != '\n' {
		return []byte("\n")
	}

	return nil
}
