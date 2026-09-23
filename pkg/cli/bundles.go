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

			fmt.Fprintf(out, "  %-13s %-8s %-11s %-16s %-14s %s\n", "host", "enabled", "registered", "version", "tier", "cli")

			for _, host := range bundle.Hosts() {
				entry := st.Bundles[string(host)]

				version := entry.Version
				if version == "" {
					version = "-"
				}

				tier := entry.VerifyTier
				if tier == "" {
					tier = "-"
				}

				cli := "missing"
				if _, err := exec.LookPath(host.Binary()); err == nil {
					cli = "found"
				}

				fmt.Fprintf(out, "  %-13s %-8s %-11s %-16s %-14s %s\n", host, onOff(entry.Enabled, "yes", "no"), onOff(entry.Registered, "yes", "no"), version, tier, cli)
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
		Use:   use + " [host]",
		Short: short,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := bundleHostArg(args, host)
			if err != nil {
				return err
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

	cmd.Flags().StringVar(&host, "host", "", "bundle host: claude, gemini or antigravity (positional works too)")

	return cmd
}

// bundleHostArg accepts the host positionally (the form every hint prints) or
// through --host; the two must not disagree.
func bundleHostArg(args []string, host string) (string, error) {
	if len(args) == 0 {
		if host == "" {
			return "", errors.New("host is required: beadle bundles enable claude")
		}

		return host, nil
	}

	if host != "" && host != args[0] {
		return "", fmt.Errorf("host %q does not match --host %q", args[0], host)
	}

	return args[0], nil
}

func printBundleReport(cmd *cobra.Command, report engine.Report) {
	out := cmd.OutOrStdout()

	for _, line := range bundleLines(report.Bundles) {
		fmt.Fprintln(out, line)
	}

	for _, warning := range report.Warnings {
		fmt.Fprintln(out, "  ! "+warning)
	}
}
