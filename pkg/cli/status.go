package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/agents-sync/pkg/adapter"
	"github.com/odiumuniverse/agents-sync/pkg/cas"
	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/registry"
	syncer "github.com/odiumuniverse/agents-sync/pkg/sync"
	"github.com/odiumuniverse/agents-sync/pkg/vault"
)

func (a *app) newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show vault, agents and resource status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := vault.ResolveRoot(a.vaultPath, os.Getenv(vault.EnvHome))
			if err != nil {
				return err
			}

			v := vault.New(root)

			cmd.Printf("vault: %s\n", v.Root())

			if !v.Initialized() {
				cmd.Println("state: not initialized (run agent-sync init)")

				return nil
			}

			cfg, err := config.Load(v.ConfigPath())
			if err != nil {
				return err
			}

			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}

			cmd.Println("agents:")

			for _, adapterItem := range adapter.All(home) {
				detected, err := adapterItem.Detect()
				if err != nil {
					return err
				}

				enabled := cfg.Agents[adapterItem.ID()].Enabled

				cmd.Printf("  %-12s enabled=%-5t detected=%t\n", adapterItem.ID(), enabled, detected)
			}

			reg, err := registry.Load(v.RegistryPath())
			if err != nil {
				return err
			}

			cmd.Println("resources:")

			for _, resource := range []string{syncer.ResourceRules, syncer.ResourceMCP, syncer.ResourceSkills, syncer.ResourcePermissions} {
				entry := reg.Resources[resource]
				if entry == nil {
					cmd.Printf("  %-11s not synced\n", resource)

					continue
				}

				status := ""
				if entry.Conflict {
					status = "  CONFLICT"
				}

				cmd.Printf("  %-11s vault=%s agents=%d%s\n", resource, shortHash(entry.Vault), len(entry.Agents), status)
			}

			if reg.Conflicts() > 0 {
				cmd.Println("resolve with: agent-sync resolve <rules|mcp|skills|permissions>")
			}

			return nil
		},
	}
}

func shortHash(h cas.Hash) string {
	if h == "" {
		return "-"
	}

	name := string(h)
	if len(name) > 8 {
		return name[:8]
	}

	return name
}
