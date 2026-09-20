package cli

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/plugin"
)

func (a *app) newPluginsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugins",
		Short: "Pin plugin versions for one agent at a time",
		Args:  cobra.NoArgs,
	}

	cmd.AddCommand(a.newPluginPinsCmd(), a.newPluginPinCmd(), a.newPluginUnpinCmd())

	return cmd
}

func (a *app) newPluginPinsCmd() *cobra.Command {
	var agentID string

	cmd := &cobra.Command{
		Use:   "pins",
		Short: "List plugin pins and whether their versions are cached",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, cfg, err := a.loadConfig()
			if err != nil {
				return err
			}

			home, _, err := homeAndCwd()
			if err != nil {
				return err
			}

			if agentID != "" {
				if err := validatePinAgent(agentID); err != nil {
					return err
				}
			}

			manifest, err := plugin.Read(home)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			printed := 0

			for _, id := range slices.Sorted(maps.Keys(cfg.Agents)) {
				if agentID != "" && id != agentID {
					continue
				}

				pins := cfg.Agents[id].PluginPins

				for _, key := range slices.Sorted(maps.Keys(pins)) {
					fmt.Fprintf(out, "  %-13s %-24s %-12s %s\n", id, key, pins[key], pluginPinState(home, manifest, key, pins[key]))

					printed++
				}
			}

			if printed == 0 {
				fmt.Fprintln(out, "no plugin pins")
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&agentID, "agent", "", "only this agent")

	return cmd
}

func pluginPinState(home string, manifest plugin.Manifest, key, version string) string {
	marketplace, name, ok := cutPluginKey(key)
	if !ok {
		return "unknown-plugin"
	}

	if !pluginInstalled(manifest, key) {
		return "unknown-plugin"
	}

	if !isDir(filepath.Join(home, ".claude", "plugins", "cache", marketplace, name, version)) {
		return "missing"
	}

	return "ok"
}

func pluginInstalled(manifest plugin.Manifest, key string) bool {
	for _, p := range manifest.Plugins {
		if p.Marketplace+"/"+p.Name == key {
			return true
		}
	}

	return false
}

func cutPluginKey(key string) (string, string, bool) {
	marketplace, name, ok := strings.Cut(key, "/")
	if !ok || marketplace == "" || name == "" {
		return "", "", false
	}

	return marketplace, name, true
}

func isDir(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.IsDir()
}

func (a *app) newPluginPinCmd() *cobra.Command {
	var agentID string

	cmd := &cobra.Command{
		Use:   "pin <marketplace>/<name> <version>",
		Short: "Pin one plugin to a version for one agent",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, cfg, err := a.loadConfig()
			if err != nil {
				return err
			}

			if err := validatePinAgent(agentID); err != nil {
				return err
			}

			if err := cfg.SetPluginPin(agentID, args[0], args[1]); err != nil {
				return err
			}

			if err := cfg.Save(v.ConfigPath()); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s pinned to %s\n", agentID, args[0], args[1])

			return nil
		},
	}

	cmd.Flags().StringVar(&agentID, "agent", "", "agent to pin for (required)")

	return cmd
}

func (a *app) newPluginUnpinCmd() *cobra.Command {
	var agentID string

	cmd := &cobra.Command{
		Use:   "unpin <marketplace>/<name>",
		Short: "Drop a plugin pin for one agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, cfg, err := a.loadConfig()
			if err != nil {
				return err
			}

			if err := validatePinAgent(agentID); err != nil {
				return err
			}

			cfg.UnsetPluginPin(agentID, args[0])

			if err := cfg.Save(v.ConfigPath()); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s unpinned\n", agentID, args[0])

			return nil
		},
	}

	cmd.Flags().StringVar(&agentID, "agent", "", "agent to unpin for (required)")

	return cmd
}

func validatePinAgent(agentID string) error {
	if agentID == "" {
		return errors.New("--agent is required")
	}

	agents, err := allAgents()
	if err != nil {
		return err
	}

	if agent.ByID(agents, agentID) == nil {
		return fmt.Errorf("unknown agent %q", agentID)
	}

	return nil
}
