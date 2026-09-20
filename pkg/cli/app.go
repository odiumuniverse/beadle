package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vmkteam/embedlog"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

type app struct {
	logger    embedlog.Logger
	vaultPath string
}

func (a *app) resolveVault() (*vault.Vault, error) {
	root, err := vault.ResolveRoot(a.vaultPath, os.Getenv(vault.EnvHome))
	if err != nil {
		return nil, err
	}

	v := vault.New(root)
	if !vaultDirExists(root) {
		return nil, fmt.Errorf("vault %s is not initialized (run beadle init)", root)
	}

	if err := a.ensureConfig(v); err != nil {
		return nil, err
	}

	return v, nil
}

func vaultDirExists(root string) bool {
	info, err := os.Stat(root) //nolint:gosec // G703: root is the resolved vault path, not request taint
	if err != nil || !info.IsDir() {
		return false
	}

	for _, marker := range []string{".gitignore", "state", "objects"} {
		if _, err := os.Stat(filepath.Join(root, marker)); err == nil { //nolint:gosec // G703: root is the resolved vault path, marker names are constants
			return true
		}
	}

	return false
}

func (a *app) ensureConfig(v *vault.Vault) error {
	if v.Initialized() {
		return nil
	}

	if err := config.Default().Save(v.ConfigPath()); err != nil {
		return err
	}

	a.logger.Print(context.Background(), "config.json was missing; recreated with defaults", "vault", v.Root())

	return nil
}

func (a *app) loadConfig() (*vault.Vault, *config.Config, error) {
	v, err := a.resolveVault()
	if err != nil {
		return nil, nil, err
	}

	cfg, err := config.Load(v.ConfigPath())
	if err != nil {
		return nil, nil, err
	}

	return v, cfg, nil
}

func (a *app) engine() (*engine.Engine, error) {
	v, cfg, err := a.loadConfig()
	if err != nil {
		return nil, err
	}

	home, cwd, err := homeAndCwd()
	if err != nil {
		return nil, err
	}

	return engine.New(v, cfg, agent.All(home, cwd), engine.WithLogger(a.logger), engine.WithHome(home))
}

func homeAndCwd() (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", "", err
	}

	return home, cwd, nil
}

func allAgents() ([]*agent.Agent, error) {
	home, cwd, err := homeAndCwd()
	if err != nil {
		return nil, err
	}

	return agent.All(home, cwd), nil
}

func parseKinds(names []string) ([]kind.ID, error) {
	ids := make([]kind.ID, 0, len(names))

	for _, name := range names {
		id, err := kind.Parse(name)
		if err != nil {
			return nil, err
		}

		ids = append(ids, id)
	}

	return ids, nil
}
