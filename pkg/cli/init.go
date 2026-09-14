package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/agents-sync/pkg/adapter"
	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/vault"
)

func (a *app) newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create the vault and register detected agents",
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := vault.ResolveRoot(a.vaultPath, os.Getenv(vault.EnvHome))
			if err != nil {
				return err
			}

			v := vault.New(root)
			if err := v.Init(); err != nil {
				return err
			}

			cfg, err := config.Load(v.ConfigPath())
			if err != nil {
				return err
			}

			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}

			cmd.Printf("vault: %s\n", v.Root())

			for _, adapterItem := range adapter.All(home) {
				detected, err := adapterItem.Detect()
				if err != nil {
					return err
				}

				if !detected {
					continue
				}

				cfg.Enable(adapterItem.ID())
				cmd.Printf("detected: %s (%s)\n", adapterItem.DisplayName(), adapterItem.ID())
			}

			if err := cfg.Save(v.ConfigPath()); err != nil {
				return err
			}

			cmd.Println("done")

			return nil
		},
	}
}
