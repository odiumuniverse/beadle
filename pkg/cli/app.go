package cli

import (
	"fmt"
	"os"

	"github.com/vmkteam/embedlog"

	"github.com/odiumuniverse/agents-sync/pkg/agent"
	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/engine"
	"github.com/odiumuniverse/agents-sync/pkg/kind"
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
