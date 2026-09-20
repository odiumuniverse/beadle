package cli

import (
	"errors"
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/state"
)

func (a *app) newBundlesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bundles",
		Short: "Generate and register native host bundles (Claude marketplace, Gemini extension, Antigravity plugin)",
		Args:  cobra.NoArgs,
	}

	cmd.AddCommand(a.newBundlesStatusCmd(), a.newBundlesToggleCmd(true), a.newBundlesToggleCmd(false))

	return cmd
}

func (a *app) newBundlesStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show bundle registration per host",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			v, _, err := a.loadConfig()
			if err != nil {
				return err
			}

			st, err := state.Load(v.StatePath())
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			fmt.Fprintf(out, "  %-13s %-8s %-11s %-16s %s\n", "host", "enabled", "registered", "version", "cli")

			for _, host := range bundle.Hosts() {
				entry := st.Bundles[string(host)]

				version := entry.Version
				if version == "" {
					version = "-"
				}

				cli := "missing"
				if _, err := exec.LookPath(host.Binary()); err == nil {
					cli = "found"
				}

				fmt.Fprintf(out, "  %-13s %-8s %-11s %-16s %s\n", host, onOff(entry.Enabled, "yes", "no"), onOff(entry.Registered, "yes", "no"), version, cli)
			}

			return nil
		},
	}
}

func (a *app) newBundlesToggleCmd(enable bool) *cobra.Command {
	use, short := "disable", "Unregister the bundle and restore file sync"
	if enable {
		use, short = "enable", "Generate the bundle and register it with the host CLI when available"
	}

	var host string

	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if host == "" {
				return errors.New("--host is required")
			}

			e, err := a.engine()
			if err != nil {
				return err
			}

			var report engine.Report

			if enable {
				report, err = e.BundlesEnable(cmd.Context(), host)
			} else {
				report, err = e.BundlesDisable(cmd.Context(), host)
			}

			if err != nil {
				return err
			}

			printBundleReport(cmd, report)

			return nil
		},
	}

	cmd.Flags().StringVar(&host, "host", "", "bundle host: claude, gemini or antigravity")

	return cmd
}

func printBundleReport(cmd *cobra.Command, report engine.Report) {
	out := cmd.OutOrStdout()

	for _, result := range report.Bundles {
		line := fmt.Sprintf("  %-13s %s", result.Host, result.Action)

		if result.Version != "" {
			line += " " + result.Version
		}

		if result.Registered {
			line += " (registered)"
		}

		if result.Note != "" {
			line += ": " + result.Note
		}

		fmt.Fprintln(out, line)
	}

	for _, warning := range report.Warnings {
		fmt.Fprintln(out, "  ! "+warning)
	}
}
