package vault

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/odiumuniverse/agents-sync/pkg/config"
)

const (
	DefaultDirName = ".agent-sync"
	EnvHome        = "AGENTSYNC_HOME"
)

var dirs = []string{
	"conflicts",
	"mcp",
	"mcp/override",
	"objects",
	"permissions",
	"permissions/override",
	"rules",
	"rules/override",
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

func (v *Vault) RegistryPath() string {
	return filepath.Join(v.root, "registry.json")
}

func (v *Vault) ObjectsDir() string {
	return filepath.Join(v.root, "objects")
}

func (v *Vault) ConflictsDir() string {
	return filepath.Join(v.root, "conflicts")
}

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

	if v.Initialized() {
		return nil
	}

	return config.Default().Save(v.ConfigPath())
}
