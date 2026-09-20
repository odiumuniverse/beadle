package cli

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/hooks"
)

func (a *app) newHooksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "Author lifecycle hooks for native bundles (beadle never runs them)",
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
				if cfg.HookApproved(name) {
					state = "approved"
				}

				fmt.Fprintf(out, "  %-20s %-14s %-10s %-6s %s\n", name, hook.Event, matcher, timeoutText(hook.Timeout), state)

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
	cmd.Flags().StringVar(&hook.Command, "command", "", "shell command rendered into bundles (required)")
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
	return &cobra.Command{
		Use:   "approve <name>",
		Short: "Approve a hook so it is rendered into bundles",
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

			cfg.ApproveHook(args[0])

			if err := cfg.Save(v.ConfigPath()); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "hook %s approved\n", args[0])

			return nil
		},
	}
}

func (a *app) newHooksRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <name>",
		Short: "Revoke a hook approval",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, cfg, err := a.loadConfig()
			if err != nil {
				return err
			}

			cfg.RevokeHook(args[0])

			if err := cfg.Save(v.ConfigPath()); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "hook %s revoked\n", args[0])

			return nil
		},
	}
}
