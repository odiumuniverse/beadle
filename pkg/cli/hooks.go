package cli

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/hooks"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

func (a *app) newHooksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "Author lifecycle hooks for host hooks files and native bundles (beadle never runs them)",
		Args:  cobra.NoArgs,
	}

	cmd.AddCommand(a.newHooksListCmd(), a.newHooksAddCmd(), a.newHooksRemoveCmd(), a.newHooksApproveCmd(), a.newHooksRevokeCmd())

	return cmd
}

func (a *app) newHooksListCmd() *cobra.Command {
	var commands bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List canonical hooks and their approval state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			v, cfg, err := a.loadConfig()
			if err != nil {
				return err
			}

			canon, err := hooks.Load(v.HooksPath())
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			if len(canon) == 0 {
				fmt.Fprintln(out, "no hooks")

				return nil
			}

			for _, name := range slices.Sorted(maps.Keys(canon)) {
				hook := canon[name]

				matcher := hook.Matcher
				if matcher == "" {
					matcher = "*"
				}

				state := "pending"
				if hookApproved(a, cfg, canon, name) {
					state = "approved"
				}

				line := fmt.Sprintf("  %-20s %-14s %-10s %-6s %s", name, hook.Event, matcher, timeoutText(hook.Timeout), state)
				if hook.Source != "" {
					line += "  " + hook.Source
				}

				fmt.Fprintln(out, line)

				if commands {
					fmt.Fprintf(out, "      %s\n", hook.Command)
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&commands, "commands", false, "print hook commands (beadle never runs them)")

	return cmd
}

func timeoutText(timeout int) string {
	if timeout == 0 {
		return "-"
	}

	return fmt.Sprintf("%ds", timeout)
}

func (a *app) newHooksAddCmd() *cobra.Command {
	var hook hooks.Hook

	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add or update a canonical hook",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			v, _, err := a.loadConfig()
			if err != nil {
				return err
			}

			if err := hooks.Validate(name, hook); err != nil {
				return err
			}

			canon, err := hooks.Load(v.HooksPath())
			if err != nil {
				return err
			}

			canon[name] = hook

			if err := hooks.Save(v.HooksPath(), canon); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "hook %s saved; approve it with: beadle hooks approve %s\n", name, name)

			return nil
		},
	}

	cmd.Flags().StringVar(&hook.Event, "event", "", "canonical event: "+strings.Join(hooks.Events(), ", "))
	cmd.Flags().StringVar(&hook.Matcher, "matcher", "", "tool matcher (default *)")
	cmd.Flags().StringVar(&hook.Command, "command", "", "shell command rendered into host hooks files and bundles (required)")
	cmd.Flags().IntVar(&hook.Timeout, "timeout", 0, "timeout in seconds (0 = host default)")

	return cmd
}

func (a *app) newHooksRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove a canonical hook",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, cfg, err := a.loadConfig()
			if err != nil {
				return err
			}

			canon, err := hooks.Load(v.HooksPath())
			if err != nil {
				return err
			}

			if _, ok := canon[args[0]]; !ok {
				return fmt.Errorf("unknown hook %q", args[0])
			}

			delete(canon, args[0])

			cfg.RevokeHook(args[0])

			if err := cfg.Save(v.ConfigPath()); err != nil {
				return err
			}

			if err := hooks.Save(v.HooksPath(), canon); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "hook %s removed\n", args[0])

			return nil
		},
	}
}

func (a *app) newHooksApproveCmd() *cobra.Command {
	var pluginKey string

	cmd := &cobra.Command{
		Use:   "approve <name>",
		Short: "Approve a hook (or every hook of a plugin) so it is rendered into host hooks files and native bundles",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if pluginKey != "" {
				if len(args) != 0 {
					return errors.New("choose either a hook name or --plugin")
				}

				return a.approvePluginHooks(cmd, pluginKey)
			}

			if len(args) != 1 {
				return errors.New("a hook name is required (or use --plugin <key>)")
			}

			v, cfg, err := a.loadConfig()
			if err != nil {
				return err
			}

			canon, err := hooks.Load(v.HooksPath())
			if err != nil {
				return err
			}

			if _, ok := canon[args[0]]; !ok {
				return fmt.Errorf("unknown hook %q", args[0])
			}

			key, fromPlugin := canon[args[0]].PluginKey()
			if fromPlugin {
				// A plugin hook is approved as part of its package: the library
				// holds one consent per package, keyed by the hash of the hooks
				// the user is shown now.
				e, err := a.engine()
				if err != nil {
					return err
				}

				if err := e.ApprovePluginHookSet(key); err != nil {
					return err
				}

				fmt.Fprintf(cmd.OutOrStdout(), "hook %s approved: plugin %s hooks approved as a set\n", args[0], key)

				return nil
			}

			cfg.ApproveHook(args[0])

			if err := cfg.Save(v.ConfigPath()); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "hook %s approved\n", args[0])

			return nil
		},
	}

	cmd.Flags().StringVar(&pluginKey, "plugin", "", "approve every expressible command hook of an installed plugin (<marketplace>/<name>)")

	return cmd
}

// approvePluginHooks copies the command hooks of one installed plugin into the
// canon and approves them; the scan itself never writes.
func (a *app) approvePluginHooks(cmd *cobra.Command, key string) error {
	v, cfg, err := a.loadConfig()
	if err != nil {
		return err
	}

	agents, err := allAgents()
	if err != nil {
		return err
	}

	e, err := a.engineWith(v, cfg, agents)
	if err != nil {
		return err
	}

	report, err := e.ApprovePluginHooks(key)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()

	for _, note := range report.Notes {
		fmt.Fprintln(out, "note: "+note)
	}

	for _, warning := range report.Warnings {
		fmt.Fprintln(out, "warning: "+warning)
	}

	return nil
}

func (a *app) newHooksRevokeCmd() *cobra.Command {
	var pluginKey string

	cmd := &cobra.Command{
		Use:   "revoke <name>",
		Short: "Revoke a hook approval (or every hook of a plugin)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, cfg, err := a.loadConfig()
			if err != nil {
				return err
			}

			if pluginKey != "" {
				if len(args) != 0 {
					return errors.New("choose either a hook name or --plugin")
				}

				return a.revokePluginHooks(cmd, v, cfg, pluginKey)
			}

			if len(args) != 1 {
				return errors.New("a hook name is required (or use --plugin <key>)")
			}

			canon, err := hooks.Load(v.HooksPath())
			if err != nil {
				return err
			}

			if _, ok := canon[args[0]]; !ok {
				return fmt.Errorf("unknown hook %q", args[0])
			}

			key, fromPlugin := canon[args[0]].PluginKey()
			if fromPlugin {
				e, err := a.engine()
				if err != nil {
					return err
				}

				if err := e.RevokePluginHookConsent(key); err != nil {
					return err
				}

				fmt.Fprintf(cmd.OutOrStdout(), "hook %s revoked: plugin %s hook consent taken back\n", args[0], key)

				return nil
			}

			cfg.RevokeHook(args[0])

			if err := cfg.Save(v.ConfigPath()); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "hook %s revoked\n", args[0])

			return nil
		},
	}

	cmd.Flags().StringVar(&pluginKey, "plugin", "", "revoke every hook a plugin contributed, dropping its entries in your configuration and its approvals (<marketplace>/<name>)")

	return cmd
}

// revokePluginHooks drops the approvals and the canon entries of one plugin's
// hooks: the plugin is gone, so they can never render again, and the doctor
// stops flagging them as missing. Only an explicit revoke removes them.
func (a *app) revokePluginHooks(cmd *cobra.Command, v *vault.Vault, cfg *config.Config, key string) error {
	canon, err := hooks.Load(v.HooksPath())
	if err != nil {
		return err
	}

	revoked := dropPluginHooks(canon, key)

	// The consent lives in the library, so it is taken back there, not here.
	e, err := a.engine()
	if err != nil {
		return err
	}

	if err := e.RevokePluginHookConsent(key); err != nil {
		return err
	}

	modules := dropPluginModules(cfg, key)

	if revoked == 0 && modules == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "no hooks of plugin %s\n", key)

		return nil
	}

	if err := hooks.Save(v.HooksPath(), canon); err != nil {
		return err
	}

	if err := cfg.Save(v.ConfigPath()); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "revoked %d hook(s) and %d hook module approval(s) of plugin %s\n", revoked, modules, key)

	return nil
}

// dropPluginHooks removes every canon entry this plugin contributed and returns
// how many went. Sorted names keep the count, and the canon's own shape on
// disk, identical between runs.
func dropPluginHooks(canon map[string]hooks.Hook, key string) int {
	dropped := 0

	for _, name := range slices.Sorted(maps.Keys(canon)) {
		hookKey, fromPlugin := canon[name].PluginKey()
		if !fromPlugin || hookKey != key {
			continue
		}

		delete(canon, name)

		dropped++
	}

	return dropped
}

// dropPluginModules revokes this plugin's hook-module approvals and returns how
// many were revoked. The list is cloned because the loop removes from the field
// it ranges over.
func dropPluginModules(cfg *config.Config, key string) int {
	revoked := 0

	for _, entry := range slices.Clone(cfg.ApprovedHooks) {
		if moduleKey, ok := hooks.HookModulePlugin(entry); ok && moduleKey == key {
			cfg.RevokeHook(entry)

			revoked++
		}
	}

	return revoked
}

// hookApproved reports one canon hook's approval state. A plugin hook is
// answered by the library, which owns that consent; everything else is beadle's
// own list of names.
func hookApproved(a *app, cfg *config.Config, canon map[string]hooks.Hook, name string) bool {
	key, fromPlugin := canon[name].PluginKey()
	if !fromPlugin {
		return cfg.HookApproved(name)
	}

	e, err := a.engine()
	if err != nil {
		return false
	}

	return e.PluginHookApprovedFor(canon, key)
}
