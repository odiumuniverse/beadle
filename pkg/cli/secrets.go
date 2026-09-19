package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/agents-sync/pkg/secret"
)

func (a *app) newSecretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage the MCP credential values kept in the vault",
		Long: "Credential values are stored in mcp/secrets.json (0600) and never in the\n" +
			"synced canon, the object store, conflict records or the git history.\n" +
			"Values are never printed.",
	}

	cmd.AddCommand(
		a.newSecretsListCmd(),
		a.newSecretsSetCmd(),
		a.newSecretsRemoveCmd(),
		a.newSecretsPruneCmd(),
		a.newSecretsMigrateCmd(),
	)

	return cmd
}

func (a *app) newSecretsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List stored credential names",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			engine, err := a.engine()
			if err != nil {
				return err
			}

			names := engine.Secrets().Names()

			cmd.Printf("secrets: %d (backend: %s, mode: %s)\n", len(names), engine.Secrets().Backend(), engine.SecretsMode())

			for _, name := range names {
				cmd.Printf("  %s\n", name)
			}

			return nil
		},
	}
}

func (a *app) newSecretsSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <name> [value]",
		Short: "Store a credential value; without a value it is read from stdin",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !secret.ValidName(name) {
				return fmt.Errorf("invalid secret name %q: letters, digits and underscores, not starting with a digit", name)
			}

			value, err := secretValue(args)
			if err != nil {
				return err
			}

			engine, err := a.engine()
			if err != nil {
				return err
			}

			engine.Secrets().Set(name, value)

			if err := engine.Secrets().Save(); err != nil {
				return err
			}

			cmd.Printf("stored %s\n", name)

			return nil
		},
	}
}

func (a *app) newSecretsRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove"},
		Short:   "Delete a stored credential value",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			engine, err := a.engine()
			if err != nil {
				return err
			}

			if !engine.Secrets().Delete(args[0]) {
				return fmt.Errorf("no secret named %q", args[0])
			}

			if err := engine.Secrets().Save(); err != nil {
				return err
			}

			cmd.Printf("removed %s\n", args[0])

			return nil
		},
	}
}

func (a *app) newSecretsPruneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prune",
		Short: "Delete stored values the canon no longer references",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			engine, err := a.engine()
			if err != nil {
				return err
			}

			removed, err := engine.PruneSecrets(cmd.Context())
			if err != nil {
				return err
			}

			for _, name := range removed {
				cmd.Printf("removed %s\n", name)
			}

			cmd.Printf("pruned: %d\n", len(removed))

			return nil
		},
	}
}

func (a *app) newSecretsMigrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate <file|keyring>",
		Short: "Move stored values between the local file and the OS keyring",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			if target != secret.BackendFile && target != secret.BackendKeyring {
				return fmt.Errorf("unknown backend %q: use file or keyring", target)
			}

			engine, err := a.engine()
			if err != nil {
				return err
			}

			store := engine.Secrets()

			if store.Backend() == target {
				if err := store.Probe(); err != nil {
					return fmt.Errorf("keyring unavailable: %w", err)
				}

				cmd.Printf("backend is already %s\n", target)

				return nil
			}

			if err := store.SetBackend(target); err != nil {
				return err
			}

			if err := store.Probe(); err != nil {
				return fmt.Errorf("keyring unavailable: %w", err)
			}

			if err := store.Save(); err != nil {
				return err
			}

			cmd.Printf("secrets backend: %s (%d value(s))\n", target, store.Len())

			return nil
		},
	}
}

func secretValue(args []string) (string, error) {
	if len(args) == 2 {
		return args[1], nil
	}

	fmt.Fprintf(os.Stderr, "value for %s: ", args[0])

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read secret value: %w", err)
	}

	value := strings.TrimRight(line, "\r\n")
	if value == "" {
		return "", fmt.Errorf("empty value for %s", args[0])
	}

	return value, nil
}
