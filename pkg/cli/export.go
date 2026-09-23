package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/agentplugins"
	"github.com/odiumuniverse/beadle/pkg/mcp"
	"github.com/odiumuniverse/beadle/pkg/skill"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

func (a *app) newExportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export the canon in external package formats",
	}

	cmd.AddCommand(a.newExportAgentPluginsCmd())

	return cmd
}

func (a *app) newExportAgentPluginsCmd() *cobra.Command {
	var out string

	cmd := &cobra.Command{
		Use:   "agent-plugins",
		Short: "Render the canon (skills and MCP) as an Agent Plugins v1.0.0 package",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if out == "" {
				return errors.New("--out is required")
			}

			v, err := a.resolveVaultReadOnly()
			if err != nil {
				return err
			}

			skills, err := skill.ReadDir(v.SkillsDir())
			if err != nil {
				return err
			}

			servers, err := canonServers(v)
			if err != nil {
				return err
			}

			version := cmd.Root().Version
			if version == "" {
				version = "0.0.0"
			}

			pkg, err := agentplugins.Render(skills, servers, agentplugins.Options{
				Name:        "beadle-canon",
				Version:     version,
				Description: "The beadle canon: portable skills and MCP servers",
				License:     "MIT",
				Keywords:    []string{"beadle", "skills", "mcp"},
			})
			if err != nil {
				return err
			}

			if err := agentplugins.Write(out, pkg.Files); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "exported agent plugins package to %s\n", out)
			fmt.Fprintf(cmd.OutOrStdout(), "skills: %d rendered, %d skipped; mcp: %d rendered, %d skipped\n",
				pkg.Skills.Rendered, pkg.Skills.Skipped, pkg.MCP.Rendered, pkg.MCP.Skipped)

			for _, warning := range pkg.Warnings {
				fmt.Fprintln(cmd.ErrOrStderr(), "  ! "+warning)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&out, "out", "", "output directory (required)")

	return cmd
}

// resolveVaultReadOnly resolves the vault without touching it: an export must
// not create or migrate the config, so a missing vault is an error with the
// init hint, not an implicit initialization.
func (a *app) resolveVaultReadOnly() (*vault.Vault, error) {
	root, err := vault.ResolveRoot(a.vaultPath, os.Getenv(vault.EnvHome))
	if err != nil {
		return nil, err
	}

	v := vault.New(root)

	if _, err := os.Stat(v.ConfigPath()); err != nil { //nolint:gosec // G703: the path is the resolved vault path, not request taint
		return nil, fmt.Errorf("vault %s is not initialized (run beadle init)", root)
	}

	return v, nil
}

// canonServers reads the canonical MCP servers of the vault.
func canonServers(v *vault.Vault) (mcp.Servers, error) {
	data, err := os.ReadFile(v.ServersPath()) //nolint:gosec // G304: the path is the vault canon
	if errors.Is(err, fs.ErrNotExist) {
		return mcp.Servers{}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read mcp servers: %w", err)
	}

	return mcp.ParseCanonical(data)
}
