package cli

import (
	"fmt"
	"os"

	"github.com/vmkteam/embedlog"

	"github.com/odiumuniverse/agents-sync/pkg/adapter"
	"github.com/odiumuniverse/agents-sync/pkg/config"
	syncer "github.com/odiumuniverse/agents-sync/pkg/sync"
	"github.com/odiumuniverse/agents-sync/pkg/vault"
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
	if !v.Initialized() {
		return nil, fmt.Errorf("vault %s is not initialized (run agent-sync init)", root)
	}

	return v, nil
}

func (a *app) engine(opts ...syncer.Option) (*syncer.Engine, error) {
	v, err := a.resolveVault()
	if err != nil {
		return nil, err
	}

	cfg, err := config.Load(v.ConfigPath())
	if err != nil {
		return nil, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	return syncer.New(v, cfg, adapter.All(home), a.logger, opts...)
}
