package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/state"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

// newMigrateCmd brings a vault's stored schemas up to the current version and
// reports what it changed.
//
// The migrations themselves already exist and run on every read: config.Load
// and state.Load each upgrade what they find in memory, and a normal command
// persists the result the next time it saves. What was missing was a way to ask
// for that on purpose — to run the upgrade now, see what it did, and get an
// answer a script can branch on.
//
// It is deliberately narrow: it migrates the two documents beadle owns the shape
// of, config.json and state.json. The layout migrations that move directories
// around — the agent-id renames, the plugin ledger rekey, the bundle farm move
// — belong to a sync, because they are changes to what the hosts see, not to
// how beadle stores its own records.
//
// A vault that is already current writes nothing at all: both Load calls report
// Migrated() false, so neither Save runs, and the vault is byte-identical
// afterwards. That is the property a re-run has to have, and it is why the
// command checks the flag instead of saving unconditionally.
func (a *app) newMigrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Upgrade the vault's stored schemas to the current version",
		Long: "Upgrade the vault's stored schemas to the current version and report what changed.\n\n" +
			"The upgrade already happens on every read; this command persists it on demand.\n" +
			"Running it on a current vault changes nothing.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runMigrate(cmd)
		},
	}
}

func (a *app) runMigrate(cmd *cobra.Command) error {
	v, err := a.resolveVault()
	if err != nil {
		return err
	}

	out := a.errOut
	if out == nil {
		out = cmd.ErrOrStderr()
	}

	changed, err := migrateConfig(v, out)
	if err != nil {
		return err
	}

	stateChanged, err := migrateState(v, out)
	if err != nil {
		return err
	}

	if !changed && !stateChanged {
		fmt.Fprintln(out, "the vault is current; nothing to migrate")

		return nil
	}

	fmt.Fprintln(out, "migrated")

	return nil
}

// migrateConfig persists the config migration when Load found one, and says so.
// The notes are the same ones a read-only command prints, which is what makes
// this command's output recognisable to anyone who has seen `beadle status`.
func migrateConfig(v *vault.Vault, out interface{ Write([]byte) (int, error) }) (bool, error) {
	cfg, err := config.Load(v.ConfigPath())
	if err != nil {
		return false, err
	}

	if !cfg.Migrated() {
		return false, nil
	}

	for _, note := range cfg.MigrationNotes() {
		fmt.Fprintln(out, "config: "+note)
	}

	if err := cfg.Save(v.ConfigPath()); err != nil {
		return false, err
	}

	return true, nil
}

// migrateState is migrateConfig for state.json.
func migrateState(v *vault.Vault, out interface{ Write([]byte) (int, error) }) (bool, error) {
	st, err := state.Load(v.StatePath())
	if err != nil {
		return false, err
	}

	if !st.Migrated() {
		return false, nil
	}

	for _, note := range st.MigrationNotes() {
		fmt.Fprintln(out, "state: "+note)
	}

	if err := st.Save(v.StatePath()); err != nil {
		return false, err
	}

	return true, nil
}
